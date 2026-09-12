package dialogue

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"webrtc-interrupt/internal/asr"
	"webrtc-interrupt/internal/interrupt"
	"webrtc-interrupt/internal/llm"
)

type testClock struct{ ticks atomic.Int64 }

func (c *testClock) now() time.Time          { return time.Unix(100, c.ticks.Load()) }
func (c *testClock) advance(d time.Duration) { c.ticks.Add(int64(d)) }

func playingFixture(t *testing.T) (*Manager, *testClock, *[]Event) {
	t.Helper()
	clock := &testClock{}
	events := &[]Event{}
	m := New(nil, nil, func(e Event) { *events = append(*events, e) })
	m.now = clock.now
	ctx, cancel := context.WithCancelCause(context.Background())
	m.epoch = 1
	m.current = &turn{epoch: 1, utteranceID: "old", ctx: ctx, fail: cancel, cancel: func() { cancel(context.Canceled) },
		frames: make(chan []byte, 4), stage: "speaking", playing: true, generated: true, text: "old answer",
		startedAt: clock.now().Add(-time.Second), speechEndAt: clock.now().Add(-2 * time.Second)}
	for _, b := range []byte{0x11, 0x22, 0x33, 0x44} {
		m.current.frames <- bytes.Repeat([]byte{b}, 160)
	}
	m.seen["old"] = true
	m.SetASRListening(true)
	t.Cleanup(m.Close)
	return m, clock, events
}

func frameFrom(t *testing.T, m *Manager) []byte {
	t.Helper()
	var frame []byte
	if err := m.WriteFrame(func(b []byte) error { frame = bytes.Clone(b); return nil }); err != nil {
		t.Fatal(err)
	}
	return frame
}

func TestSoftPauseResumesWithoutSkippingAudio(t *testing.T) {
	m, clock, events := playingFixture(t)
	old := m.current
	if frameFrom(t, m)[0] != 0x11 {
		t.Fatal("incorrect first frame")
	}
	m.ObserveVAD(interrupt.SpeechStarted, clock.now().Add(-200*time.Millisecond))
	clock.advance(20 * time.Millisecond)
	for i := 0; i < 3; i++ {
		if !bytes.Equal(frameFrom(t, m), silenceFrame()) {
			t.Fatal("audio escaped soft pause")
		}
	}
	if old.ctx.Err() != nil || len(old.frames) != 3 || m.epoch != 1 {
		t.Fatal("soft pause cancelled or consumed the old response")
	}
	if old.metrics.SpeechToPauseMS == nil || *old.metrics.SpeechToPauseMS != 220 {
		t.Fatal("VAD hysteresis missing from stop latency")
	}
	m.ObserveVAD(interrupt.SpeechEnded, time.Time{})
	m.Accept(asr.Event{Event: "asr_partial", UtteranceID: "noise", Text: " ...!? \u3002"})
	clock.advance(confirmationGrace - time.Millisecond)
	if !bytes.Equal(frameFrom(t, m), silenceFrame()) {
		t.Fatal("resumed before confirmation grace")
	}
	clock.advance(time.Millisecond)
	if frameFrom(t, m)[0] != 0x22 || m.current != old || old.ctx.Err() != nil {
		t.Fatal("did not resume original playback position")
	}
	resumed := false
	for _, e := range *events {
		if e.Reason == "unconfirmed" && e.Status == "speaking" {
			resumed = true
		}
	}
	if !resumed {
		t.Fatal("resume event missing")
	}
}

func TestPausePreservesFrameAfterFailedRTPWrite(t *testing.T) {
	m, clock, _ := playingFixture(t)
	_ = m.WriteFrame(func([]byte) error { return errors.New("temporary RTP failure") })
	m.ObserveVAD(interrupt.SpeechStarted, clock.now())
	_ = m.WriteFrame(func([]byte) error { return errors.New("silence write failure") })
	if m.current.metrics.SpeechToPauseMS != nil {
		t.Fatal("failed silence write counted as a pause")
	}
	clock.advance(maxSoftPause)
	if frameFrom(t, m)[0] != 0x11 {
		t.Fatal("pending frame lost during pause")
	}
}

func TestContinuousAndRepeatedNoiseHaveBoundedPause(t *testing.T) {
	m, clock, _ := playingFixture(t)
	m.ObserveVAD(interrupt.SpeechStarted, clock.now())
	clock.advance(2 * time.Second)
	m.ObserveVAD(interrupt.SpeechEnded, time.Time{})
	clock.advance(400 * time.Millisecond)
	m.ObserveVAD(interrupt.SpeechStarted, clock.now())
	clock.advance(400 * time.Millisecond)
	if !bytes.Equal(frameFrom(t, m), silenceFrame()) {
		t.Fatal("old quiet-period deadline resumed during new speech")
	}
	clock.advance(1200 * time.Millisecond)
	if frameFrom(t, m)[0] != 0x11 {
		t.Fatal("repeated noise extended maximum pause indefinitely")
	}
	m.ObserveVAD(interrupt.SpeechStarted, clock.now())
	if frameFrom(t, m)[0] != 0x22 {
		t.Fatal("duplicate VAD start re-paused continuous noise")
	}
	m.ObserveVAD(interrupt.SpeechEnded, time.Time{})
	m.ObserveVAD(interrupt.SpeechStarted, clock.now())
	if !bytes.Equal(frameFrom(t, m), silenceFrame()) {
		t.Fatal("next speech segment failed to pause")
	}
}

