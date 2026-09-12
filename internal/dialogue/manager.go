package dialogue

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"webrtc-interrupt/internal/asr"
	"webrtc-interrupt/internal/llm"
)

type LanguageModel interface {
	Stream(context.Context, []llm.Message, func(string) error) error
}

type SpeechSynthesizer interface {
	Synthesize(context.Context, string) ([]byte, error)
}

type Event struct {
	Event   string   `json:"event"`
	Epoch   uint64   `json:"response_epoch"`
	Status  string   `json:"status,omitempty"`
	Text    string   `json:"text,omitempty"`
	Detail  string   `json:"detail,omitempty"`
	Reason  string   `json:"reason,omitempty"`
	Metrics *Metrics `json:"metrics,omitempty"`
}

type turn struct {
	epoch                 uint64
	utteranceID           string
	ctx                   context.Context
	cancel                func()
	fail                  context.CancelCauseFunc
	frames                chan []byte
	pending               []byte
	text                  string
	generated, playing    bool
	stage                 string
	pause                 *softPause
	startedAt             time.Time
	speechEndAt           time.Time
	firstText, firstAudio bool
	metrics               Metrics
}

// Manager serializes response events and audio writes. Each epoch owns its
// queues; replacing an epoch makes late cloud results unreachable by playback.
type Manager struct {
	mu           sync.Mutex
	model        LanguageModel
	speech       SpeechSynthesizer
	emit         func(Event)
	epoch        uint64
	current      *turn
	closed       bool
	seen         map[string]bool
	seenOrder    []string
	history      []llm.Message
	now          func() time.Time
	inSpeech     bool
	asrListening bool
}

func New(model LanguageModel, speech SpeechSynthesizer, emit func(Event)) *Manager {
	return &Manager{model: model, speech: speech, emit: emit, seen: make(map[string]bool), now: time.Now}
}

func (m *Manager) Ready() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		m.statusLocked(nil, m.readyStatus(), "")
	}
}

func (m *Manager) readyStatus() string {
	if m.model == nil {
		return "llm_unconfigured"
	}
	if m.speech == nil {
		return "tts_unconfigured"
	}
	return "ready"
}

func (m *Manager) Accept(event asr.Event) {
	if event.UtteranceID == "" || !hasSpeechText(event.Text) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.seen[event.UtteranceID] {
		return
	}
	if event.Event == "asr_partial" {
		if m.current != nil && m.current.utteranceID != event.UtteranceID {
			m.stopLocked("interrupted")
		}
		return
	}
	if event.Event != "asr_final" {
		return
	}
	m.seen[event.UtteranceID] = true
	m.seenOrder = append(m.seenOrder, event.UtteranceID)
	if len(m.seenOrder) > 100 {
		delete(m.seen, m.seenOrder[0])
		m.seenOrder = m.seenOrder[1:]
	}
	m.stopLocked("interrupted")
	if state := m.readyStatus(); state != "ready" {
		m.statusLocked(nil, state, "")
		return
	}
	m.epoch++
	deadline, timeoutCancel := context.WithTimeout(context.Background(), 90*time.Second)
	ctx, cancel := context.WithCancelCause(deadline)
	t := &turn{epoch: m.epoch, utteranceID: event.UtteranceID, ctx: ctx, fail: cancel,
		cancel: func() { cancel(context.Canceled); timeoutCancel() }, frames: make(chan []byte, 250), startedAt: m.now(), speechEndAt: event.SpeechEndAt}
	m.current = t
	text := []rune(event.Text)
	if len(text) > 2000 {
		text = text[:2000]
	}
	m.history = append(m.history, llm.Message{Role: "user", Content: string(text)})
	if len(m.history) > 12 {
		m.history = m.history[len(m.history)-12:]
	}
	messages := append([]llm.Message{{Role: "system", Content: "You are a Chinese voice assistant. Reply naturally in Chinese using one to three short spoken sentences, under 100 Chinese characters. Do not use Markdown, lists, or reasoning narration. Answer the user's latest question using the conversation context."}}, m.history...)
	m.progressLocked(t, "thinking")
	go m.generate(t, messages)
}

func (m *Manager) Stop(epoch uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil && m.current.epoch == epoch {
		m.stopLocked("interrupted")
	}
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	if m.current != nil {
		m.current.cancel()
		m.current = nil
	}
}

func (m *Manager) stopLocked(status string) {
	if m.current == nil {
		return
	}
	m.current.cancel()
	m.statusLocked(m.current, status, "")
	m.current = nil
}

func (m *Manager) statusLocked(t *turn, status, detail string) {
	epoch := m.epoch
	if t != nil {
		epoch = t.epoch
	}
	log.Printf("response epoch=%d status=%s", epoch, status)
	m.emit(Event{Event: "response_status", Epoch: epoch, Status: status, Detail: detail})
}

func (m *Manager) failLocked(t *turn, err error) {
	if m.current != t || m.closed {
		return
	}
	t.cancel()
	log.Printf("response epoch=%d failed: %v", t.epoch, err)
	m.statusLocked(t, "failed", err.Error())
	m.current = nil
}

