package dialogue

import (
	"log"
	"strings"
	"time"
	"unicode"

	"webrtc-interrupt/internal/asr"
	"webrtc-interrupt/internal/interrupt"
)

const (
	confirmationGrace = 800 * time.Millisecond
	maxDuckDuration   = 4 * time.Second
	minimumDuck       = 120 * time.Millisecond
	partialStability  = 200 * time.Millisecond
	duckGain          = 0.5
)

type duckState struct {
	onset, deadline, resumeAt, minimumUntil time.Time
	utteranceID                             string
	confirmed, measured                     bool
	final                                   *asr.Event
}

// ObserveVAD changes playback only. ASR continues receiving every input frame.
// onset estimates the first voiced frame before VAD's hysteresis threshold.
func (m *Manager) ObserveVAD(event interrupt.Event, onset time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	t := m.current
	switch event {
	case interrupt.SpeechStarted:
		if m.inSpeech {
			return
		}
		m.inSpeech = true
		if t == nil || t.waitingFinal || !m.asrListening || t.ctx.Err() != nil {
			return
		}
		if t.duck != nil {
			t.duck.resumeAt = time.Time{}
			return
		}
		if onset.IsZero() || onset.After(m.now()) {
			onset = m.now()
		}
		m.beginDuckLocked(t, onset)
	case interrupt.SpeechEnded:
		if !m.inSpeech {
			return
		}
		m.inSpeech = false
		if t != nil && t.duck != nil {
			t.duck.resumeAt = m.now().Add(confirmationGrace)
		}
	}
}

func (m *Manager) SetASRListening(listening bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.asrListening = listening
	if !listening {
		// A broken stream may have lost a continuation after its last final.
		m.clearMergeLocked("asr_stopped")
		m.dropUnfinishedInterjectionsLocked()
	}
	if !listening && m.current != nil && m.current.duck != nil {
		if m.current.duck.confirmed {
			m.confirmLocked(m.current)
		} else {
			m.resumeLocked(m.current, "asr_unavailable")
		}
	}
}

func (m *Manager) progressLocked(t *turn, status string) {
	t.stage = status
	if t.duck == nil {
		m.statusLocked(t, status, "")
	}
}

func (m *Manager) resumeLocked(t *turn, reason string) {
	t.duck = nil
	log.Printf("response epoch=%d status=%s reason=%s", t.epoch, t.stage, reason)
	m.emit(Event{Event: "response_status", Epoch: t.epoch, Status: t.stage, Reason: reason})
}

func (m *Manager) beginDuckLocked(t *turn, onset time.Time) {
	if t.duck != nil {
		return
	}
	now := m.now()
	t.duck = &duckState{onset: onset, deadline: now.Add(maxDuckDuration), minimumUntil: now.Add(minimumDuck)}
	if !m.inSpeech {
		t.duck.resumeAt = now.Add(confirmationGrace)
	}
	t.metrics.SpeechToDuckMS = nil
	log.Printf("response epoch=%d duck gain=%.2f", t.epoch, duckGain)
	m.statusLocked(t, "ducking", "")
}

func speechText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, text)
}

func explicitStop(text string) bool {
	for _, phrase := range []string{"\u7b49\u4e00\u4e0b", "\u505c\u4e00\u4e0b", "\u505c\u6b62", "\u522b\u8bf4\u4e86", "\u6362\u4e2a\u95ee\u9898", "\u7b49\u7b49"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return text == "\u505c"
}

func commonPrefix(a, b string) int {
	x, y := []rune(a), []rune(b)
	n := 0
	for n < len(x) && n < len(y) && x[n] == y[n] {
		n++
	}
	return n
}

func (m *Manager) confirmLocked(t *turn) {
	d := t.duck
	oldEpoch := t.epoch
	// Preserve the triggering utterance so its final can complete the new turn.
	m.removeInterjectionLocked(d.utteranceID)
	m.clearMergeLocked("interrupted")
	droppedInputs := len(m.interjections)
	m.clearInterjectionsLocked()
	log.Printf("input_queue epoch=%d cleared=true reason=interrupted inputs_dropped=%d", oldEpoch, droppedInputs)
	m.stopLocked("interrupted")
	next := m.reserveLocked(d.utteranceID)
	log.Printf("turn_transition old_epoch=%d new_epoch=%d state=listening waiting_final=true", oldEpoch, next.epoch)
	m.emit(Event{Event: "turn_transition", Epoch: next.epoch, PreviousEpoch: oldEpoch, UtteranceID: d.utteranceID, Status: "listening"})
	if d.final != nil {
		m.startFinalLocked(next, *d.final)
	}
}
