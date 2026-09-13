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
	"webrtc-interrupt/internal/audio"
	"webrtc-interrupt/internal/home"
	"webrtc-interrupt/internal/llm"
)

type LanguageModel interface {
	Stream(context.Context, []llm.Message, func(string) error) error
	ClassifyInterruption(context.Context, llm.InterruptionInput) (bool, error)
	ClassifyAction(context.Context, llm.ActionInput) (home.Call, error)
}

type SpeechSynthesizer interface {
	Stream(context.Context, string, func([]byte) error) error
}

type Event struct {
	Event         string           `json:"event"`
	Epoch         uint64           `json:"response_epoch"`
	Status        string           `json:"status,omitempty"`
	Text          string           `json:"text,omitempty"`
	Detail        string           `json:"detail,omitempty"`
	Reason        string           `json:"reason,omitempty"`
	Metrics       *Metrics         `json:"metrics,omitempty"`
	PreviousEpoch uint64           `json:"previous_epoch,omitempty"`
	UtteranceID   string           `json:"utterance_id,omitempty"`
	QueueDropped  int              `json:"queue_dropped,omitempty"`
	LLMActive     bool             `json:"llm_active,omitempty"`
	TTSActive     bool             `json:"tts_active,omitempty"`
	Interrupt     *bool            `json:"interrupt,omitempty"`
	Continuation  *bool            `json:"continuation,omitempty"`
	Fallback      bool             `json:"fallback,omitempty"`
	LatencyMS     *int64           `json:"latency_ms,omitempty"`
	QueueSize     *int             `json:"queue_size,omitempty"`
	ToolCall      *home.Call       `json:"tool_call,omitempty"`
	PlanID        string           `json:"plan_id,omitempty"`
	StepIndex     int              `json:"step_index,omitempty"`
	StepCount     int              `json:"step_count,omitempty"`
	PlanSteps     []PlanStepStatus `json:"steps,omitempty"`
	SourceIDs     []string         `json:"source_utterance_ids,omitempty"`
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
	duck                  *duckState
	waitingFinal          bool
	llmActive, ttsActive  bool
	space                 chan struct{}
	startedAt             time.Time
	speechEndAt           time.Time
	firstText, firstAudio bool
	metrics               Metrics
	toolCall              *home.Call
	plan                  *taskPlan
	stepText              string
	input                 asr.Event
}

// Manager serializes response events and audio writes. Each epoch owns its
// queues; replacing an epoch makes late cloud results unreachable by playback.
type Manager struct {
	mu            sync.Mutex
	model         LanguageModel
	speech        SpeechSynthesizer
	executor      home.Executor
	emit          func(Event)
	epoch         uint64
	current       *turn
	closed        bool
	seen          map[string]bool
	seenOrder     []string
	history       []llm.Message
	executions    []executionResult
	now           func() time.Time
	inSpeech      bool
	asrListening  bool
	interjections []*interjection
	plan          *taskPlan
	merge         *mergeGroup
	joining       *queuedJoin
}

func New(model LanguageModel, speech SpeechSynthesizer, emit func(Event)) *Manager {
	return NewWithExecutor(model, speech, home.VoiceExecutor{}, emit)
}

func NewWithExecutor(model LanguageModel, speech SpeechSynthesizer, executor home.Executor, emit func(Event)) *Manager {
	return &Manager{model: model, speech: speech, executor: executor, emit: emit, seen: make(map[string]bool), now: time.Now}
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
	if event.Event != "asr_partial" && event.Event != "asr_final" {
		return
	}
	if event.Event == "asr_final" {
		event.FinalReceivedAt = m.now()
	}
	m.acceptLocked(event)
}

func (m *Manager) acceptLocked(event asr.Event) {
	t := m.current
	if t != nil && t.utteranceID == event.UtteranceID {
		if t.waitingFinal && event.Event == "asr_final" {
			m.startFinalLocked(t, event)
		}
		return
	}
	m.acceptInterjectionLocked(event)
}

