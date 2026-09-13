package dialogue

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"webrtc-interrupt/internal/asr"
	"webrtc-interrupt/internal/home"
	"webrtc-interrupt/internal/llm"
)

type planModel struct {
	intentModel
	plan func(context.Context, llm.ActionInput) (home.Plan, error)
}

func (m planModel) PlanActions(ctx context.Context, input llm.ActionInput) (home.Plan, error) {
	return m.plan(ctx, input)
}

func planFixture(t *testing.T) (*Manager, *testClock, *[]Event, *atomic.Int32) {
	t.Helper()
	clock, events, calls := &testClock{}, &[]Event{}, &atomic.Int32{}
	model := planModel{intentModel: intentModel{
		modelFunc: func(_ context.Context, messages []llm.Message, speak func(string) error) error {
			if messages[len(messages)-1].Content != "一加一等于几" {
				t.Errorf("QA received whole plan instead of subquestion: %s", messages[len(messages)-1].Content)
			}
			return speak("等于二。")
		},
		decide: func(_ context.Context, input llm.InterruptionInput) (bool, error) {
			return input.UserText == "去收菜", nil
		},
	}, plan: func(_ context.Context, input llm.ActionInput) (home.Plan, error) {
		calls.Add(1)
		steps := []home.Step{{Action: home.Water, Text: "去浇水"}, {Action: home.GeneralQA, Text: "一加一等于几"}, {Action: home.Fertilize, Text: "去施肥"}}
		if input.UserText == "贴贴" {
			steps = []home.Step{{Action: home.Affection, Text: input.UserText}}
		}
		if input.UserText == "去收菜" {
			steps = []home.Step{{Action: home.Harvest, Text: input.UserText}}
		}
		return home.Plan{ID: input.UserText, Steps: steps}, nil
	}}
	m := New(model, speechFunc(func(context.Context, string) ([]byte, error) { return bytes.Repeat([]byte{0x33}, 320), nil }), func(e Event) { *events = append(*events, e) })
	m.now = clock.now
	m.SetASRListening(true)
	t.Cleanup(m.Close)
	return m, clock, events, calls
}

func waitGenerated(t *testing.T, m *Manager, epoch uint64) {
	t.Helper()
	waitFor(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.current != nil && m.current.epoch == epoch && m.current.generated
	})
}

func TestPlanOrdersPlaybackBeforeNextStepAndDeferredInputs(t *testing.T) {
	m, _, events, calls := planFixture(t)
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "plan", Text: "先浇水再回答一加一最后施肥"})
	waitGenerated(t, m, 1)
	frameFrom(t, m)
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "praise", Text: "贴贴"})
	waitFor(t, func() bool { return bufferedCount(m) == 1 })
	// A failed RTP write must not complete the current step or start the next.
	if err := m.WriteFrame(func([]byte) error { return errors.New("blocked RTP") }); err == nil {
		t.Fatal("write error lost")
	}
	m.mu.Lock()
	if m.epoch != 1 || m.plan.index != 0 {
		t.Error("next step started before audio was sent")
	}
	m.mu.Unlock()
	for epoch := uint64(1); epoch <= 3; epoch++ {
		waitGenerated(t, m, epoch)
		if calls.Load() != 1 || bufferedCount(m) != 1 {
			t.Fatal("replanned a step or dispatched cache before plan finished")
		}
		finishEpoch(t, m, epoch)
	}
	waitGenerated(t, m, 4)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.plan != nil || m.current.toolCall.Name != home.Affection || calls.Load() != 2 {
		t.Fatal("cache did not drain after entire plan")
	}
	ids := map[string]bool{}
	completed := 0
	for _, e := range *events {
		if e.Event == "tool_status" && e.Epoch <= 3 && e.Status == "running" {
			if e.PlanID == "" || e.StepIndex != int(e.Epoch) || ids[e.ToolCall.ID] {
				t.Error("step lost identity or order")
			}
			ids[e.ToolCall.ID] = true
		}
		if e.Event == "plan_status" && e.Status == "completed" {
			completed++
		}
	}
	if len(ids) != 3 || completed != 1 {
		t.Error("missing plan lifecycle events")
	}
}

func TestPlanStopCancelsRemainderAndOldCache(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{false: "voice", true: "manual"}[manual], func(t *testing.T) {
			m, clock, events, _ := planFixture(t)
			m.Accept(asr.Event{Event: "asr_final", UtteranceID: "plan", Text: "组合任务"})
			waitGenerated(t, m, 1)
			frameFrom(t, m)
			m.Accept(asr.Event{Event: "asr_final", UtteranceID: "cache", Text: "贴贴"})
			waitFor(t, func() bool { return bufferedCount(m) == 1 })
			m.mu.Lock()
			old := m.current
			m.mu.Unlock()
			if manual {
				m.Stop(1)
			} else {
				m.Accept(asr.Event{Event: "asr_final", UtteranceID: "switch", Text: "去收菜"})
				waitConfirmed(t, m)
				clock.advance(minimumDuck)
				frameFrom(t, m)
				waitGenerated(t, m, 2)
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			if old.ctx.Err() == nil || m.plan != nil || len(m.interjections) != 0 {
				t.Fatal("stop left work or cached input alive")
			}
			found := false
			for _, e := range *events {
				if e.Event == "plan_status" && e.Status == "cancelled" {
					found = true
					for _, step := range e.PlanSteps {
						if step.Status != "cancelled" {
							t.Error("pending step survived interruption")
						}
					}
				}
			}
			if !found {
				t.Error("plan cancellation was not observable")
			}
		})
	}
}

