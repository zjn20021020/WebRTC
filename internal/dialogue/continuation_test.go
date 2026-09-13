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

type continuationModel struct {
	planModel
	continueInput func(context.Context, llm.ContinuationInput) (bool, error)
}

func (m continuationModel) ClassifyContinuation(ctx context.Context, input llm.ContinuationInput) (bool, error) {
	return m.continueInput(ctx, input)
}

func continuationFixture(t *testing.T, decide func(context.Context, llm.ContinuationInput) (bool, error)) (*Manager, *testClock, *[]Event, *atomic.Int32) {
	t.Helper()
	events, clock, plans := &[]Event{}, &testClock{}, &atomic.Int32{}
	model := continuationModel{planModel: planModel{intentModel: intentModel{
		modelFunc: func(_ context.Context, messages []llm.Message, speak func(string) error) error {
			return speak(messages[len(messages)-1].Content)
		},
		decide: func(context.Context, llm.InterruptionInput) (bool, error) { return false, nil },
	}, plan: func(_ context.Context, input llm.ActionInput) (home.Plan, error) {
		plans.Add(1)
		action := home.GeneralQA
		if input.UserText == "去浇水" {
			action = home.Water
		}
		return home.Plan{ID: "fixture-plan", Steps: []home.Step{{Action: action, Text: input.UserText}}}, nil
	}}, continueInput: decide}
	m := New(model, speechFunc(func(context.Context, string) ([]byte, error) { return bytes.Repeat([]byte{0x33}, 320), nil }), func(e Event) { *events = append(*events, e) })
	m.now = clock.now
	m.SetASRListening(true)
	t.Cleanup(m.Close)
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "s:water", Text: "去浇水"})
	waitGenerated(t, m, 1)
	frameFrom(t, m)
	return m, clock, events, plans
}

func bufferFinal(t *testing.T, m *Manager, clock *testClock, id, text string, count int) {
	t.Helper()
	m.AcceptASR(asr.Event{Event: "asr_final", UtteranceID: id, Text: text})
	clock.advance(finalMergeWindow)
	frameFrom(t, m)
	waitFor(t, func() bool { return bufferedCount(m) == count })
}

func TestQueuedContinuationBeyondASRWindowRunsOneAnswer(t *testing.T) {
	var relations atomic.Int32
	m, clock, events, plans := continuationFixture(t, func(_ context.Context, input llm.ContinuationInput) (bool, error) {
		relations.Add(1)
		if !strings.Contains(input.PendingText, "给我讲个故事吧") {
			t.Error("lost pending story")
		}
		return true, nil
	})
	bufferFinal(t, m, clock, "s:1", "给我讲个故事吧。", 1)
	clock.advance(7 * time.Second)
	bufferFinal(t, m, clock, "s:2", "要和夜晚和月亮相关的。", 2)
	clock.advance(7 * time.Second)
	bufferFinal(t, m, clock, "s:3", "结尾温暖一点。", 3)
	if relations.Load() != 0 || plans.Load() != 1 {
		t.Fatal("queued work started before watering ended")
	}
	finishEpoch(t, m, 1)
	waitGenerated(t, m, 2)
	m.mu.Lock()
	if m.current.stepText != "给我讲个故事吧。 要和夜晚和月亮相关的。 结尾温暖一点。" || len(m.current.input.SourceIDs) != 3 || len(m.interjections) != 0 {
		t.Error("qualifiers were lost, rewritten or independently queued")
	}
	m.mu.Unlock()
	finishEpoch(t, m, 2)
	m.AcceptASR(asr.Event{Event: "asr_final", UtteranceID: "s:2", Text: "duplicate qualifier"})
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil || m.merge != nil || relations.Load() != 2 || plans.Load() != 2 {
		t.Fatal("second answer or duplicate source revived")
	}
	merged := 0
	for _, e := range *events {
		if e.Event == "input_merge" && e.Reason == "queued_continuation" {
			merged++
		}
	}
	if merged != 2 {
		t.Error("semantic joins were not observable")
	}
}