func (m *Manager) rememberLocked(id string) {
	if m.seen[id] {
		return
	}
	m.seen[id] = true
	m.seenOrder = append(m.seenOrder, id)
	if len(m.seenOrder) > 100 {
		delete(m.seen, m.seenOrder[0])
		m.seenOrder = m.seenOrder[1:]
	}
}

func (m *Manager) reserveLocked(utteranceID string) *turn {
	m.epoch++
	deadline, timeoutCancel := context.WithTimeout(context.Background(), 90*time.Second)
	ctx, cancel := context.WithCancelCause(deadline)
	t := &turn{epoch: m.epoch, utteranceID: utteranceID, ctx: ctx, fail: cancel,
		cancel: func() { cancel(context.Canceled); timeoutCancel() }, frames: make(chan []byte, 250), space: make(chan struct{}, 1), waitingFinal: true}
	m.current = t
	m.progressLocked(t, "listening")
	return t
}

func (m *Manager) startFinalLocked(t *turn, event asr.Event) {
	t.input = event
	m.rememberLocked(event.UtteranceID)
	if state := m.readyStatus(); state != "ready" {
		m.statusLocked(t, state, "")
		t.cancel()
		m.current = nil
		return
	}
	t.waitingFinal = false
	t.startedAt = event.FinalReceivedAt
	if t.startedAt.IsZero() {
		t.startedAt = m.now()
	}
	t.speechEndAt = event.SpeechEndAt
	text := []rune(event.Text)
	if len(text) > 2000 {
		text = text[:2000]
	}
	m.history = append(m.history, llm.Message{Role: "user", Content: string(text)})
	if len(m.history) > 12 {
		m.history = m.history[len(m.history)-12:]
	}
	messages := append([]llm.Message{{Role: "system", Content: home.RoleSkill}}, m.history...)
	m.progressLocked(t, "thinking")
	go m.generate(t, messages)
}

func (m *Manager) Stop(epoch uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.epoch == epoch {
		m.clearMergeLocked("manual_stop")
		m.clearInterjectionsLocked()
		m.stopLocked("interrupted")
	}
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	m.clearMergeLocked("closed")
	m.cancelPlanLocked("cancelled")
	m.clearInterjectionsLocked()
	if m.current != nil {
		m.toolStatusLocked(m.current, "cancelled")
		m.current.cancel()
		m.current = nil
	}
}

func (m *Manager) stopLocked(status string) {
	if m.current == nil {
		return
	}
	t := m.current
	m.cancelPlanLocked("cancelled")
	m.recordExecutionLocked(t, "cancelled")
	m.cancelIntentRequestsLocked(t.epoch)
	t.cancel()
	m.rememberLocked(t.utteranceID)
	dropped := len(t.frames)
	if t.pending != nil {
		dropped++
		t.pending = nil
	}
	for len(t.frames) > 0 {
		<-t.frames
	}
	log.Printf("response epoch=%d cancel llm_active=%t tts_active=%t queue_dropped=%d", t.epoch, t.llmActive, t.ttsActive, dropped)
	m.emit(Event{Event: "response_cancelled", Epoch: t.epoch, QueueDropped: dropped, LLMActive: t.llmActive, TTSActive: t.ttsActive})
	m.toolStatusLocked(t, "cancelled")
	m.statusLocked(t, status, "")
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
	m.cancelIntentRequestsLocked(t.epoch)
	m.cancelPlanLocked("failed")
	m.recordExecutionLocked(t, "failed")
	t.cancel()
	log.Printf("response epoch=%d failed: %v", t.epoch, err)
	m.toolStatusLocked(t, "failed")
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
	m.mu.Lock()
	if m.current != t || t.ctx.Err() != nil {
		m.mu.Unlock()
		close(segments)
		<-speechDone
		return
	}
	t.llmActive = true
	m.mu.Unlock()
	err := m.respond(t, messages, func(delta string) error {
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
	m.mu.Lock()
	t.llmActive = false
	m.mu.Unlock()
	log.Printf("response epoch=%d text_production_finished cancelled=%t", t.epoch, t.ctx.Err() != nil)
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
		m.failLocked(t, errors.New("response produced no speech text"))
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
			if m.current != t || t.ctx.Err() != nil {
				m.mu.Unlock()
				return context.Canceled
			}
			t.ttsActive = true
			if m.current == t && !t.playing {
				m.progressLocked(t, "synthesizing")
			}
			m.mu.Unlock()
			var pending []byte
			received := false
			err := m.speech.Stream(t.ctx, text, func(chunk []byte) error {
				if len(chunk) > 0 {
					received = true
				}
				pending = append(pending, chunk...)
				for len(pending) >= 160 {
					if err := m.enqueueFrame(t, append([]byte(nil), pending[:160]...)); err != nil {
						return err
					}
					pending = pending[160:]
				}
				return nil
			})
			m.mu.Lock()
			t.ttsActive = false
			m.mu.Unlock()
			log.Printf("response epoch=%d tts_finished cancelled=%t", t.epoch, t.ctx.Err() != nil)
			if err != nil {
				return err
			}
			if !received {
				return errors.New("Tencent TTS returned empty audio")
			}
			if len(pending) > 0 {
				frame := silenceFrame()
				copy(frame, pending)
				if err := m.enqueueFrame(t, frame); err != nil {
					return err
				}
			}
		}
	}
}

