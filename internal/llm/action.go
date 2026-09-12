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
	"strings"

	"webrtc-interrupt/internal/home"
)

//go:embed prompts/action.md
var actionPrompt string

var ErrInvalidActionResult = errors.New("action result must be exactly one allowed native tool call with empty arguments")

type ActionInput struct {
	UserText      string    `json:"user_text"`
	RecentHistory []Message `json:"recent_history"`
}

type functionTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        home.Action    `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

func actionTools() []functionTool {
	var result []functionTool
	for _, definition := range home.Definitions() {
		tool := functionTool{Type: "function"}
		tool.Function.Name, tool.Function.Description = definition.Name, definition.Description
		tool.Function.Parameters = map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false}
		result = append(result, tool)
	}
	return result
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
	body, err := json.Marshal(map[string]any{
		"model": c.config.Model, "messages": []Message{{Role: "system", Content: home.RoleSkill + "\n\n" + actionPrompt}, {Role: "user", Content: string(data)}},
		"stream": false, "max_tokens": 128, "temperature": 0,
		"thinking": map[string]string{"type": "disabled"}, "tools": actionTools(), "tool_choice": "required",
	})
	if err != nil {
		return home.Call{}, errors.New("invalid action request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.address, bytes.NewReader(body))
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
		return home.Call{}, ErrInvalidActionResult
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
	invalid := ErrInvalidActionResult
	if json.Unmarshal(data, &response) != nil || len(response.Error) != 0 || len(response.Choices) != 1 {
		return home.Call{}, invalid
	}
	choice := response.Choices[0]
	if choice.FinishReason != "tool_calls" || strings.TrimSpace(choice.Message.Content) != "" || len(choice.Message.ToolCalls) != 1 {
		return home.Call{}, invalid
	}
	if refusal := string(choice.Message.Refusal); refusal != "" && refusal != "null" && refusal != `""` {
		return home.Call{}, invalid
	}
	call := choice.Message.ToolCalls[0]
	if call.Type != "function" || call.ID == "" || len(call.ID) > 200 || !home.Valid(call.Function.Name) {
		return home.Call{}, invalid
	}
	decoder := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return home.Call{}, invalid
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return home.Call{}, invalid
	}
	if _, err := decoder.Token(); err != io.EOF {
		return home.Call{}, invalid
	}
	return home.Call{ID: call.ID, Name: call.Function.Name}, nil
}
