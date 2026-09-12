package dialogue

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"webrtc-interrupt/internal/asr"
	"webrtc-interrupt/internal/llm"
)

type intentModel struct {
	modelFunc
	decide func(context.Context, llm.InterruptionInput) (bool, error)
}

func (f intentModel) ClassifyInterruption(ctx context.Context, input llm.InterruptionInput) (bool, error) {
	return f.decide(ctx, input)
}

func bufferedCount(m *Manager) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.interjections {
		if c.buffered {
			n++
		}
	}
	return n
}

func finishEpoch(t *testing.T, m *Manager, epoch uint64) {
	t.Helper()
	for i := 0; i < 260; i++ {
		m.mu.Lock()
		finished := m.current == nil || m.epoch != epoch
		m.mu.Unlock()
		if finished {
			return
		}
		frameFrom(t, m)
	}
	t.Fatal("epoch never drained")
}

func installBufferedModel(m *Manager, decide func(context.Context, llm.InterruptionInput) (bool, error)) <-chan []llm.Message {
	started := make(chan []llm.Message, 8)
	m.model = intentModel{modelFunc: func(_ context.Context, messages []llm.Message, emit func(string) error) error {
		started <- messages
		return emit("new answer!")
	}, decide: decide}
	m.speech = speechFunc(func(context.Context, string) ([]byte, error) { return bytes.Repeat([]byte{0x44}, 320), nil })
	return started
}

func TestFalseIntentPreservesPlaybackAndDrainsFinalsInOrder(t *testing.T) {
	m, clock, events := playingFixture(t)
	old := m.current
	var calls atomic.Int32
	started := installBufferedModel(m, func(context.Context, llm.InterruptionInput) (bool, error) { calls.Add(1); return false, nil })
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "a", Text: "question A"})
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "a", Text: "duplicate"})
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "b", Text: "question B"})
	waitFor(t, func() bool { return bufferedCount(m) == 2 })
	if old.ctx.Err() != nil || frameFrom(t, m)[0] != 0x11 {
		t.Fatal("false intent interrupted old speech or kept it ducked")
	}
	select {
	case <-started:
		t.Fatal("buffered question started before playback ended")
	default:
	}
	clock.advance(3 * time.Second)
	finishEpoch(t, m, 1)
	for index, question := range []string{"question A", "question B"} {
		select {
		case messages := <-started:
			if messages[len(messages)-1].Content != question {
				t.Fatal("buffered question order or final text changed")
			}
		case <-time.After(time.Second):
			t.Fatal("buffer was not dispatched after completion")
		}
		waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.current != nil && m.current.generated })
		m.mu.Lock()
		latency := m.current.metrics.FinalToTextMS
		m.mu.Unlock()
		if latency == nil || *latency < 3000 {
			t.Fatal("final-to-text metric omitted buffer waiting time")
		}
		finishEpoch(t, m, uint64(index+2))
	}
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "a", Text: "late duplicate"})
	if calls.Load() != 2 || m.current != nil || m.epoch != 3 {
		t.Fatal("duplicate or deferred question was classified/answered again")
	}
	for _, e := range *events {
		if e.Event == "intent_result" && (e.Interrupt == nil || *e.Interrupt || e.Fallback || e.Status == "error") {
			t.Fatal("normal model false was marked as a fallback")
		}
	}
}

func TestPartialFalseIsRejudgedOnFinalAndOnlyTrueCancels(t *testing.T) {
	m, clock, _ := playingFixture(t)
	old := m.current
	var calls atomic.Int32
	started := installBufferedModel(m, func(_ context.Context, input llm.InterruptionInput) (bool, error) {
		calls.Add(1)
		return input.IsFinal, nil
	})
	m.Accept(asr.Event{Event: "asr_partial", UtteranceID: "a", Text: "\u505c\u4e00\u4e0b"})
	waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return calls.Load() == 1 && !m.interjections[0].running })
	if old.ctx.Err() != nil || len(m.interjections) != 1 {
		t.Fatal("keyword bypassed false model decision")
	}
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "a", Text: "\u505c\u4e00\u4e0b\uff0c\u6362\u4e2a\u95ee\u9898"})
	waitConfirmed(t, m)
	clock.advance(minimumDuck)
	frameFrom(t, m)
	if old.ctx.Err() == nil || len(old.frames) != 0 || calls.Load() != 2 {
		t.Fatal("true final failed to cancel and clear old turn")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("true final never generated a response")
	}
}

