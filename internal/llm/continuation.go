package llm

import (
	"context"
	_ "embed"
	"errors"
)

//go:embed prompts/continuation.md
var continuationPrompt string

var ErrInvalidContinuationResult = errors.New("continuation result must be exactly one JSON boolean field: continuation")

type ContinuationInput struct {
	PendingText string `json:"pending_text"`
	UserText    string `json:"user_text"`
}

// This only groups unstarted inputs. It never authorizes playback interruption.
func (c *Client) ClassifyContinuation(ctx context.Context, input ContinuationInput) (bool, error) {
	return c.classifyBoolean(ctx, continuationPrompt, input, "continuation", ErrInvalidContinuationResult)
}
