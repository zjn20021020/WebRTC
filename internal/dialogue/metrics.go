package dialogue

import (
	"encoding/json"
	"log"
	"time"
)

// Speech-end times refer to the arrival of ASR's last input sample at the
// server. Audio times refer to RTP writes, not the device's playback clock.
type Metrics struct {
	SpeechEndToTextMS  *int64 `json:"speech_end_to_first_text_ms,omitempty"`
	SpeechEndToAudioMS *int64 `json:"speech_end_to_first_audio_ms,omitempty"`
	FinalToTextMS      *int64 `json:"asr_final_to_first_text_ms,omitempty"`
	FinalToAudioMS     *int64 `json:"asr_final_to_first_audio_ms,omitempty"`
	SpeechToDuckMS     *int64 `json:"speech_to_duck_ms,omitempty"`
}

func elapsedMS(start, end time.Time) *int64 {
	if start.IsZero() || end.Before(start) {
		return nil
	}
	ms := end.Sub(start).Milliseconds()
	return &ms
}

func (m *Manager) metricsLocked(t *turn) {
	metrics := t.metrics
	encoded, _ := json.Marshal(metrics)
	log.Printf("response epoch=%d metrics=%s", t.epoch, encoded)
	m.emit(Event{Event: "response_metrics", Epoch: t.epoch, Metrics: &metrics})
}
