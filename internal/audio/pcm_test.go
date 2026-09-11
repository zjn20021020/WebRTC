package audio

import (
	"bytes"
	"testing"
)

func TestPCM16LEByteOrder(t *testing.T) {
	got := PCM16LE([]int16{0, 1, -1, -32768, 32767})
	want := []byte{0, 0, 1, 0, 255, 255, 0, 128, 255, 127}
	if !bytes.Equal(got, want) {
		t.Fatalf("PCM bytes = %x, want %x", got, want)
	}
}
