package dialogue

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"webrtc-interrupt/internal/asr"
	"webrtc-interrupt/internal/llm"
)

func TestCrossFinalWaitContinuationDefersWithoutCancelling(t *testing.T) {
	m, clock, _ := playingFixture(t)
	old := m.current
	var calls atomic.Int32
	installBufferedModel(m, func(_ context.Context, input llm.InterruptionInput) (bool, error) {
		calls.Add(1)
		if !input.IsFinal || !strings.Contains(input.UserText, "等一下") || !strings.Contains(input.UserText, "再去种地") {
			t.Errorf("classified fragment: %+v", input)
		}
		return false, nil
	})
	m.AcceptASR(asr.Event{Event: "asr_final", UtteranceID: "session:1", Text: "等一下。", BeginTime: 100, EndTime: 900})
	clock.advance(900 * time.Millisecond)
	frameFrom(t, m)
	if calls.Load() != 0 || old.ctx.Err() != nil {
		t.Fatal("wait prefix authorized early interruption")
	}
	m.AcceptASR(asr.Event{Event: "asr_partial", UtteranceID: "session:2", Text: "再去", BeginTime: 1500, EndTime: 1800})
	clock.advance(time.Second)
	frameFrom(t, m)
	if calls.Load() != 0 {
		t.Fatal("continuation partial was classified")
	}
	m.AcceptASR(asr.Event{Event: "asr_final", UtteranceID: "session:2", Text: "再去种地。", BeginTime: 1500, EndTime: 2200})
	clock.advance(ambiguousMergeWindow)
	frameFrom(t, m)
	waitFor(t, func() bool { return bufferedCount(m) == 1 })
	if calls.Load() != 1 || old.ctx.Err() != nil {
		t.Fatal("merged request did not preserve old task")
	}
	m.AcceptASR(asr.Event{Event: "asr_final", UtteranceID: "session:2", Text: "再去种地。"})
	clock.advance(2 * time.Second)
	frameFrom(t, m)
	if m.merge != nil || calls.Load() != 1 {
		t.Fatal("duplicate source final revived merged request")
	}
}

func TestStandaloneWaitCommitsAfterBoundedMergeWindow(t *testing.T) {
	m, clock, _ := playingFixture(t)
	m.AcceptASR(asr.Event{Event: "asr_final", UtteranceID: "s:0", Text: "等一下。"})
	clock.advance(ambiguousMergeWindow - time.Millisecond)
	frameFrom(t, m)
	if m.epoch != 1 {
		t.Fatal("standalone wait cut too early")
	}
	clock.advance(time.Millisecond)
	frameFrom(t, m)
	waitConfirmed(t, m)
	clock.advance(minimumDuck)
	frameFrom(t, m)
	waitEpoch(t, m, 2)
}

func TestASRFailureDiscardsUncommittedWaitPrefix(t *testing.T) {
	m, clock, _ := playingFixture(t)
	old := m.current
	var calls atomic.Int32
	installBufferedModel(m, func(context.Context, llm.InterruptionInput) (bool, error) { calls.Add(1); return true, nil })
	m.AcceptASR(asr.Event{Event: "asr_final", UtteranceID: "s:1", Text: "等一下"})
	m.SetASRListening(false)
	clock.advance(ambiguousMergeWindow)
	frameFrom(t, m)
	if calls.Load() != 0 || old.ctx.Err() != nil || m.merge != nil {
		t.Fatal("ASR failure committed an incomplete logical input")
	}
}

func TestFullInputQueueAlsoDiscardsMergeCollector(t *testing.T) {
	m, clock, _ := playingFixture(t)
	installBufferedModel(m, func(context.Context, llm.InterruptionInput) (bool, error) { return false, nil })
	for i := 0; i < maxInterjections; i++ {
		m.Accept(asr.Event{Event: "asr_final", UtteranceID: string(rune('a' + i)), Text: "待会种菜"})
	}
	waitFor(t, func() bool { return bufferedCount(m) == maxInterjections })
	m.AcceptASR(asr.Event{Event: "asr_final", UtteranceID: "s:1", Text: "再施肥"})
	if m.merge != nil {
		t.Fatal("rejected queue input left a live collector")
	}
	m.Stop(1)
	m.AcceptASR(asr.Event{Event: "asr_final", UtteranceID: "s:1", Text: "再施肥"})
	clock.advance(finalMergeWindow)
	frameFrom(t, m)
	if m.current != nil {
		t.Fatal("rejected input revived after space became available")
	}
}

func TestCrossFinalBoundaryAndIncompleteTail(t *testing.T) {
	for _, tc := range []struct {
		name string
		next asr.Event
	}{
		{"audio_gap", asr.Event{Event: "asr_final", UtteranceID: "s:2", Text: "去施肥", BeginTime: 4000, EndTime: 4500}},
		{"new_asr_session", asr.Event{Event: "asr_final", UtteranceID: "other:0", Text: "去施肥", BeginTime: 1200, EndTime: 1700}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, clock, _ := playingFixture(t)
			inputs := make(chan string, 2)
			installBufferedModel(m, func(_ context.Context, input llm.InterruptionInput) (bool, error) {
				inputs <- input.UserText
				return false, nil
			})
			m.AcceptASR(asr.Event{Event: "asr_final", UtteranceID: "s:1", Text: "先浇水", BeginTime: 100, EndTime: 900})
			m.AcceptASR(tc.next)
			clock.advance(finalMergeWindow)
			frameFrom(t, m)
			waitFor(t, func() bool { return bufferedCount(m) == 2 })
			for i := 0; i < 2; i++ {
				if strings.Contains(<-inputs, "先浇水 去施肥") {
					t.Fatal("merged across boundary")
				}
			}
		})
	}
	t.Run("missing_tail_final", func(t *testing.T) {
		m, clock, _ := playingFixture(t)
		var calls atomic.Int32
		installBufferedModel(m, func(context.Context, llm.InterruptionInput) (bool, error) { calls.Add(1); return true, nil })
		m.AcceptASR(asr.Event{Event: "asr_final", UtteranceID: "s:1", Text: "等一下"})
		m.AcceptASR(asr.Event{Event: "asr_partial", UtteranceID: "s:2", Text: "再去"})
		clock.advance(maxMergeDuration)
		frameFrom(t, m)
		if calls.Load() != 0 || m.merge != nil || len(m.interjections) != 0 {
			t.Fatal("incomplete continuation executed a prefix")
		}
		m.AcceptASR(asr.Event{Event: "asr_final", UtteranceID: "s:2", Text: "再去种菜"})
		if m.merge != nil {
			t.Fatal("late rejected continuation revived")
		}
	})
}

func TestStopAndDisconnectDiscardUncommittedMergedInput(t *testing.T) {
	for _, closeSession := range []bool{false, true} {
		m, clock, _ := playingFixture(t)
		m.AcceptASR(asr.Event{Event: "asr_final", UtteranceID: "s:1", Text: "先浇水"})
		m.AcceptASR(asr.Event{Event: "asr_partial", UtteranceID: "s:2", Text: "再施肥"})
		if closeSession {
			m.Close()
		} else {
			m.Stop(1)
		}
		clock.advance(maxMergeDuration)
		frameFrom(t, m)
		if m.merge != nil || m.current != nil || len(m.interjections) != 0 {
			t.Fatal("stop retained merged inputs")
		}
	}
}