func TestQueuedRelationFalseAndFailurePreserveBothInputs(t *testing.T) {
	for _, mode := range []string{"independent", "invalid", "timeout", "different_session", "source_limit", "text_limit"} {
		t.Run(mode, func(t *testing.T) {
			var relations atomic.Int32
			m, _, events, plans := continuationFixture(t, func(ctx context.Context, _ llm.ContinuationInput) (bool, error) {
				relations.Add(1)
				if mode == "invalid" {
					return true, llm.ErrInvalidContinuationResult
				}
				if mode == "timeout" {
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) > continuationTimeout {
						t.Error("unbounded relation request")
					}
					return true, context.DeadlineExceeded
				}
				return false, nil
			})
			first := asr.Event{Event: "asr_final", UtteranceID: "s:1", Text: "讲个故事"}
			second := asr.Event{Event: "asr_final", UtteranceID: "s:2", Text: "月亮为什么会发光"}
			if mode == "different_session" {
				second.UtteranceID = "other:1"
			}
			if mode == "source_limit" {
				first.SourceIDs = []string{"s:1", "s:a", "s:b", "s:c", "s:d", "s:e"}
			}
			if mode == "text_limit" {
				first.Text = strings.Repeat("字", 1995)
			}
			// Keep a large input from becoming the synthesized output in this limit test.
			model := m.model.(continuationModel)
			model.modelFunc = func(_ context.Context, _ []llm.Message, speak func(string) error) error { return speak("好的。") }
			m.model = model
			m.Accept(first)
			m.Accept(second)
			waitFor(t, func() bool { return bufferedCount(m) == 2 })
			finishEpoch(t, m, 1)
			for i, want := range []string{first.Text, second.Text} {
				waitGenerated(t, m, uint64(i+2))
				m.mu.Lock()
				if m.current.stepText != want {
					t.Error("false/fallback lost original input")
				}
				m.mu.Unlock()
				finishEpoch(t, m, uint64(i+2))
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.current != nil || plans.Load() != 3 {
				t.Fatal("independent requests did not run exactly once each")
			}
			if strings.HasSuffix(mode, "limit") || mode == "different_session" {
				if relations.Load() != 0 {
					t.Error("classified across a hard boundary")
				}
			} else if relations.Load() != 1 {
				t.Error("retried a completed relation check")
			}
			if mode == "invalid" || mode == "timeout" {
				found := false
				for _, e := range *events {
					if e.Event == "input_relation" && e.Fallback && e.Continuation != nil && !*e.Continuation && e.Reason != "" {
						found = true
					}
				}
				if !found {
					t.Error("missing false fallback evidence")
				}
			}
		})
	}
}

func TestStopAndCloseCancelQueuedRelationAndIgnoreLateTrue(t *testing.T) {
	for _, closing := range []bool{false, true} {
		started, release, ended := make(chan context.Context, 1), make(chan struct{}), make(chan struct{})
		m, _, _, plans := continuationFixture(t, func(ctx context.Context, _ llm.ContinuationInput) (bool, error) {
			started <- ctx
			<-release
			defer close(ended)
			return true, nil
		})
		m.Accept(asr.Event{Event: "asr_final", UtteranceID: "s:1", Text: "讲故事"})
		m.Accept(asr.Event{Event: "asr_final", UtteranceID: "s:2", Text: "要月亮的"})
		waitFor(t, func() bool { return bufferedCount(m) == 2 })
		finishEpoch(t, m, 1)
		ctx := <-started
		if closing {
			m.Close()
		} else {
			m.Stop(1)
		}
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Error("stop did not cancel relation HTTP context")
		}
		close(release)
		<-ended
		frameFrom(t, m)
		m.mu.Lock()
		if m.joining != nil || m.current != nil || len(m.interjections) != 0 || plans.Load() != 1 {
			t.Error("late true revived cleared queue")
		}
		m.mu.Unlock()
	}
}

func TestQueuedHeadWaitsForContinuationPartialOrItsTimeout(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		m, clock, _, _ := continuationFixture(t, func(context.Context, llm.ContinuationInput) (bool, error) { return true, nil })
		bufferFinal(t, m, clock, "s:1", "讲个故事", 1)
		clock.advance(7 * time.Second)
		m.AcceptASR(asr.Event{Event: "asr_partial", UtteranceID: "s:2", Text: "要月亮"})
		finishEpoch(t, m, 1)
		m.mu.Lock()
		if m.current != nil {
			t.Error("dispatched base while a related draft was still arriving")
		}
		m.mu.Unlock()
		if timeout {
			clock.advance(unfinishedInputTimeout)
		} else {
			m.AcceptASR(asr.Event{Event: "asr_final", UtteranceID: "s:2", Text: "要月亮相关的"})
			clock.advance(finalMergeWindow)
		}
		frameFrom(t, m)
		waitGenerated(t, m, 2)
		m.mu.Lock()
		if strings.Contains(m.current.stepText, "月亮") == timeout {
			t.Error("partial timeout or final grouping failed")
		}
		m.mu.Unlock()
	}
}
