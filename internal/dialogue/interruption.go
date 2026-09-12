package dialogue

import (
	"log"
	"time"

	"webrtc-interrupt/internal/interrupt"
)

const (
	confirmationGrace = 800 * time.Millisecond
	maxSoftPause      = 4 * time.Second
)

type softPause struct {
	onset, deadline, resumeAt time.Time
	measured                  bool
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
		if t == nil || !m.asrListening || t.ctx.Err() != nil {
			return
		}
		if t.pause != nil {
			t.pause.resumeAt = time.Time{}
			return
		}
		if onset.IsZero() || onset.After(m.now()) {
			onset = m.now()
		}
		t.pause = &softPause{onset: onset, deadline: m.now().Add(maxSoftPause)}
		t.metrics.SpeechToPauseMS = nil
		m.statusLocked(t, "paused", "")
	case interrupt.SpeechEnded:
		if !m.inSpeech {
			return
		}
		m.inSpeech = false
		if t != nil && t.pause != nil {
			t.pause.resumeAt = m.now().Add(confirmationGrace)
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
	if !listening && m.current != nil && m.current.pause != nil {
		m.resumeLocked(m.current, "asr_unavailable")
	}
}

func (m *Manager) progressLocked(t *turn, status string) {
	t.stage = status
	if t.pause == nil {
		m.statusLocked(t, status, "")
	}
}

func (m *Manager) resumeLocked(t *turn, reason string) {
	t.pause = nil
	log.Printf("response epoch=%d status=%s reason=%s", t.epoch, t.stage, reason)
	m.emit(Event{Event: "response_status", Epoch: t.epoch, Status: t.stage, Reason: reason})
}
