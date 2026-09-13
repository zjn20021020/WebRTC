package dialogue

import (
	"context"
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

	"webrtc-interrupt/internal/asr"
	"webrtc-interrupt/internal/home"
	"webrtc-interrupt/internal/llm"
)

type ActionPlanner interface {
	PlanActions(context.Context, llm.ActionInput) (home.Plan, error)
}

type PlanStepStatus struct {
	Action home.Action `json:"action"`
	Text   string      `json:"text"`
	Status string      `json:"status"`
}

type taskPlan struct {
	id    string
	steps []PlanStepStatus
	index int
	input asr.Event
}

func (m *Manager) classifyPlan(ctx context.Context, input llm.ActionInput) (home.Plan, error) {
	if planner, ok := m.model.(ActionPlanner); ok {
		return planner.PlanActions(ctx, input)
	}
	call, err := m.model.ClassifyAction(ctx, input)
	return home.Plan{ID: call.ID, Steps: []home.Step{{Action: call.Name, Text: input.UserText}}}, err
}

func validPlan(plan home.Plan) bool {
	if plan.ID == "" || len(plan.ID) > 200 || len(plan.Steps) == 0 || len(plan.Steps) > home.MaxPlanSteps {
		return false
	}
	for _, step := range plan.Steps {
		if !home.Valid(step.Action) || strings.TrimSpace(step.Text) == "" || utf8.RuneCountInString(step.Text) > 2000 {
			return false
		}
	}
	return true
}

func (m *Manager) installPlanLocked(t *turn, plan home.Plan) {
	// Single calls preserve the existing ID, status and completion contract.
	if len(plan.Steps) > 1 {
		p := &taskPlan{id: plan.ID, input: t.input}
		for _, step := range plan.Steps {
			p.steps = append(p.steps, PlanStepStatus{Action: step.Action, Text: step.Text, Status: "pending"})
		}
		m.plan = p
		t.plan = p
	}
	t.toolCall = &home.Call{ID: plan.ID, Name: plan.Steps[0].Action}
	t.stepText = plan.Steps[0].Text
	if t.plan != nil {
		t.toolCall.ID = fmt.Sprintf("%s:%d", plan.ID, 1)
		m.planStatusLocked("running")
	}
}

func (m *Manager) planStatusLocked(status string) {
	p := m.plan
	if p == nil {
		return
	}
	steps := append([]PlanStepStatus(nil), p.steps...)
	log.Printf("plan id=%q epoch=%d status=%s step=%d total=%d", p.id, m.epoch, status, p.index+1, len(p.steps))
	m.emit(Event{Event: "plan_status", Epoch: m.epoch, Status: status, PlanID: p.id, StepIndex: p.index + 1, StepCount: len(p.steps), PlanSteps: steps})
}

func (m *Manager) cancelPlanLocked(status string) {
	if m.plan == nil {
		return
	}
	for i := m.plan.index; i < len(m.plan.steps); i++ {
		if m.plan.steps[i].Status != "completed" {
			m.plan.steps[i].Status = "cancelled"
		}
	}
	if status == "failed" {
		m.plan.steps[m.plan.index].Status = "failed"
	}
	m.planStatusLocked(status)
	m.plan = nil
}

// Called only after the previous step's final RTP frame was successfully sent.
func (m *Manager) advancePlanLocked(t *turn) bool {
	p := m.plan
	if p == nil || t.plan != p {
		return false
	}
	p.steps[p.index].Status = "completed"
	if p.index+1 == len(p.steps) {
		m.planStatusLocked("completed")
		m.plan = nil
		return false
	}
	p.index++
	next := m.reserveLocked(t.utteranceID)
	next.plan = p
	next.toolCall = &home.Call{ID: fmt.Sprintf("%s:%d", p.id, p.index+1), Name: p.steps[p.index].Action}
	next.stepText = p.steps[p.index].Text
	event := p.input
	event.Text = next.stepText
	m.emit(Event{Event: "plan_step_transition", Epoch: next.epoch, PreviousEpoch: t.epoch, PlanID: p.id, StepIndex: p.index + 1, StepCount: len(p.steps)})
	m.startFinalLocked(next, event)
	return true
}