func (m *Manager) enqueueFrame(t *turn, frame []byte) error {
	for {
		m.mu.Lock()
		if m.current != t || t.ctx.Err() != nil {
			m.mu.Unlock()
			return context.Canceled
		}
		select {
		case t.frames <- frame:
			m.mu.Unlock()
			return nil
		default:
			m.mu.Unlock()
		}
		select {
		case <-t.ctx.Done():
			return context.Cause(t.ctx)
		case <-t.space:
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
	m.pumpMergeLocked()
	m.pumpInterjectionsLocked()
	t := m.current
	if t != nil && t.ctx.Err() != nil {
		m.failLocked(t, context.Cause(t.ctx))
		t = nil
	}
	if t != nil && t.duck != nil {
		d := t.duck
		now := m.now()
		if d.confirmed && !now.Before(d.minimumUntil) {
			m.confirmLocked(t)
			t = m.current
		} else if !d.confirmed && (!now.Before(d.deadline) || (!d.resumeAt.IsZero() && !now.Before(d.resumeAt))) {
			m.resumeLocked(t, "unconfirmed")
		}
	}
	if t != nil && t.pending == nil {
		select {
		case t.pending = <-t.frames:
			select {
			case t.space <- struct{}{}:
			default:
			}
		default:
		}
	}
	if t == nil || t.pending == nil {
		if t != nil && t.generated && (t.duck == nil || !t.duck.confirmed) {
			m.completeLocked(t)
		}
		return write(silenceFrame())
	}
	frame := t.pending
	if t.duck != nil {
		samples := audio.DecodePCMU(frame)
		for i := range samples {
			samples[i] = int16(float64(samples[i]) * duckGain)
		}
		frame = audio.EncodePCMU(samples)
	}
	if err := write(frame); err != nil {
		return err
	}
	if t.duck != nil && !t.duck.measured && containsAudio(frame) {
		t.duck.measured = true
		t.metrics.SpeechToDuckMS = elapsedMS(t.duck.onset, m.now())
		m.metricsLocked(t)
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
	if t.generated && len(t.frames) == 0 && (t.duck == nil || !t.duck.confirmed) {
		m.completeLocked(t)
	}
	return nil
}

func (m *Manager) completeLocked(t *turn) {
	m.recordExecutionLocked(t, "completed")
	m.history = append(m.history, llm.Message{Role: "assistant", Content: t.text})
	m.toolStatusLocked(t, "completed")
	m.statusLocked(t, "completed", "")
	m.cancelIntentRequestsLocked(t.epoch)
	t.cancel()
	m.current = nil
	if m.advancePlanLocked(t) {
		return
	}
	m.drainInterjectionsLocked()
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
