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

func TestEncodePCMUSilence(t *testing.T) {
	encoded := EncodePCMU([]int16{0, 0, 0})
	for _, value := range encoded {
		if value != 0xff {
			t.Fatalf("silence encoded to %#x", value)
		}
	}
}

func TestGenerateTestToneFrames(t *testing.T) {
	frames := GenerateTestToneFrames(8000, 160)
	if len(frames) != 100 {
		t.Fatalf("got %d frames, want 100", len(frames))
	}
	if got := RMS(DecodePCMU(frames[10])); got < 100 {
		t.Fatalf("test tone RMS = %.1f, want audible signal", got)
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
