package asr

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

// Explicitly opt in: uses the existing synthetic fixture and real ASR quota.
// No microphone recording, LLM or TTS request is involved in this probe.
func TestLiveShortCommand(t *testing.T) {
	if os.Getenv("ASR_LIVE_PROBE") != "1" {
		t.Skip("set ASR_LIVE_PROBE=1 for cloud probe")
	}
	if err := godotenv.Load("../../.env"); err != nil {
		t.Fatal("cannot load local ASR configuration")
	}
	wav, err := os.ReadFile("../../bin/home-plant.wav")
	if err != nil {
		t.Fatal(err)
	}
	// demo-fixtures writes this exact PCM16LE mono header. Reject other formats.
	if len(wav) < 44 || string(wav[:4]) != "RIFF" || string(wav[8:16]) != "WAVEfmt " || binary.LittleEndian.Uint32(wav[16:20]) != 16 || binary.LittleEndian.Uint16(wav[20:22]) != 1 || binary.LittleEndian.Uint16(wav[22:24]) != 1 || binary.LittleEndian.Uint32(wav[24:28]) != SampleRate || binary.LittleEndian.Uint16(wav[34:36]) != 16 || string(wav[36:40]) != "data" || int(binary.LittleEndian.Uint32(wav[40:44])) != len(wav)-44 {
		t.Fatal("expected unmodified demo-fixtures 8kHz mono PCM16 WAV")
	}
	config := ConfigFromEnv()
	mode := "baseline"
	var pcm []byte
	appendSilence := func(seconds int) { pcm = append(pcm, make([]byte, seconds*SampleRate*2)...) }
	type probe struct {
		Name           string
		BeginMS, EndMS int
		Gain           float64
	}
	var probes []probe
	for i, gain := range []float64{1, 0.1, 1} {
		gap := 3
		if i == 2 {
			gap = 12
		}
		appendSilence(gap)
		begin := len(pcm) * 1000 / (SampleRate * 2)
		for offset := 44; offset < len(wav); offset += 2 {
			sample := int16(float64(int16(binary.LittleEndian.Uint16(wav[offset:offset+2]))) * gain)
			pcm = binary.LittleEndian.AppendUint16(pcm, uint16(sample))
		}
		probes = append(probes, probe{Name: []string{"normal", "quiet", "after_silence"}[i], BeginMS: begin, EndMS: len(pcm) * 1000 / (SampleRate * 2), Gain: gain})
	}
	appendSilence(4)
	input := make(chan []byte, 1)
	input <- pcm
	close(input)
	type record struct {
		At    time.Time
		Event Event
	}
	var records []record
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err = Run(ctx, config, input, func(e Event) { records = append(records, record{time.Now(), e}) })
	var finals []Event
	for _, r := range records {
		if r.Event.Event == "asr_final" {
			finals = append(finals, r.Event)
		}
	}
	passed := err == nil && len(finals) == len(probes)
	for _, e := range finals {
		passed = passed && strings.Contains(e.Text, "种地")
	}
	result := struct {
		Mode    string
		Passed  bool
		Probes  []probe
		Records []record
		Error   string
	}{mode, passed, probes, records, ""}
	if err != nil {
		result.Error = err.Error()
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	directory := "../../bin/asr-probe"
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, mode+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if !passed {
		t.Fatalf("ASR probe failed: error=%v finals=%+v", err, finals)
	}
}