func TestFinalSupersedesLatePartialApproval(t *testing.T) {
	m, clock, _ := playingFixture(t)
	old := m.current
	partialStarted, partialExited := make(chan struct{}), make(chan struct{})
	installBufferedModel(m, func(ctx context.Context, input llm.InterruptionInput) (bool, error) {
		if input.IsFinal {
			return false, nil
		}
		close(partialStarted)
		<-ctx.Done()
		close(partialExited)
		return true, nil
	})
	m.Accept(asr.Event{Event: "asr_partial", UtteranceID: "a", Text: "\u505c\u4e00\u4e0b"})
	<-partialStarted
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "a", Text: "\u4e0d\u7528\u505c\uff0c\u7ee7\u7eed"})
	<-partialExited
	waitFor(t, func() bool { return bufferedCount(m) == 1 })
	clock.advance(time.Second)
	if frameFrom(t, m)[0] != 0x11 || old.ctx.Err() != nil || m.epoch != 1 {
		t.Fatal("stale partial approval overrode final rejection")
	}
}

func TestWaitUtteranceDefersPartialDecisionUntilFinal(t *testing.T) {
	for _, final := range []struct {
		text      string
		interrupt bool
	}{
		{"等一下再去种地", false},
		{"等一下，再去种地", false},
		{"等一下", true},
		{"等一下别浇水了去种地", true},
	} {
		t.Run(final.text, func(t *testing.T) {
			m, clock, events := playingFixture(t)
			old := m.current
			var calls atomic.Int32
			installBufferedModel(m, func(_ context.Context, input llm.InterruptionInput) (bool, error) {
				calls.Add(1)
				if !input.IsFinal {
					t.Error("ambiguous wait partial reached the model")
					return true, nil
				}
				return final.interrupt, nil
			})
			for _, text := range []string{"等", "等一下", "等一下再", "等一下再去种地"} {
				m.Accept(asr.Event{Event: "asr_partial", UtteranceID: "wait", Text: text})
				clock.advance(time.Second)
				frameFrom(t, m)
			}
			if calls.Load() != 0 || old.ctx.Err() != nil || m.epoch != 1 {
				t.Fatal("wait prefix caused premature interruption")
			}
			found := false
			for _, event := range *events {
				if event.Event == "intent_status" && event.Reason == "ambiguous_wait" {
					found = true
				}
			}
			if !found {
				t.Fatal("missing waiting-final diagnostic")
			}
			m.Accept(asr.Event{Event: "asr_final", UtteranceID: "wait", Text: final.text})
			if final.interrupt {
				waitConfirmed(t, m)
				clock.advance(minimumDuck)
				frameFrom(t, m)
				if old.ctx.Err() == nil || m.epoch != 2 {
					t.Fatal("explicit final stop no longer works")
				}
			} else {
				waitFor(t, func() bool { return bufferedCount(m) == 1 })
				if old.ctx.Err() != nil || m.epoch != 1 || frameFrom(t, m)[0] != 0x11 {
					t.Fatal("deferred planting cancelled watering")
				}
			}
		})
	}
}

func TestAppendedPartialInvalidatesInFlightApproval(t *testing.T) {
	m, clock, _ := playingFixture(t)
	old := m.current
	started, cancelled := make(chan struct{}), make(chan struct{})
	installBufferedModel(m, func(ctx context.Context, input llm.InterruptionInput) (bool, error) {
		if input.IsFinal {
			return false, nil
		}
		close(started)
		<-ctx.Done()
		close(cancelled)
		return true, nil
	})
	m.Accept(asr.Event{Event: "asr_partial", UtteranceID: "a", Text: "停一下"})
	<-started
	m.Accept(asr.Event{Event: "asr_partial", UtteranceID: "a", Text: "停一下这个词是什么意思"})
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("appended words did not cancel the obsolete request")
	}
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "a", Text: "停一下这个词是什么意思"})
	waitFor(t, func() bool { return bufferedCount(m) == 1 })
	clock.advance(time.Second)
	if old.ctx.Err() != nil || m.epoch != 1 || frameFrom(t, m)[0] != 0x11 {
		t.Fatal("late prefix approval cancelled current playback")
	}
}

