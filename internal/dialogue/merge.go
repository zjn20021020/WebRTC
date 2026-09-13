package dialogue

import (
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"webrtc-interrupt/internal/asr"
)

const (
	finalMergeWindow     = 800 * time.Millisecond
	ambiguousMergeWindow = 1400 * time.Millisecond
	maxMergeDuration     = 6 * time.Second
	maxMergeGapMS        = 1500
	maxMergeParts        = 6
)

type mergeGroup struct {
	parts             []asr.Event
	started, deadline time.Time
	firstFinalAt      time.Time
}

// AcceptASR assembles provider sentences. Accept remains the entry point for an
// already-complete logical input. The existing RTP clock flushes bounded waits.
func (m *Manager) AcceptASR(event asr.Event) {
	if event.UtteranceID == "" || !hasSpeechText(event.Text) || (event.Event != "asr_partial" && event.Event != "asr_final") {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.seen[event.UtteranceID] {
		return
	}
	if utf8.RuneCountInString(event.Text) > 2000 {
		m.clearMergeLocked("input_too_long")
		m.rememberLocked(event.UtteranceID)
		m.emit(Event{Event: "input_rejected", Epoch: m.epoch, UtteranceID: event.UtteranceID, Reason: "input_too_long"})
		return
	}
	if event.Event == "asr_final" {
		event.FinalReceivedAt = m.now()
	}
	g := m.merge
	index := -1
	if g != nil {
		for i, part := range g.parts {
			if part.UtteranceID == event.UtteranceID {
				index = i
				break
			}
		}
		if index >= 0 && g.parts[index].Event == "asr_final" {
			return
		}
		if index < 0 && !canMerge(g, event, m.now()) {
			m.flushMergeLocked("boundary")
			g = nil
		}
	}
	if g == nil {
		g = &mergeGroup{started: m.now()}
		m.merge = g
	}
	if index < 0 {
		g.parts = append(g.parts, event)
	} else {
		g.parts[index] = event
	}
	if len(g.parts) > maxMergeParts || utf8.RuneCountInString(mergeText(g.parts)) > 2000 {
		m.clearMergeLocked("merge_limit")
		m.emit(Event{Event: "input_rejected", Epoch: m.epoch, UtteranceID: event.UtteranceID, Reason: "merge_limit"})
		return
	}
	if event.Event == "asr_final" {
		if g.firstFinalAt.IsZero() {
			g.firstFinalAt = m.now()
		}
		window := finalMergeWindow
		if ambiguousWait(speechText(mergeText(g.parts))) {
			window = ambiguousMergeWindow
		}
		g.deadline = m.now().Add(window)
	} else {
		g.deadline = time.Time{}
	}
	combined := combinedEvent(g.parts)
	combined.Event, combined.Provisional = "asr_partial", true
	m.emitMergeLocked(g, "collecting", "")
	m.acceptLocked(combined)
	if m.seen[combined.UtteranceID] {
		// The input queue can reject the provisional input before final.
		// Discard its collector too so later fragments cannot revive it.
		m.clearMergeLocked("queue_full")
	}
}

func streamID(id string) string {
	if index := strings.LastIndexByte(id, ':'); index >= 0 {
		return id[:index]
	}
	return ""
}

func canMerge(g *mergeGroup, event asr.Event, now time.Time) bool {
	last := g.parts[len(g.parts)-1]
	if last.Event != "asr_final" || (!g.firstFinalAt.IsZero() && now.Sub(g.firstFinalAt) >= maxMergeDuration) || (!g.deadline.IsZero() && !now.Before(g.deadline)) {
		return false
	}
	if streamID(last.UtteranceID) != streamID(event.UtteranceID) {
		return false
	}
	if last.EndTime > 0 && event.BeginTime > 0 && (event.BeginTime < last.BeginTime || event.BeginTime-last.EndTime > maxMergeGapMS) {
		return false
	}
	return true
}

func mergeText(parts []asr.Event) string {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		texts = append(texts, strings.TrimSpace(part.Text))
	}
	return strings.Join(texts, " ")
}

func combinedEvent(parts []asr.Event) asr.Event {
	event := parts[len(parts)-1]
	event.UtteranceID = parts[0].UtteranceID
	event.BeginTime = parts[0].BeginTime
	event.Text = mergeText(parts)
	return event
}

func (m *Manager) emitMergeLocked(g *mergeGroup, status, reason string) {
	ids := make([]string, 0, len(g.parts))
	for _, part := range g.parts {
		ids = append(ids, part.UtteranceID)
	}
	m.emit(Event{Event: "input_merge", Epoch: m.epoch, Status: status, Reason: reason, UtteranceID: ids[0], SourceIDs: ids, Text: mergeText(g.parts)})
	log.Printf("input_merge epoch=%d input=%q parts=%d status=%s reason=%s", m.epoch, ids[0], len(ids), status, reason)
}

func (m *Manager) pumpMergeLocked() {
	g := m.merge
	if g == nil {
		return
	}
	longInput := g.firstFinalAt.IsZero() && m.now().Sub(g.started) >= unfinishedInputTimeout
	mergeExpired := !g.firstFinalAt.IsZero() && m.now().Sub(g.firstFinalAt) >= maxMergeDuration
	if longInput || mergeExpired || (!g.deadline.IsZero() && !m.now().Before(g.deadline)) {
		m.flushMergeLocked("window_elapsed")
	}
}

func (m *Manager) flushMergeLocked(reason string) {
	g := m.merge
	if g == nil {
		return
	}
	m.merge = nil
	var finals []asr.Event
	incomplete := false
	for _, part := range g.parts {
		m.rememberLocked(part.UtteranceID)
		if part.Event == "asr_final" {
			finals = append(finals, part)
		} else {
			incomplete = true
			m.emit(Event{Event: "input_rejected", Epoch: m.epoch, UtteranceID: part.UtteranceID, Reason: "asr_final_timeout"})
		}
	}
	if incomplete || len(finals) == 0 {
		m.removeInterjectionLocked(g.parts[0].UtteranceID)
		m.emitMergeLocked(g, "discarded", reason)
		return
	}
	g.parts = finals
	m.emitMergeLocked(g, "committed", reason)
	event := combinedEvent(finals)
	event.Event, event.Provisional = "asr_final", false
	m.acceptLocked(event)
}

func (m *Manager) clearMergeLocked(reason string) {
	if m.merge == nil {
		return
	}
	for _, part := range m.merge.parts {
		m.rememberLocked(part.UtteranceID)
	}
	m.removeInterjectionLocked(m.merge.parts[0].UtteranceID)
	m.emitMergeLocked(m.merge, "discarded", reason)
	m.merge = nil
}
