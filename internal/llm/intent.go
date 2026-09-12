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
)

//go:embed prompts/interruption.md
var interruptionPrompt string

type InterruptionInput struct {
	PreviousUserText  string `json:"previous_user_text"`
	AssistantResponse string `json:"assistant_response"`
	UserText          string `json:"user_text"`
	IsFinal           bool   `json:"is_final"`
}

// ClassifyInterruption uses a separate, short, non-thinking JSON-mode request.
// The caller owns the deadline; only a validated boolean may authorize a stop.
func (c *Client) ClassifyInterruption(ctx context.Context, input InterruptionInput) (bool, error) {
	if !c.config.Enabled() {
		return false, errors.New("DeepSeek credentials are not configured")
	}
	data, err := json.Marshal(input)
	if err != nil {
		return false, errors.New("invalid interruption input")
	}
	body, err := json.Marshal(struct {
		Model          string            `json:"model"`
		Messages       []Message         `json:"messages"`
		Stream         bool              `json:"stream"`
		MaxTokens      int               `json:"max_tokens"`
		Temperature    float64           `json:"temperature"`
		Thinking       map[string]string `json:"thinking"`
		ResponseFormat map[string]string `json:"response_format"`
	}{c.config.Model, []Message{{Role: "system", Content: interruptionPrompt}, {Role: "user", Content: string(data)}},
		false, 32, 0, map[string]string{"type": "disabled"}, map[string]string{"type": "json_object"}})
	if err != nil {
		return false, errors.New("invalid interruption request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.address, bytes.NewReader(body))
	if err != nil {
		return false, errors.New("invalid DeepSeek endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, errors.New("DeepSeek intent connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("DeepSeek intent request failed (HTTP %d)", resp.StatusCode)
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err != nil || len(data) > 64*1024 {
		return false, errors.New("invalid DeepSeek intent response size")
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &response) != nil || len(response.Error) != 0 || len(response.Choices) != 1 || response.Choices[0].FinishReason != "stop" {
		return false, errors.New("incomplete DeepSeek intent response")
	}
	return parseInterruption(response.Choices[0].Message.Content)
}

func parseInterruption(content string) (bool, error) {
	invalid := errors.New("intent result must be exactly one JSON boolean field: interrupt")
	decoder := json.NewDecoder(bytes.NewBufferString(content))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return false, invalid
	}
	if key, err := decoder.Token(); err != nil || key != "interrupt" {
		return false, invalid
	}
	token, err := decoder.Token()
	value, ok := token.(bool)
	if err != nil || !ok {
		return false, invalid
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return false, invalid
	}
	if _, err := decoder.Token(); err != io.EOF {
		return false, invalid
	}
	return value, nil
}