func TestAppendedPartialRevokesApprovalBeforeHardStop(t *testing.T) {
	m, clock, _ := playingFixture(t)
	old := m.current
	installBufferedModel(m, func(_ context.Context, input llm.InterruptionInput) (bool, error) { return !input.IsFinal, nil })
	m.Accept(asr.Event{Event: "asr_partial", UtteranceID: "a", Text: "停一下"})
	waitConfirmed(t, m)
	m.Accept(asr.Event{Event: "asr_partial", UtteranceID: "a", Text: "停一下这个词是什么意思"})
	clock.advance(minimumDuck)
	frameFrom(t, m)
	if old.ctx.Err() != nil || m.epoch != 1 {
		t.Fatal("superseded pending approval was applied on next RTP tick")
	}
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "a", Text: "停一下这个词是什么意思"})
	waitFor(t, func() bool { return bufferedCount(m) == 1 })
}

func TestPlaybackCompletionInvalidatesPendingIntent(t *testing.T) {
	m, _, _ := playingFixture(t)
	judgeStarted, judgeExited := make(chan struct{}), make(chan struct{})
	started := installBufferedModel(m, func(ctx context.Context, _ llm.InterruptionInput) (bool, error) {
		close(judgeStarted)
		<-ctx.Done()
		close(judgeExited)
		return true, nil
	})
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "a", Text: "next question"})
	<-judgeStarted
	finishEpoch(t, m, 1)
	<-judgeExited
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("pending final lost when playback finished")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.epoch != 2 || m.current == nil || m.current.ctx.Err() != nil {
		t.Fatal("late approval cancelled next turn")
	}
}

func TestIntentErrorDefersInsteadOfInterrupting(t *testing.T) {
	for _, tc := range []struct {
		failure error
		reason  string
	}{
		{llm.ErrInvalidIntentResult, "invalid_output"},
		{fmt.Errorf("wrapped: %w", llm.ErrInvalidIntentResult), "invalid_output"},
		{context.DeadlineExceeded, "timeout"},
		{errors.New("connection failed"), "request_failed"},
	} {
		t.Run(tc.failure.Error(), func(t *testing.T) {
			m, _, events := playingFixture(t)
			old := m.current
			installBufferedModel(m, func(context.Context, llm.InterruptionInput) (bool, error) { return true, tc.failure })
			m.Accept(asr.Event{Event: "asr_final", UtteranceID: "a", Text: "next question"})
			waitFor(t, func() bool { return bufferedCount(m) == 1 })
			if old.ctx.Err() != nil || frameFrom(t, m)[0] != 0x11 {
				t.Fatal("invalid result authorized cancellation")
			}
			found := false
			for _, e := range *events {
				if e.Event == "intent_result" && e.Status == "error" && e.Interrupt != nil && !*e.Interrupt && e.Fallback && e.Reason == tc.reason {
					found = true
				}
			}
			if !found {
				t.Fatal("explicit false fallback or failure reason missing")
			}
		})
	}
}

func TestBufferLimitAndManualStopClearPendingQuestions(t *testing.T) {
	m, clock, events := playingFixture(t)
	installBufferedModel(m, func(context.Context, llm.InterruptionInput) (bool, error) { return false, nil })
	for i := 0; i < maxInterjections+1; i++ {
		m.Accept(asr.Event{Event: "asr_final", UtteranceID: fmt.Sprint(i), Text: "question"})
	}
	waitFor(t, func() bool { return bufferedCount(m) == maxInterjections })
	m.Stop(1)
	clock.advance(time.Minute)
	if !bytes.Equal(frameFrom(t, m), silenceFrame()) || len(m.interjections) != 0 {
		t.Fatal("manual stop left auto-playing cached questions")
	}
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "0", Text: "late final"})
	if m.current != nil {
		t.Fatal("cleared input revived after stop")
	}
	found := false
	for _, e := range *events {
		if e.Event == "input_rejected" && e.Reason == "queue_full" {
			found = true
		}
	}
	if !found {
		t.Fatal("queue overflow was silent")
	}
}

func TestMissingFinalDoesNotBlockBufferedQuestionsForever(t *testing.T) {
	m, clock, _ := playingFixture(t)
	started := installBufferedModel(m, func(context.Context, llm.InterruptionInput) (bool, error) { return false, nil })
	m.Accept(asr.Event{Event: "asr_partial", UtteranceID: "missing", Text: "hmm"})
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "a", Text: "next question"})
	waitFor(t, func() bool { return bufferedCount(m) == 1 })
	finishEpoch(t, m, 1)
	clock.advance(unfinishedInputTimeout)
	frameFrom(t, m)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("missing final blocked all later inputs")
	}
}
