package dialogue

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"webrtc-interrupt/internal/asr"
	"webrtc-interrupt/internal/audio"
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
		frames: make(chan []byte, 250), space: make(chan struct{}, 1), stage: "speaking", playing: true, generated: true, text: "old answer",
		startedAt: clock.now().Add(-time.Second), speechEndAt: clock.now().Add(-2 * time.Second)}
	for i := 0; i < 200; i++ {
		m.current.frames <- bytes.Repeat([]byte{0x11}, 160)
	}
	m.rememberLocked("old")
	m.SetASRListening(true)
	t.Cleanup(m.Close)
	return m, clock, events
}

func frameFrom(t *testing.T, m *Manager) []byte {
	t.Helper()
	var out []byte
	if err := m.WriteFrame(func(b []byte) error { out = bytes.Clone(b); return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDuckingReducesServerAudioAndContinuesPlayback(t *testing.T) {
	m, clock, events := playingFixture(t)
	old := m.current
	original := frameFrom(t, m)
	m.ObserveVAD(interrupt.SpeechStarted, clock.now().Add(-200*time.Millisecond))
	clock.advance(20 * time.Millisecond)
	quiet := frameFrom(t, m)
	ratio := audio.RMS(audio.DecodePCMU(quiet)) / audio.RMS(audio.DecodePCMU(original))
	if ratio < 0.48 || ratio > 0.52 {
		t.Fatalf("duck gain=%f, must be audible at about 50%%", ratio)
	}
	if old.ctx.Err() != nil || len(old.frames) != 198 {
		t.Fatal("duck cancelled generation or paused queue consumption")
	}
	if old.metrics.SpeechToDuckMS == nil || *old.metrics.SpeechToDuckMS != 220 {
		t.Fatal("duck latency omitted VAD time")
	}
	m.ObserveVAD(interrupt.SpeechEnded, time.Time{})
	clock.advance(confirmationGrace)
	if !bytes.Equal(frameFrom(t, m), original) || m.current != old {
		t.Fatal("noise recovery failed to restore volume on the same turn")
	}
	if (*events)[len(*events)-1].Reason != "unconfirmed" {
		t.Fatal("recovery reason missing")
	}
}

func TestStablePartialCancelsBackendAndReservesTurnBeforeFinal(t *testing.T) {
	m, clock, events := playingFixture(t)
	old := m.current
	m.Accept(asr.Event{Event: "asr_partial", UtteranceID: "new", Text: "\u4fee\u6539"})
	if old.ctx.Err() != nil {
		t.Fatal("first partial caused hard cancellation")
	}
	frameFrom(t, m)
	clock.advance(partialStability)
	m.Accept(asr.Event{Event: "asr_partial", UtteranceID: "new", Text: "\u4fee\u6539\u8ba2\u5355"})
	if old.ctx.Err() == nil || len(old.frames) != 0 || old.pending != nil {
		t.Fatal("old context and queue not cleared")
	}
	if m.epoch != 2 || m.current == old || !m.current.waitingFinal || m.current.stage != "listening" {
		t.Fatal("new turn was not created before final")
	}
	if !bytes.Equal(frameFrom(t, m), silenceFrame()) {
		t.Fatal("old audio leaked while new turn waits for final")
	}
	transition := false
	for _, e := range *events {
		if e.Event == "turn_transition" && e.PreviousEpoch == 1 && e.Epoch == 2 {
			transition = true
		}
	}
	if !transition {
		t.Fatal("turn transition evidence missing")
	}
	clock.advance(2 * maxDuckDuration)
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "old", Text: "late old final"})
	if m.epoch != 2 || !m.current.waitingFinal {
		t.Fatal("retired final revived old turn")
	}
	if err := m.enqueueFrame(old, bytes.Repeat([]byte{0x33}, 160)); !errors.Is(err, context.Canceled) {
		t.Fatal("late TTS frame accepted")
	}
}

func TestFullClosedLoopStartsNewGenerationOnlyAfterFinal(t *testing.T) {
	m, clock, _ := playingFixture(t)
	started := make(chan []llm.Message, 1)
	m.model = modelFunc(func(_ context.Context, msg []llm.Message, emit func(string) error) error {
		started <- msg
		return emit("New answer!")
	})
	m.speech = speechFunc(func(context.Context, string) ([]byte, error) { return bytes.Repeat([]byte{0x44}, 320), nil })
	m.Accept(asr.Event{Event: "asr_partial", UtteranceID: "new", Text: "\u505c\u4e00\u4e0b"})
	if m.epoch != 1 {
		t.Fatal("explicit stop skipped audible duck stage")
	}
	frameFrom(t, m)
	clock.advance(minimumDuck)
	frameFrom(t, m)
	if m.epoch != 2 || !m.current.waitingFinal {
		t.Fatal("confirmation did not enter waiting turn")
	}
	select {
	case <-started:
		t.Fatal("LLM started before final")
	default:
	}
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "new", Text: "\u6362\u4e2a\u95ee\u9898"})
	select {
	case messages := <-started:
		if messages[len(messages)-1].Content != "\u6362\u4e2a\u95ee\u9898" {
			t.Fatal("wrong question")
		}
	case <-time.After(time.Second):
		t.Fatal("new LLM never started")
	}
	waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.current != nil && m.current.generated })
	if frameFrom(t, m)[0] != 0x44 {
		t.Fatal("new TTS did not reach downlink")
	}
	frameFrom(t, m)
	if m.epoch != 2 || m.current != nil {
		t.Fatal("final allocated an extra epoch or playback never completed")
	}
}

