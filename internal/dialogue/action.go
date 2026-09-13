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
	if t.toolCall != nil {
		return m.executeStep(t, messages, speak)
	}
	input := llm.ActionInput{UserText: messages[len(messages)-1].Content, RecentHistory: messages[1 : len(messages)-1]}
	m.mu.Lock()
	if m.current != t || t.ctx.Err() != nil {
		m.mu.Unlock()
		return context.Canceled
	}
	input.RecentHistory = append([]llm.Message{m.executionContextLocked(t, false)}, input.RecentHistory...)
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.ctx, actionTimeout)
	started := time.Now()
	var plan home.Plan
	var err error
	var attempt int
	for attempt = 1; attempt <= 2; attempt++ {
		plan, err = m.classifyPlan(ctx, input)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err == nil && !validPlan(plan) {
			err = llm.ErrInvalidActionResult
		}
		validation := llm.ActionValidationReason(err)
		if attempt == 2 || !errors.Is(err, llm.ErrInvalidActionResult) || validation == "refusal" || validation == "provider_error" {
			break
		}
		m.mu.Lock()
		if m.current != t || t.ctx.Err() != nil {
			m.mu.Unlock()
			cancel()
			return context.Canceled
		}
		log.Printf("action epoch=%d attempt=%d retry=true validation=%s", t.epoch, attempt, validation)
		m.emit(Event{Event: "action_retry", Epoch: t.epoch, Status: "retrying", Reason: "invalid_output", Detail: validation})
		m.mu.Unlock()
		input.Repair = true
	}
	cancel()
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
		log.Printf("action epoch=%d attempts=%d fallback=true route=unavailable reason=%s validation=%s latency_ms=%d", t.epoch, attempt, reason, llm.ActionValidationReason(err), latency)
		m.emit(Event{Event: "action_result", Epoch: t.epoch, Fallback: true, Status: "error", Reason: reason, Detail: llm.ActionValidationReason(err), LatencyMS: &latency})
		m.mu.Unlock()
		return speak("小洛克，这次任务没能启动，请再试一次。")
	}
	m.installPlanLocked(t, plan)
	log.Printf("action epoch=%d tool=%s attempts=%d steps=%d fallback=false latency_ms=%d", t.epoch, t.toolCall.Name, attempt, len(plan.Steps), latency)
	result := Event{Event: "action_result", Epoch: t.epoch, Status: "classified", ToolCall: t.toolCall, LatencyMS: &latency}
	if t.plan != nil {
		result.PlanID = t.plan.id
	}
	m.emit(result)
	m.mu.Unlock()
	return m.executeStep(t, messages, speak)
}

func (m *Manager) executeStep(t *turn, messages []llm.Message, speak func(string) error) error {
	m.mu.Lock()
	if m.current != t || t.ctx.Err() != nil {
		m.mu.Unlock()
		return context.Canceled
	}
	call := *t.toolCall
	t.llmActive = call.Name == home.GeneralQA
	if t.plan != nil {
		t.plan.steps[t.plan.index].Status = "running"
		m.planStatusLocked("running")
	}
	m.toolStatusLocked(t, "running")
	if call.Name == home.GeneralQA {
		stepMessages := append([]llm.Message(nil), messages[:len(messages)-1]...)
		state := m.executionContextLocked(t, true)
		stepMessages = append(stepMessages, state, llm.Message{Role: "user", Content: t.stepText})
		m.emit(Event{Event: "response_context", Epoch: t.epoch, Reason: "answer_current_step_now", Detail: state.Content})
		m.mu.Unlock()
		return m.model.Stream(t.ctx, stepMessages, speak)
	}
	m.mu.Unlock()
	if m.executor == nil {
		return errors.New("home executor is not configured")
	}
	return m.executor.Execute(t.ctx, home.Request{Call: call, Epoch: t.epoch, UserText: t.stepText}, speak)
}

func (m *Manager) toolStatusLocked(t *turn, status string) {
	if t.toolCall == nil {
		return
	}
	log.Printf("tool epoch=%d name=%s status=%s", t.epoch, t.toolCall.Name, status)
	event := Event{Event: "tool_status", Epoch: t.epoch, Status: status, ToolCall: t.toolCall}
	if t.plan != nil {
		event.PlanID, event.StepIndex, event.StepCount = t.plan.id, t.plan.index+1, len(t.plan.steps)
	}
	m.emit(event)
}
