package llm

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"webrtc-interrupt/internal/home"
)

//go:embed prompts/action.md
var actionPrompt string

var ErrInvalidActionResult = errors.New("action result must be exactly one allowed native tool call with empty arguments")

type ActionResultError struct{ Reason string }

func (e *ActionResultError) Error() string { return ErrInvalidActionResult.Error() + ": " + e.Reason }
func (e *ActionResultError) Unwrap() error { return ErrInvalidActionResult }

// ActionValidationReason returns only a local diagnostic code, never response text.
func ActionValidationReason(err error) string {
	var invalid *ActionResultError
	if errors.As(err, &invalid) {
		return invalid.Reason
	}
	return ""
}

type ActionInput struct {
	UserText      string    `json:"user_text"`
	RecentHistory []Message `json:"recent_history"`
	Repair        bool      `json:"-"`
}

type functionTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        home.Action    `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
		Strict      bool           `json:"strict,omitempty"`
	} `json:"function"`
}

func actionTools(strict bool) []functionTool {
	var result []functionTool
	for _, definition := range home.Definitions() {
		tool := functionTool{Type: "function"}
		tool.Function.Name, tool.Function.Description = definition.Name, definition.Description
		tool.Function.Strict = strict
		tool.Function.Parameters = map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false}
		result = append(result, tool)
	}
	return result
}

// Strict schemas are a Beta feature of the official API. Keep custom gateways
// and the endpoints used by streaming answers and interruption decisions intact.
func actionEndpoint(address string) (string, bool) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "api.deepseek.com") {
		return address, false
	}
	switch u.Path {
	case "/chat/completions", "/v1/chat/completions", "/beta/chat/completions":
		u.Path = "/beta/chat/completions"
		return u.String(), true
	default:
		return address, false
	}
}

// ClassifyAction uses native function calling, then locally validates the
// allowlist and arguments before a caller can dispatch any executor.
func (c *Client) ClassifyAction(ctx context.Context, input ActionInput) (home.Call, error) {
	if !c.config.Enabled() {
		return home.Call{}, errors.New("DeepSeek credentials are not configured")
	}
	data, err := json.Marshal(input)
	if err != nil {
		return home.Call{}, errors.New("invalid action input")
	}
	prompt := actionPrompt
	if input.Repair {
		prompt += "\nProtocol repair: the previous attempt was rejected and NO tool was executed. Reclassify the same latest user request. Return exactly ONE native function call from the provided tools with arguments {}, no content, no explanation. A cancelled old action is not a second tool to call."
	}
	address, strict := actionEndpoint(c.address)
	body, err := json.Marshal(map[string]any{
		"model": c.config.Model, "messages": []Message{{Role: "system", Content: prompt}, {Role: "user", Content: string(data)}},
		"stream": false, "max_tokens": 128, "temperature": 0,
		"thinking": map[string]string{"type": "disabled"}, "tools": actionTools(strict), "tool_choice": "required",
	})
	if err != nil {
		return home.Call{}, errors.New("invalid action request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(body))
	if err != nil {
		return home.Call{}, errors.New("invalid DeepSeek endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return home.Call{}, ctx.Err()
		}
		return home.Call{}, errors.New("DeepSeek action connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return home.Call{}, fmt.Errorf("DeepSeek action request failed (HTTP %d)", resp.StatusCode)
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if ctx.Err() != nil {
		return home.Call{}, ctx.Err()
	}
	if err != nil {
		return home.Call{}, errors.New("DeepSeek action response read failed")
	}
	if len(data) > 64*1024 {
		return home.Call{}, &ActionResultError{Reason: "response_too_large"}
	}
	return parseAction(data)
}

func parseAction(data []byte) (home.Call, error) {
	var response struct {
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string          `json:"content"`
				Refusal   json.RawMessage `json:"refusal"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      home.Action `json:"name"`
						Arguments string      `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	invalid := func(reason string) (home.Call, error) { return home.Call{}, &ActionResultError{Reason: reason} }
	if json.Unmarshal(data, &response) != nil {
		return invalid("invalid_envelope")
	}
	if len(response.Error) != 0 {
		return invalid("provider_error")
	}
	if len(response.Choices) != 1 {
		return invalid("choice_count")
	}
	choice := response.Choices[0]
	if refusal := string(choice.Message.Refusal); refusal != "" && refusal != "null" && refusal != `""` {
		return invalid("refusal")
	}
	if choice.FinishReason != "tool_calls" {
		switch choice.FinishReason {
		case "length":
			return invalid("truncated")
		case "content_filter":
			return invalid("refusal")
		case "stop":
			return invalid("no_tool_finish")
		default:
			return invalid("finish_reason")
		}
	}
	if strings.TrimSpace(choice.Message.Content) != "" {
		return invalid("unexpected_content")
	}
	if len(choice.Message.ToolCalls) != 1 {
		return invalid("tool_count")
	}
	call := choice.Message.ToolCalls[0]
	if call.Type != "function" {
		return invalid("tool_type")
	}
	if call.ID == "" || len(call.ID) > 200 {
		return invalid("call_id")
	}
	if !home.Valid(call.Function.Name) {
		return invalid("unknown_tool")
	}
	decoder := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return invalid("invalid_arguments")
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return invalid("nonempty_arguments")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return invalid("trailing_arguments")
	}
	return home.Call{ID: call.ID, Name: call.Function.Name}, nil
}
