package audio

import "testing"

func TestDecodePCMUSilence(t *testing.T) {
	samples := DecodePCMU([]byte{0xff, 0xff, 0xff})
	for _, sample := range samples {
		if sample != 0 {
			t.Fatalf("silence decoded to %d", sample)
		}
	}
}

func TestRMSEmptyAndSignal(t *testing.T) {
	if got := RMS(nil); got != 0 {
		t.Fatalf("RMS(nil) = %v, want 0", got)
	}
	if got := RMS([]int16{-3, 3}); got != 3 {
		t.Fatalf("RMS([-3, 3]) = %v, want 3", got)
	}
}