func TestFillersAndUnstablePartialsDoNotHardInterrupt(t *testing.T) {
	m, clock, _ := playingFixture(t)
	old := m.current
	m.ObserveVAD(interrupt.SpeechStarted, clock.now())
	for _, text := range []string{"\u55ef", "\u554a\u554a", "...", "\u54e6"} {
		m.Accept(asr.Event{Event: "asr_final", UtteranceID: "filler", Text: text})
	}
	m.Accept(asr.Event{Event: "asr_partial", UtteranceID: "new", Text: "\u4fee\u6539"})
	clock.advance(partialStability)
	m.Accept(asr.Event{Event: "asr_partial", UtteranceID: "new", Text: "\u660e\u5929"})
	if old.ctx.Err() != nil {
		t.Fatal("filler or unstable partial caused cancellation")
	}
	clock.advance(partialStability - time.Millisecond)
	m.Accept(asr.Event{Event: "asr_partial", UtteranceID: "new", Text: "\u660e\u5929\u53bb"})
	if old.ctx.Err() != nil {
		t.Fatal("partial confirmed before stability threshold")
	}
}

func TestFinalOnlyInterruptionStillDucksBeforeCancellation(t *testing.T) {
	m, clock, _ := playingFixture(t)
	old := m.current
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "new", Text: "new question"})
	if old.ctx.Err() != nil {
		t.Fatal("final skipped duck")
	}
	if !containsAudio(frameFrom(t, m)) {
		t.Fatal("duck was silence")
	}
	clock.advance(minimumDuck)
	frameFrom(t, m)
	if old.ctx.Err() == nil || m.epoch != 2 {
		t.Fatal("final did not confirm after duck")
	}
}

func TestNoiseWatchdogAndASRFailureRestoreGain(t *testing.T) {
	m, clock, _ := playingFixture(t)
	m.ObserveVAD(interrupt.SpeechStarted, clock.now())
	clock.advance(maxDuckDuration)
	if frameFrom(t, m)[0] != 0x11 {
		t.Fatal("continuous noise did not recover")
	}
	m.ObserveVAD(interrupt.SpeechStarted, clock.now())
	if m.current.duck != nil {
		t.Fatal("duplicate VAD start retriggered noise")
	}
	m.ObserveVAD(interrupt.SpeechEnded, time.Time{})
	m.ObserveVAD(interrupt.SpeechStarted, clock.now())
	m.SetASRListening(false)
	if frameFrom(t, m)[0] != 0x11 {
		t.Fatal("ASR failure did not restore volume")
	}
}

func TestManualStopAndCloseCannotResumeDuckedTurn(t *testing.T) {
	for _, closeSession := range []bool{true, false} {
		m, clock, _ := playingFixture(t)
		old := m.current
		m.ObserveVAD(interrupt.SpeechStarted, clock.now())
		m.Stop(0)
		if old.ctx.Err() != nil {
			t.Fatal("stale stop affected current turn")
		}
		if closeSession {
			m.Close()
		} else {
			m.Stop(1)
		}
		clock.advance(2 * maxDuckDuration)
		if old.ctx.Err() == nil || !bytes.Equal(frameFrom(t, m), silenceFrame()) {
			t.Fatal("stopped turn revived")
		}
	}
}

func TestFailedDuckedWriteRetainsOriginalPendingFrame(t *testing.T) {
	m, clock, _ := playingFixture(t)
	m.ObserveVAD(interrupt.SpeechStarted, clock.now())
	_ = m.WriteFrame(func([]byte) error { return errors.New("RTP write failed") })
	if m.current.metrics.SpeechToDuckMS != nil {
		t.Fatal("failed write counted as duck")
	}
	clock.advance(maxDuckDuration)
	if frameFrom(t, m)[0] != 0x11 {
		t.Fatal("retry lost or permanently attenuated pending frame")
	}
}
