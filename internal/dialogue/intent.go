package dialogue

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"webrtc-interrupt/internal/asr"
	"webrtc-interrupt/internal/llm"
)

const (
	intentTimeout          = 2 * time.Second
	intentCooldown         = 500 * time.Millisecond
	unfinishedInputTimeout = 15 * time.Second
	maxInterjections       = 8
)

type interjection struct {
	event                                 asr.Event
	partial                               string
	partialAt, updatedAt, requestedAt     time.Time
	eligible, running, buffered, approved bool
	waitForFinal                          bool
	checkedText                           string
	checkedFinal                          bool
	partialRequests                       int
	version, ownerEpoch                   uint64
	cancel                                context.CancelFunc
}

func (c *interjection) final() bool { return c.event.Event == "asr_final" }

func (m *Manager) acceptInterjectionLocked(event asr.Event) {
	var c *interjection
	for _, item := range m.interjections {
		if item.event.UtteranceID == event.UtteranceID {
			c = item
			break
		}
	}
	if c != nil && c.final() {
		return
	}
	if c == nil {
		if len(m.interjections) >= maxInterjections {
			m.emit(Event{Event: "input_rejected", Epoch: m.epoch, UtteranceID: event.UtteranceID, Reason: "queue_full"})
			m.rememberLocked(event.UtteranceID)
			return
		}
		c = &interjection{partialAt: m.now()}
		m.interjections = append(m.interjections, c)
		if m.current != nil && !m.current.waitingFinal {
			m.beginDuckLocked(m.current, m.now())
		}
	}
	event.Text = limitText(event.Text, 2000)
	text := speechText(event.Text)
	stable := commonPrefix(c.partial, text) >= 2 && m.now().Sub(c.partialAt) >= partialStability
	if commonPrefix(c.partial, text) < 2 {
		c.partialAt = m.now()
	}
	c.partial = text
	c.event, c.updatedAt = event, m.now()
	if !c.final() && !c.waitForFinal && ambiguousWait(text) {
		// A wait prefix can grow into "wait, then plant". Keep this entire
		// utterance provisional until final, even if later partials rewrite it.
		c.waitForFinal = true
		log.Printf("intent epoch=%d utterance_id=%q status=waiting_final reason=ambiguous_wait", m.epoch, event.UtteranceID)
		m.emit(Event{Event: "intent_status", Epoch: m.epoch, UtteranceID: event.UtteranceID, Status: "waiting_final", Reason: "ambiguous_wait"})
	}
	c.eligible = !event.Provisional && (c.final() || (!c.waitForFinal && (explicitStop(text) || stable)))
	// Appending words can reverse intent just as a rewritten prefix can.
	if (c.running || c.approved) && (c.final() || text != c.checkedText) {
		m.cancelIntentLocked(c)
		if c.approved && m.current != nil && m.current.duck != nil && m.current.duck.utteranceID == event.UtteranceID {
			d := m.current.duck
			d.confirmed, d.utteranceID, d.final = false, "", nil
			log.Printf("intent epoch=%d approval_revoked=true reason=transcript_updated", m.epoch)
		}
		c.approved = false
	}
	if c.approved && m.current != nil && m.current.duck != nil && m.current.duck.utteranceID == event.UtteranceID {
		return
	}
	if m.current == nil {
		m.drainInterjectionsLocked()
		return
	}
	if !m.current.waitingFinal {
		m.requestIntentLocked(m.current, c)
	}
}

func limitText(text string, n int) string {
	runes := []rune(text)
	if len(runes) > n {
		return string(runes[:n])
	}
	return text
}

