package dialogue

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"webrtc-interrupt/internal/asr"
	"webrtc-interrupt/internal/llm"
)

type modelFunc func(context.Context, []llm.Message, func(string) error) error

func (f modelFunc) Stream(ctx context.Context, m []llm.Message, e func(string) error) error {
	return f(ctx, m, e)
}

func (f modelFunc) ClassifyInterruption(context.Context, llm.InterruptionInput) (bool, error) {
	return true, nil
}

type speechFunc func(context.Context, string) ([]byte, error)

type streamSpeechFunc func(context.Context, string, func([]byte) error) error

func (f streamSpeechFunc) Stream(ctx context.Context, text string, emit func([]byte) error) error {
	return f(ctx, text, emit)
}

func (f speechFunc) Stream(ctx context.Context, s string, emit func([]byte) error) error {
	data, err := f(ctx, s)
	if err != nil {
		return err
	}
	return emit(data)
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestStreamingSpeechAndPadding(t *testing.T) {
	var mu sync.Mutex
	var events []Event
	m := New(modelFunc(func(_ context.Context, _ []llm.Message, emit func(string) error) error { return emit("Hello!") }), speechFunc(func(context.Context, string) ([]byte, error) { return bytes.Repeat([]byte{0x33}, 161), nil }), func(e Event) { mu.Lock(); events = append(events, e); mu.Unlock() })
	defer m.Close()
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "a", Text: "question"})
	waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.current != nil && m.current.generated })
	var frames [][]byte
	for i := 0; i < 3; i++ {
		if err := m.WriteFrame(func(b []byte) error { frames = append(frames, bytes.Clone(b)); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if len(frames[0]) != 160 || frames[0][159] != 0x33 || frames[1][0] != 0x33 || frames[1][1] != 0xff || !bytes.Equal(frames[2], silenceFrame()) {
		t.Fatal("wrong frame order or silence padding")
	}
	mu.Lock()
	defer mu.Unlock()
	if events[len(events)-1].Status != "completed" {
		t.Fatal("playback completion missing")
	}
	if len(m.history) != 2 || m.history[1].Content != "Hello!" {
		t.Fatal("completed answer not saved in history")
	}
}

func TestInterruptDropsQueuedAndLateAudio(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	m := New(modelFunc(func(_ context.Context, _ []llm.Message, emit func(string) error) error { return emit("old!") }), speechFunc(func(ctx context.Context, _ string) ([]byte, error) {
		close(started)
		<-release
		defer close(finished)
		return bytes.Repeat([]byte{0x33}, 640), nil
	}), func(Event) {})
	defer m.Close()
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "a", Text: "first"})
	<-started
	m.Accept(asr.Event{Event: "asr_partial", UtteranceID: "b", Text: "\u505c\u4e00\u4e0b"})
	waitEpoch(t, m, 2)
	close(release)
	<-finished
	for i := 0; i < 3; i++ {
		_ = m.WriteFrame(func(b []byte) error {
			if !bytes.Equal(b, silenceFrame()) {
				t.Error("stale audio played")
			}
			return nil
		})
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || !m.current.waitingFinal || len(m.history) != 1 {
		t.Fatal("interrupted answer retained")
	}
}

func TestNewEpochDeduplicatesAndCancelsModel(t *testing.T) {
	started := make(chan context.Context, 2)
	m := New(modelFunc(func(ctx context.Context, _ []llm.Message, emit func(string) error) error {
		started <- ctx
		<-ctx.Done()
		return emit("stale")
	}), speechFunc(func(context.Context, string) ([]byte, error) { return nil, errors.New("must not synthesize") }), func(Event) {})
	defer m.Close()
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "a", Text: "first"})
	old := <-started
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "a", Text: "duplicate"})
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "b", Text: "second"})
	current := <-started
	if old.Err() == nil || current.Err() != nil {
		t.Fatal("epoch replacement did not cancel only old model")
	}
	m.Stop(2)
	if current.Err() == nil {
		t.Fatal("manual stop did not cancel request")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.epoch != 2 {
		t.Fatal("duplicate final started another response")
	}
}

func TestSynthesisFailureCancelsLanguageModel(t *testing.T) {
	cancelled := make(chan struct{})
	m := New(modelFunc(func(ctx context.Context, _ []llm.Message, emit func(string) error) error {
		if err := emit("Hello!"); err != nil {
			return err
		}
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}), speechFunc(func(context.Context, string) ([]byte, error) { return nil, errors.New("fixture TTS failure") }), func(Event) {})
	defer m.Close()
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "a", Text: "question"})
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("TTS failure left LLM active")
	}
	waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.current == nil })
}

