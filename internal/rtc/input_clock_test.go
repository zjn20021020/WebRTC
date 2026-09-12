package rtc

import (
	"testing"
	"time"
)

func TestInputClockMapsAudioOffsetsAcrossNetworkGaps(t *testing.T) {
	var clock inputClock
	start := time.Unix(100, 0)
	clock.record(160, start.Add(20*time.Millisecond))
	clock.record(160, start.Add(80*time.Millisecond))
	if got := clock.at(10); !got.Equal(start.Add(10 * time.Millisecond)) {
		t.Fatalf("first packet offset: %v", got)
	}
	if got := clock.at(30); !got.Equal(start.Add(70 * time.Millisecond)) {
		t.Fatalf("arrival gap lost: %v", got)
	}
	if got := clock.at(20); !got.Equal(start.Add(20 * time.Millisecond)) {
		t.Fatalf("packet boundary shifted: %v", got)
	}
	for _, offset := range []int64{-1, 0, 41, 1 << 62} {
		if !clock.at(offset).IsZero() {
			t.Fatal("invalid offset produced a timestamp")
		}
	}
}

func TestInputClockBoundsRetainedHistory(t *testing.T) {
	var clock inputClock
	start := time.Unix(100, 0)
	for i := 0; i < len(clock.spans)+5; i++ {
		clock.record(160, start.Add(time.Duration(i+1)*20*time.Millisecond))
	}
	if !clock.at(20).IsZero() {
		t.Fatal("expired sample timestamps retained")
	}
	if got := clock.at(120100); !got.Equal(start.Add(120100 * time.Millisecond)) {
		t.Fatal("ring wrap lost latest timestamp")
	}
}