func (m *Manager) requestIntentLocked(t *turn, c *interjection) {
	if c.running || c.buffered || c.approved || !c.eligible || t.ctx.Err() != nil || (t.duck != nil && t.duck.confirmed) {
		return
	}
	text := speechText(c.event.Text)
	if c.checkedText == text && c.checkedFinal == c.final() {
		return
	}
	if !c.final() && (c.partialRequests >= 3 || (!c.requestedAt.IsZero() && m.now().Sub(c.requestedAt) < intentCooldown)) {
		return
	}
	if !c.final() {
		c.partialRequests++
	}
	c.checkedText, c.checkedFinal = text, c.final()
	c.requestedAt, c.ownerEpoch, c.running = m.now(), t.epoch, true
	c.version++
	version := c.version
	ctx, cancel := context.WithTimeout(t.ctx, intentTimeout)
	c.cancel = cancel
	input := llm.InterruptionInput{UserText: c.event.Text, IsFinal: c.final(), AssistantResponse: limitText(t.text, 1200)}
	if t.toolCall != nil {
		input.CurrentTool = string(t.toolCall.Name)
	}
	for i := len(m.history) - 1; i >= 0; i-- {
		if m.history[i].Role == "user" {
			input.PreviousUserText = limitText(m.history[i].Content, 600)
			break
		}
	}
	m.beginDuckLocked(t, m.now())
	m.emit(Event{Event: "intent_status", Epoch: t.epoch, UtteranceID: c.event.UtteranceID, Status: "checking"})
	model := m.model
	go func() {
		started := time.Now()
		decision, err := false, errors.New("intent model is not configured")
		if model != nil {
			decision, err = model.ClassifyInterruption(ctx, input)
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		cancel()
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.closed || m.current != t || c.version != version || !c.running {
			return
		}
		c.running, c.cancel = false, nil
		latency := time.Since(started).Milliseconds()
		event := Event{Event: "intent_result", Epoch: t.epoch, UtteranceID: c.event.UtteranceID, LatencyMS: &latency}
		if err != nil {
			decision = false
			event.Fallback, event.Status, event.Reason = true, "error", "request_failed"
			if errors.Is(err, context.DeadlineExceeded) {
				event.Reason = "timeout"
			} else if errors.Is(err, llm.ErrInvalidIntentResult) {
				event.Reason = "invalid_output"
			}
			log.Printf("intent epoch=%d utterance_id=%q is_final=%t interrupt=false fallback=true reason=%s latency_ms=%d error=%q", t.epoch, c.event.UtteranceID, input.IsFinal, event.Reason, latency, err)
		} else {
			log.Printf("intent epoch=%d utterance_id=%q is_final=%t interrupt=%t fallback=false latency_ms=%d", t.epoch, c.event.UtteranceID, input.IsFinal, decision, latency)
		}
		event.Interrupt = &decision
		m.emit(event)
		if err == nil && decision {
			c.approved = true
			m.beginDuckLocked(t, m.now())
			t.duck.confirmed, t.duck.utteranceID = true, c.event.UtteranceID
			if c.final() {
				copy := c.event
				t.duck.final = &copy
			}
			if !t.playing || !m.now().Before(t.duck.minimumUntil) {
				m.confirmLocked(t)
			}
			return
		}
		if c.final() {
			c.buffered = true
			m.emit(Event{Event: "input_buffered", Epoch: t.epoch, UtteranceID: c.event.UtteranceID, Text: c.event.Text})
			m.queueChangedLocked()
		}
		if t.duck != nil && !t.duck.confirmed && !m.intentRunningLocked(t.epoch) {
			m.resumeLocked(t, "intent_deferred")
		}
	}()
}

// This delays a decision; it never classifies an utterance or authorizes a stop.
func ambiguousWait(text string) bool {
	for _, phrase := range []string{"等一下", "等下", "等等", "等一会", "等会", "待会", "稍后", "过会"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return strings.HasSuffix(text, "等") || strings.HasSuffix(text, "等一")
}

func (m *Manager) intentRunningLocked(epoch uint64) bool {
	for _, c := range m.interjections {
		if c.ownerEpoch == epoch && c.running {
			return true
		}
	}
	return false
}

func (m *Manager) cancelIntentLocked(c *interjection) {
	if c.cancel != nil {
		c.cancel()
	}
	c.version++
	c.running, c.cancel = false, nil
}

func (m *Manager) cancelIntentRequestsLocked(epoch uint64) {
	for _, c := range m.interjections {
		if c.ownerEpoch == epoch {
			if c.running {
				// A planned next step must reconsider a decision cancelled with
				// the previous step, using its own current action as context.
				c.checkedText = ""
			}
			m.cancelIntentLocked(c)
		}
	}
}

func (m *Manager) removeInterjectionLocked(id string) {
	for i, c := range m.interjections {
		if c.event.UtteranceID == id {
			if m.joining != nil && (m.joining.first == c || m.joining.second == c) {
				m.cancelQueuedJoinLocked()
			}
			m.cancelIntentLocked(c)
			m.interjections = append(m.interjections[:i], m.interjections[i+1:]...)
			m.queueChangedLocked()
			return
		}
	}
}

func (m *Manager) queueChangedLocked() {
	size := 0
	for _, c := range m.interjections {
		if c.final() {
			size++
		}
	}
	m.emit(Event{Event: "input_queue", Epoch: m.epoch, QueueSize: &size})
}

func (m *Manager) clearInterjectionsLocked() {
	m.cancelQueuedJoinLocked()
	for _, c := range m.interjections {
		m.cancelIntentLocked(c)
		m.rememberLocked(c.event.UtteranceID)
	}
	m.interjections = nil
	m.queueChangedLocked()
}

func (m *Manager) dropUnfinishedInterjectionsLocked() {
	for i := len(m.interjections) - 1; i >= 0; i-- {
		c := m.interjections[i]
		if !c.final() && !c.approved {
			m.removeInterjectionLocked(c.event.UtteranceID)
			m.rememberLocked(c.event.UtteranceID)
		}
	}
}

func (m *Manager) pumpInterjectionsLocked() {
	for i := len(m.interjections) - 1; i >= 0; i-- {
		c := m.interjections[i]
		if !c.final() && !c.approved && m.now().Sub(c.updatedAt) >= unfinishedInputTimeout {
			m.emit(Event{Event: "input_rejected", Epoch: m.epoch, UtteranceID: c.event.UtteranceID, Reason: "asr_final_timeout"})
			m.removeInterjectionLocked(c.event.UtteranceID)
			m.rememberLocked(c.event.UtteranceID)
		}
	}
	if m.current == nil {
		m.drainInterjectionsLocked()
		return
	}
	if !m.current.waitingFinal {
		for _, c := range m.interjections {
			m.requestIntentLocked(m.current, c)
		}
	}
}

func (m *Manager) drainInterjectionsLocked() {
	if m.closed || m.current != nil || len(m.interjections) == 0 {
		return
	}
	c := m.interjections[0]
	if !c.final() {
		return
	}
	if m.joinQueuedInputsLocked(c) {
		return
	}
	m.dispatchInterjectionLocked(c)
}

func (m *Manager) dispatchInterjectionLocked(c *interjection) {
	event := c.event
	m.removeInterjectionLocked(event.UtteranceID)
	m.emit(Event{Event: "input_dispatched", Epoch: m.epoch, UtteranceID: event.UtteranceID, Text: event.Text})
	m.startFinalLocked(m.reserveLocked(event.UtteranceID), event)
}