func TestSegmenterLimitsAndFlush(t *testing.T) {
	input := strings.Repeat("\u4f60", 207) + "\u3002Hello there"
	var splitter segmenter
	var pieces []string
	emit := func(s string) error {
		pieces = append(pieces, s)
		if len([]rune(s)) > 100 {
			t.Fatal("oversized TTS segment")
		}
		return nil
	}
	for _, r := range input {
		if err := splitter.push(string(r), emit); err != nil {
			t.Fatal(err)
		}
	}
	_ = splitter.flush(emit)
	if strings.Join(pieces, "") != input || len(pieces) != 4 {
		t.Fatal("segmentation dropped or duplicated characters")
	}
}

func TestBoundedAudioQueueUnblocksOnClose(t *testing.T) {
	m := New(modelFunc(func(_ context.Context, _ []llm.Message, emit func(string) error) error { return emit("Hello!") }), speechFunc(func(context.Context, string) ([]byte, error) { return bytes.Repeat([]byte{0x33}, 160*500), nil }), func(Event) {})
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "a", Text: "question"})
	waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return len(m.current.frames) == 250 })
	m.mu.Lock()
	ctx := m.current.ctx
	m.mu.Unlock()
	m.Close()
	if ctx.Err() == nil {
		t.Fatal("blocked producer was not cancelled")
	}
}

func TestQueuedAudioDiscardedOnNewFinal(t *testing.T) {
	m := New(modelFunc(func(_ context.Context, messages []llm.Message, emit func(string) error) error {
		return emit(messages[len(messages)-1].Content + "!")
	}), speechFunc(func(_ context.Context, text string) ([]byte, error) {
		value := byte(0x33)
		if text == "second!" {
			value = 0x44
		}
		return bytes.Repeat([]byte{value}, 320), nil
	}), func(Event) {})
	defer m.Close()
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "a", Text: "first"})
	waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.current.generated })
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "b", Text: "second"})
	waitEpoch(t, m, 2)
	waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.current.generated })
	for i := 0; i < 2; i++ {
		_ = m.WriteFrame(func(b []byte) error {
			if !bytes.Equal(b, bytes.Repeat([]byte{0x44}, 160)) {
				t.Error("replaced epoch leaked queued audio")
			}
			return nil
		})
	}
}

func TestStreamingTTSChunksDoNotInsertSilenceBetweenPackets(t *testing.T) {
	payload := bytes.Repeat([]byte{0x33}, 321)
	m := New(modelFunc(func(_ context.Context, _ []llm.Message, emit func(string) error) error { return emit("Hello!") }), streamSpeechFunc(func(_ context.Context, _ string, emit func([]byte) error) error {
		for _, chunk := range [][]byte{payload[:13], payload[13:224], payload[224:]} {
			if err := emit(chunk); err != nil {
				return err
			}
		}
		return nil
	}), func(Event) {})
	defer m.Close()
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "a", Text: "question"})
	waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.current.generated })
	var got []byte
	for i := 0; i < 3; i++ {
		got = append(got, frameFrom(t, m)...)
	}
	if !bytes.Equal(got[:321], payload) || !bytes.Equal(got[321:], bytes.Repeat([]byte{0xff}, 159)) {
		t.Fatal("TTS chunk boundaries introduced silence or lost samples")
	}
}

func TestConfirmedInterruptionCancelsActiveLLMAndStreamingTTS(t *testing.T) {
	clock := &testClock{}
	llmStopped := make(chan struct{})
	ttsStopped := make(chan struct{})
	m := New(modelFunc(func(ctx context.Context, _ []llm.Message, emit func(string) error) error {
		if err := emit("Hello!"); err != nil {
			return err
		}
		<-ctx.Done()
		close(llmStopped)
		return ctx.Err()
	}), streamSpeechFunc(func(ctx context.Context, _ string, emit func([]byte) error) error {
		if err := emit(bytes.Repeat([]byte{0x33}, 160*10)); err != nil {
			return err
		}
		<-ctx.Done()
		close(ttsStopped)
		return emit(bytes.Repeat([]byte{0x44}, 160))
	}), func(Event) {})
	m.now = clock.now
	defer m.Close()
	m.SetASRListening(true)
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "a", Text: "question"})
	waitFor(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.current.frames) == 10 && m.current.llmActive && m.current.ttsActive
	})
	frameFrom(t, m)
	m.Accept(asr.Event{Event: "asr_partial", UtteranceID: "b", Text: "\u505c\u4e00\u4e0b"})
	waitConfirmed(t, m)
	frameFrom(t, m)
	clock.advance(minimumDuck)
	frameFrom(t, m)
	for _, done := range []chan struct{}{llmStopped, ttsStopped} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("old cloud task not cancelled")
		}
	}
	if !bytes.Equal(frameFrom(t, m), silenceFrame()) || m.epoch != 2 || !m.current.waitingFinal {
		t.Fatal("old stream leaked while new turn waits")
	}
}
