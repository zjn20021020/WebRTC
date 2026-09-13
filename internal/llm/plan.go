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
	"unicode/utf8"

	"webrtc-interrupt/internal/home"
)

//go:embed prompts/plan.md
var planPrompt string

func planTool(strict bool) functionTool {
	t := functionTool{Type: "function"}
	t.Function.Name = "execute_plan"
	t.Function.Description = "Execute one validated, ordered home task plan."
	t.Function.Strict = strict
	names := make([]string, 0, len(home.Definitions()))
	for _, definition := range home.Definitions() {
		names = append(names, string(definition.Name))
	}
	t.Function.Parameters = map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"steps"},
		"properties": map[string]any{"steps": map[string]any{
			"type": "array", "minItems": 1, "maxItems": home.MaxPlanSteps,
			"items": map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"action", "text"},
				"properties": map[string]any{
					"action": map[string]any{"type": "string", "enum": names},
					"text":   map[string]any{"type": "string"},
				},
			},
		}},
	}
	return t
}

// PlanActions uses one native tool to make sequence order explicit, instead of
// treating parallel tool_calls as a dependency graph.
func (c *Client) PlanActions(ctx context.Context, input ActionInput) (home.Plan, error) {
	if !c.config.Enabled() {
		return home.Plan{}, errors.New("DeepSeek credentials are not configured")
	}
	data, err := json.Marshal(input)
	if err != nil {
		return home.Plan{}, errors.New("invalid plan input")
	}
	prompt := planPrompt
	if input.Repair {
		prompt += "\nProtocol repair: no step was executed. Return one execute_plan call with only steps, each containing action and text."
	}
	address, strict := actionEndpoint(c.address)
	body, err := json.Marshal(map[string]any{
		"model": c.config.Model, "messages": []Message{{Role: "system", Content: prompt}, {Role: "user", Content: string(data)}},
		"stream": false, "max_tokens": 768, "temperature": 0, "thinking": map[string]string{"type": "disabled"},
		"tools": []functionTool{planTool(strict)}, "tool_choice": "required",
	})
	if err != nil {
		return home.Plan{}, errors.New("invalid plan request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(body))
	if err != nil {
		return home.Plan{}, errors.New("invalid DeepSeek endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return home.Plan{}, ctx.Err()
		}
		return home.Plan{}, errors.New("DeepSeek plan connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return home.Plan{}, fmt.Errorf("DeepSeek plan request failed (HTTP %d)", resp.StatusCode)
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if ctx.Err() != nil {
		return home.Plan{}, ctx.Err()
	}
	if err != nil || len(data) > 64*1024 {
		return home.Plan{}, errors.New("invalid DeepSeek plan response size or read failure")
	}
	return parsePlan(data)
}

func parsePlan(data []byte) (home.Plan, error) {
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Finish  string `json:"finish_reason"`
			Message struct {
				Content string          `json:"content"`
				Refusal json.RawMessage `json:"refusal"`
				Calls   []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	invalid := func(reason string) (home.Plan, error) { return home.Plan{}, &ActionResultError{Reason: reason} }
	if json.Unmarshal(data, &envelope) != nil {
		return invalid("invalid_envelope")
	}
	if len(envelope.Error) != 0 {
		return invalid("provider_error")
	}
	if len(envelope.Choices) != 1 {
		return invalid("choice_count")
	}
	choice := envelope.Choices[0]
	if refusal := string(choice.Message.Refusal); (refusal != "" && refusal != "null" && refusal != `""`) || choice.Finish == "content_filter" {
		return invalid("refusal")
	}
	if choice.Finish != "tool_calls" {
		return invalid("finish_reason")
	}
	if strings.TrimSpace(choice.Message.Content) != "" {
		return invalid("unexpected_content")
	}
	if len(choice.Message.Calls) != 1 {
		return invalid("tool_count")
	}
	call := choice.Message.Calls[0]
	if call.ID == "" || len(call.ID) > 200 || call.Type != "function" || call.Function.Name != "execute_plan" {
		return invalid("invalid_plan_call")
	}
	// Token walking catches duplicate keys as well as unknown/missing fields.
	d := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	token := func(expected any) bool { value, err := d.Token(); return err == nil && value == expected }
	if !token(json.Delim('{')) || !token("steps") || !token(json.Delim('[')) {
		return invalid("invalid_plan")
	}
	plan := home.Plan{ID: call.ID}
	for d.More() {
		if len(plan.Steps) >= home.MaxPlanSteps || !token(json.Delim('{')) {
			return invalid("plan_limit")
		}
		step := home.Step{}
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] {
				return invalid("invalid_step")
			}
			seen[name] = true
			var value string
			if d.Decode(&value) != nil {
				return invalid("invalid_step")
			}
			switch name {
			case "action":
				step.Action = home.Action(value)
			case "text":
				step.Text = strings.TrimSpace(value)
			default:
				return invalid("invalid_step")
			}
		}
		if !token(json.Delim('}')) || len(seen) != 2 || !home.Valid(step.Action) || step.Text == "" || utf8.RuneCountInString(step.Text) > 2000 {
			return invalid("invalid_step")
		}
		plan.Steps = append(plan.Steps, step)
	}
	if len(plan.Steps) == 0 || !token(json.Delim(']')) || !token(json.Delim('}')) {
		return invalid("invalid_plan")
	}
	if _, err := d.Token(); err != io.EOF {
		return invalid("trailing_arguments")
	}
	return plan, nil
}