func (m *Manager) generate(t *turn, messages []llm.Message) {
	segments := make(chan string, 8)
	speechDone := make(chan error, 1)
	go func() {
		err := m.synthesize(t, segments)
		if err != nil {
			t.fail(err)
		}
		speechDone <- err
	}()
	send := func(text string) error {
		select {
		case segments <- text:
			return nil
		case <-t.ctx.Done():
			return context.Cause(t.ctx)
		}
	}
	var splitter segmenter
	err := m.model.Stream(t.ctx, messages, func(delta string) error {
		m.mu.Lock()
		if m.current != t || t.ctx.Err() != nil {
			m.mu.Unlock()
			return context.Canceled
		}
		if utf8.RuneCountInString(t.text)+utf8.RuneCountInString(delta) > 1200 {
			m.mu.Unlock()
			return errors.New("reply exceeds speech length limit")
		}
		if !t.firstText && strings.TrimSpace(delta) != "" {
			t.firstText = true
			t.metrics.FinalToTextMS = elapsedMS(t.startedAt, m.now())
			t.metrics.SpeechEndToTextMS = elapsedMS(t.speechEndAt, m.now())
			m.metricsLocked(t)
		}
		t.text += delta
		m.emit(Event{Event: "response_text", Epoch: t.epoch, Text: t.text})
		m.mu.Unlock()
		return splitter.push(delta, send)
	})
	if err == nil {
		err = splitter.flush(send)
	}
	if err != nil {
		t.fail(err)
	}
	close(segments)
	speechErr := <-speechDone
	if err == nil {
		err = speechErr
	}
	if cause := context.Cause(t.ctx); cause != nil {
		err = cause
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != t || m.closed {
		return
	}
	if err != nil {
		m.failLocked(t, err)
		return
	}
	if strings.TrimSpace(t.text) == "" {
		m.failLocked(t, errors.New("DeepSeek returned an empty reply"))
		return
	}
	t.generated = true
}

func (m *Manager) synthesize(t *turn, segments <-chan string) error {
	for {
		select {
		case <-t.ctx.Done():
			return context.Cause(t.ctx)
		case text, open := <-segments:
			if !open {
				return nil
			}
			if t.ctx.Err() != nil {
				return context.Cause(t.ctx)
			}
			m.mu.Lock()
			if m.current == t && !t.playing {
				m.progressLocked(t, "synthesizing")
			}
			m.mu.Unlock()
			encoded, err := m.speech.Synthesize(t.ctx, text)
			if err != nil {
				return err
			}
			if len(encoded) == 0 {
				return errors.New("Tencent TTS returned empty audio")
			}
			for offset := 0; offset < len(encoded); offset += 160 {
				frame := silenceFrame()
				copy(frame, encoded[offset:min(offset+160, len(encoded))])
				select {
				case t.frames <- frame:
				case <-t.ctx.Done():
					return context.Cause(t.ctx)
				}
			}
		}
	}
}

func silenceFrame() []byte {
	frame := make([]byte, 160)
	for i := range frame {
		frame[i] = 0xff
	}
	return frame
}

// WriteFrame is called on the existing 20ms RTP clock. Holding mu through the
// write prevents a cancelled epoch from sending after its stop event.
func (m *Manager) WriteFrame(write func([]byte) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.current
	if t != nil && t.ctx.Err() != nil {
		m.failLocked(t, context.Cause(t.ctx))
		t = nil
	}
	if t != nil && t.pause != nil {
		now := m.now()
		if !now.Before(t.pause.deadline) || (!t.pause.resumeAt.IsZero() && !now.Before(t.pause.resumeAt)) {
			m.resumeLocked(t, "unconfirmed")
		} else {
			if err := write(silenceFrame()); err != nil {
				return err
			}
			if !t.pause.measured {
				t.pause.measured = true
				t.metrics.SpeechToPauseMS = elapsedMS(t.pause.onset, m.now())
				m.metricsLocked(t)
			}
			return nil
		}
	}
	if t != nil && t.pending == nil {
		select {
		case t.pending = <-t.frames:
		default:
		}
	}
	if t == nil || t.pending == nil {
		if t != nil && t.generated {
			m.completeLocked(t)
		}
		return write(silenceFrame())
	}
	if err := write(t.pending); err != nil {
		return err
	}
	if !t.firstAudio && containsAudio(t.pending) {
		t.firstAudio = true
		t.metrics.FinalToAudioMS = elapsedMS(t.startedAt, m.now())
		t.metrics.SpeechEndToAudioMS = elapsedMS(t.speechEndAt, m.now())
		m.metricsLocked(t)
	}
	t.pending = nil
	if !t.playing {
		t.playing = true
		m.progressLocked(t, "speaking")
	}
	if t.generated && len(t.frames) == 0 {
		m.completeLocked(t)
	}
	return nil
}

func (m *Manager) completeLocked(t *turn) {
	m.history = append(m.history, llm.Message{Role: "assistant", Content: t.text})
	m.statusLocked(t, "completed", "")
	t.cancel()
	m.current = nil
}

func hasSpeechText(text string) bool {
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

func containsAudio(frame []byte) bool {
	for _, b := range frame {
		if b != 0xff && b != 0x7f {
			return true
		}
	}
	return false
}