func TestConfirmedInterruptCannotResume(t *testing.T) {
	for _, kind := range []string{"asr_partial", "asr_final"} {
		t.Run(kind, func(t *testing.T) {
			m, clock, _ := playingFixture(t)
			old := m.current
			m.ObserveVAD(interrupt.SpeechStarted, clock.now())
			m.Accept(asr.Event{Event: "asr_partial", UtteranceID: "old", Text: "stale own transcript"})
			if m.current != old {
				t.Fatal("old transcript interrupted its own reply")
			}
			m.Accept(asr.Event{Event: kind, UtteranceID: "new", Text: "\u7b49\u4e00\u4e0b"})
			if old.ctx.Err() == nil || m.current != nil {
				t.Fatal("confirmed speech did not cancel old reply")
			}
			clock.advance(2 * maxSoftPause)
			m.ObserveVAD(interrupt.SpeechEnded, time.Time{})
			if !bytes.Equal(frameFrom(t, m), silenceFrame()) {
				t.Fatal("cancelled audio returned after timeout")
			}
		})
	}
}

func TestStopAndDisconnectDuringPause(t *testing.T) {
	for _, closeSession := range []bool{false, true} {
		m, clock, _ := playingFixture(t)
		old := m.current
		m.ObserveVAD(interrupt.SpeechStarted, clock.now())
		m.Stop(0)
		if m.current != old {
			t.Fatal("stale stop command cancelled current epoch")
		}
		if closeSession {
			m.Close()
		} else {
			m.Stop(1)
		}
		clock.advance(2 * maxSoftPause)
		if old.ctx.Err() == nil || !bytes.Equal(frameFrom(t, m), silenceFrame()) {
			t.Fatal("stop or disconnect leaked audio")
		}
	}
}

func TestASRFailureResumesAndDisablesSoftPause(t *testing.T) {
	m, clock, events := playingFixture(t)
	m.ObserveVAD(interrupt.SpeechStarted, clock.now())
	m.SetASRListening(false)
	if frameFrom(t, m)[0] != 0x11 {
		t.Fatal("ASR failure left reply paused")
	}
	m.ObserveVAD(interrupt.SpeechEnded, time.Time{})
	m.ObserveVAD(interrupt.SpeechStarted, clock.now())
	if frameFrom(t, m)[0] != 0x22 {
		t.Fatal("unavailable ASR allowed another soft pause")
	}
	found := false
	for _, e := range *events {
		if e.Reason == "asr_unavailable" {
			found = true
		}
	}
	if !found {
		t.Fatal("ASR failure recovery reason missing")
	}
}

func TestGenerationDuringPauseDoesNotResumePlayback(t *testing.T) {
	clock := &testClock{}
	started := make(chan struct{})
	release := make(chan struct{})
	m := New(modelFunc(func(ctx context.Context, _ []llm.Message, emit func(string) error) error {
		close(started)
		select {
		case <-release:
			return emit("Hello!")
		case <-ctx.Done():
			return ctx.Err()
		}
	}), speechFunc(func(context.Context, string) ([]byte, error) { return bytes.Repeat([]byte{0x33}, 160*300), nil }), func(Event) {})
	m.now = clock.now
	t.Cleanup(m.Close)
	m.SetASRListening(true)
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "a", Text: "question", SpeechEndAt: clock.now().Add(-600 * time.Millisecond)})
	<-started
	m.ObserveVAD(interrupt.SpeechStarted, clock.now())
	clock.advance(100 * time.Millisecond)
	close(release)
	waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return len(m.current.frames) == 250 })
	if !bytes.Equal(frameFrom(t, m), silenceFrame()) {
		t.Fatal("cloud completion resumed paused playback")
	}
	m.mu.Lock()
	if m.current.metrics.SpeechEndToAudioMS != nil || *m.current.metrics.SpeechEndToTextMS != 700 || *m.current.metrics.FinalToTextMS != 100 {
		t.Error("incorrect first-text or premature first-audio metric")
	}
	ctx := m.current.ctx
	m.mu.Unlock()
	m.Close()
	if ctx.Err() == nil {
		t.Fatal("paused queue producer survived disconnect")
	}
}

func TestAudioMetricExcludesSilenceAndMissingInputTime(t *testing.T) {
	m, clock, _ := playingFixture(t)
	m.current.speechEndAt = time.Time{}
	m.current.pending = silenceFrame()
	_ = frameFrom(t, m)
	if m.current.firstAudio {
		t.Fatal("leading silence counted as first audible frame")
	}
	clock.advance(50 * time.Millisecond)
	_ = frameFrom(t, m)
	if m.current.metrics.SpeechEndToAudioMS != nil || *m.current.metrics.FinalToAudioMS != 1050 {
		t.Fatal("missing input timestamp must not fabricate latency")
	}
}
