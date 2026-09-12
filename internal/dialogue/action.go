package dialogue

import (
	"context"
	"errors"
	"log"
	"time"

	"webrtc-interrupt/internal/home"
	"webrtc-interrupt/internal/llm"
)

const actionTimeout = 5 * time.Second

func (m *Manager) respond(t *turn, messages []llm.Message, speak func(string) error) error {
	input := llm.ActionInput{UserText: messages[len(messages)-1].Content, RecentHistory: messages[1 : len(messages)-1]}
	ctx, cancel := context.WithTimeout(t.ctx, actionTimeout)
	started := time.Now()
	call, err := m.model.ClassifyAction(ctx, input)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	cancel()
	if err == nil && (!home.Valid(call.Name) || call.ID == "") {
		err = llm.ErrInvalidActionResult
	}
	latency := time.Since(started).Milliseconds()
	m.mu.Lock()
	if m.current != t || t.ctx.Err() != nil {
		m.mu.Unlock()
		return context.Canceled
	}
	if err != nil {
		t.llmActive = false
		reason := "request_failed"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "timeout"
		} else if errors.Is(err, llm.ErrInvalidActionResult) {
			reason = "invalid_output"
		}
		log.Printf("action epoch=%d fallback=true route=clarification reason=%s latency_ms=%d", t.epoch, reason, latency)
		m.emit(Event{Event: "action_result", Epoch: t.epoch, Fallback: true, Status: "error", Reason: reason, LatencyMS: &latency})
		m.mu.Unlock()
		return speak("小洛克，我还没确认你想做什么，请再说一遍吧。")
	}
	t.toolCall = &call
	t.llmActive = call.Name == home.GeneralQA
	log.Printf("action epoch=%d tool=%s fallback=false latency_ms=%d", t.epoch, call.Name, latency)
	m.emit(Event{Event: "action_result", Epoch: t.epoch, Status: "classified", ToolCall: &call, LatencyMS: &latency})
	m.toolStatusLocked(t, "running")
	m.mu.Unlock()
	if call.Name == home.GeneralQA {
		return m.model.Stream(t.ctx, messages, speak)
	}
	if m.executor == nil {
		return errors.New("home executor is not configured")
	}
	return m.executor.Execute(t.ctx, home.Request{Call: call, Epoch: t.epoch, UserText: input.UserText}, speak)
}

func (m *Manager) toolStatusLocked(t *turn, status string) {
	if t.toolCall == nil {
		return
	}
	log.Printf("tool epoch=%d name=%s status=%s", t.epoch, t.toolCall.Name, status)
	m.emit(Event{Event: "tool_status", Epoch: t.epoch, Status: status, ToolCall: t.toolCall})
}
