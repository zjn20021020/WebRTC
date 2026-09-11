package interrupt

import (
	"testing"
	"time"
)

func TestDetectorUsesHysteresis(t *testing.T) {
	detector := NewDetector(10, 60*time.Millisecond, 40*time.Millisecond, 20*time.Millisecond)
	for i := 0; i < 2; i++ {
		if event, ok := detector.Update(20); ok || event != "" {
			t.Fatalf("speech started too early: %q", event)
		}
	}
	if event, ok := detector.Update(20); !ok || event != SpeechStarted {
		t.Fatalf("got %q, %v; want speech_started", event, ok)
	}
	if event, ok := detector.Update(0); ok || event != "" {
		t.Fatalf("speech ended too early: %q", event)
	}
	if event, ok := detector.Update(0); !ok || event != SpeechEnded {
		t.Fatalf("got %q, %v; want speech_ended", event, ok)
	}
}