func TestPlanFailureDoesNotExecuteRemainder(t *testing.T) {
	m, _, events, _ := planFixture(t)
	var executed atomic.Int32
	m.executor = executorFunc(func(context.Context, home.Request, func(string) error) error {
		executed.Add(1)
		return errors.New("tool offline")
	})
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "p", Text: "组合任务"})
	waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.current == nil })
	m.mu.Lock()
	defer m.mu.Unlock()
	if executed.Load() != 1 || m.plan != nil {
		t.Fatal("failed plan continued")
	}
	found := false
	for _, e := range *events {
		if e.Event == "plan_status" && e.Status == "failed" {
			found = len(e.PlanSteps) == 3 && e.PlanSteps[0].Status == "failed" && e.PlanSteps[1].Status == "cancelled" && e.PlanSteps[2].Status == "cancelled"
		}
	}
	if !found {
		t.Fatal("missing failure evidence")
	}
}

func TestMergedFinalsReachPlannerOnceAndAllowLongFirstSentence(t *testing.T) {
	m, clock, _, calls := planFixture(t)
	model := m.model.(planModel)
	original := model.plan
	model.plan = func(ctx context.Context, input llm.ActionInput) (home.Plan, error) {
		if input.UserText != "先浇水。 再回答一加一最后施肥。" {
			t.Errorf("incomplete planning input: %s", input.UserText)
		}
		return original(ctx, input)
	}
	m.model = model
	m.AcceptASR(asr.Event{Event: "asr_partial", UtteranceID: "s:1", Text: "先浇水"})
	clock.advance(8 * time.Second)
	frameFrom(t, m)
	m.AcceptASR(asr.Event{Event: "asr_final", UtteranceID: "s:1", Text: "先浇水。"})
	clock.advance(300 * time.Millisecond)
	m.AcceptASR(asr.Event{Event: "asr_final", UtteranceID: "s:2", Text: "再回答一加一最后施肥。"})
	if calls.Load() != 0 {
		t.Fatal("planner ran before merge commit")
	}
	clock.advance(finalMergeWindow)
	frameFrom(t, m)
	waitGenerated(t, m, 1)
	if calls.Load() != 1 {
		t.Fatal("merged input planned multiple times")
	}
}

func TestMergeLimitsRejectWholeGroup(t *testing.T) {
	for _, tooLong := range []bool{false, true} {
		m, clock, _, calls := planFixture(t)
		for i := 0; i <= maxMergeParts; i++ {
			text := "去浇水"
			if tooLong {
				text = strings.Repeat("种", 400)
			}
			m.AcceptASR(asr.Event{Event: "asr_final", UtteranceID: "s:" + string(rune('a'+i)), Text: text})
			if tooLong && i == 5 {
				break
			}
		}
		clock.advance(2 * time.Second)
		frameFrom(t, m)
		if calls.Load() != 0 {
			t.Fatal("limit executed a prefix")
		}
	}
}

func TestPendingIntentIsRecheckedAcrossPlanStep(t *testing.T) {
	m, _, _, _ := planFixture(t)
	started, rechecked := make(chan struct{}), make(chan struct{})
	model := m.model.(planModel)
	var decisions atomic.Int32
	model.decide = func(ctx context.Context, input llm.InterruptionInput) (bool, error) {
		if decisions.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return false, ctx.Err()
		}
		if input.CurrentTool != string(home.GeneralQA) {
			t.Error("recheck used previous step context")
		}
		close(rechecked)
		return false, nil
	}
	m.model = model
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "p", Text: "组合任务"})
	waitGenerated(t, m, 1)
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "q", Text: "贴贴"})
	<-started
	finishEpoch(t, m, 1)
	waitGenerated(t, m, 2)
	frameFrom(t, m)
	select {
	case <-rechecked:
	case <-time.After(time.Second):
		t.Fatal("pending intent lost at step boundary")
	}
	waitFor(t, func() bool { return bufferedCount(m) == 1 })
}

func TestCancelledPlanCannotInstallLateResult(t *testing.T) {
	m, _, _, _ := planFixture(t)
	started, release, ended := make(chan struct{}), make(chan struct{}), make(chan struct{})
	model := m.model.(planModel)
	original := model.plan
	model.plan = func(ctx context.Context, input llm.ActionInput) (home.Plan, error) {
		close(started)
		<-release
		defer close(ended)
		return original(ctx, input)
	}
	m.model = model
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "p", Text: "组合任务"})
	<-started
	m.mu.Lock()
	old := m.current
	m.mu.Unlock()
	m.Stop(1)
	close(release)
	<-ended
	waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return !old.llmActive })
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil || m.plan != nil {
		t.Fatal("cancelled planner installed stale steps")
	}
}
