package dialogue

import (
	"context"
	"errors"
	"log"
	"time"
	"unicode/utf8"

	"webrtc-interrupt/internal/asr"
	"webrtc-interrupt/internal/llm"
)

const continuationTimeout = 2 * time.Second

type ContinuationClassifier interface {
	ClassifyContinuation(context.Context, llm.ContinuationInput) (bool, error)
}

type queuedJoin struct {
	first, second *interjection
	cancel        context.CancelFunc
}

func (m *Manager) cancelQueuedJoinLocked() {
	if m.joining != nil {
		m.joining.cancel()
		m.joining = nil
	}
}

// Called only while idle, before reserving an epoch or starting the planner.
// Hold the queue head until the adjacent draft or bounded relation check ends.
func (m *Manager) joinQueuedInputsLocked(first *interjection) bool {
	if m.joining != nil {
		return true
	}
	model, ok := m.model.(ContinuationClassifier)
	if !ok || len(m.interjections) < 2 {
		return false
	}
	second := m.interjections[1]
	if streamID(first.event.UtteranceID) != streamID(second.event.UtteranceID) {
		return false
	}
	if !second.final() {
		return true
	}
	parts := []asr.Event{first.event, second.event}
	combined := combinedEvent(parts)
	if len(combined.SourceIDs) > maxMergeParts || utf8.RuneCountInString(combined.Text) > 2000 {
		// Preserve both complete requests separately instead of truncating either.
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), continuationTimeout)
	join := &queuedJoin{first: first, second: second, cancel: cancel}
	m.joining = join
	input := llm.ContinuationInput{PendingText: first.event.Text, UserText: second.event.Text}
	m.emit(Event{Event: "input_relation", Epoch: m.epoch, UtteranceID: first.event.UtteranceID, SourceIDs: combined.SourceIDs, Status: "checking"})
	go func() {
		started := time.Now()
		decision, err := model.ClassifyContinuation(ctx, input)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		cancel()
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.closed || m.joining != join {
			return
		}
		m.joining = nil
		latency := time.Since(started).Milliseconds()
		result := Event{Event: "input_relation", Epoch: m.epoch, UtteranceID: first.event.UtteranceID,
			SourceIDs: combined.SourceIDs, Status: "completed", LatencyMS: &latency}
		if err != nil {
			decision = false
			result.Fallback, result.Status, result.Reason = true, "error", "request_failed"
			if errors.Is(err, context.DeadlineExceeded) {
				result.Reason = "timeout"
			} else if errors.Is(err, llm.ErrInvalidContinuationResult) {
				result.Reason = "invalid_output"
			}
		}
		result.Continuation = &decision
		m.emit(result)
		log.Printf("input_relation input=%q continuation=%t fallback=%t reason=%s latency_ms=%d", first.event.UtteranceID, decision, result.Fallback, result.Reason, latency)
		if decision {
			first.event = combined
			m.removeInterjectionLocked(second.event.UtteranceID)
			for _, id := range combined.SourceIDs {
				m.rememberLocked(id)
			}
			m.emitMergeLocked(&mergeGroup{parts: parts}, "committed", "queued_continuation")
			m.drainInterjectionsLocked()
			return
		}
		// A valid false or failed check preserves FIFO and both original texts.
		m.dispatchInterjectionLocked(first)
	}()
	return true
}
