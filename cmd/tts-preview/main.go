package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"log"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/joho/godotenv"
	"webrtc-interrupt/internal/audio"
	"webrtc-interrupt/internal/tts"
)

// Preview the runtime streaming voice after its PCM16 -> PCMU conversion.
func main() {
	voice := flag.Int64("voice", 0, "Voice ID override; 0 uses the configured voice")
	text := flag.String("text", "\u5c0f\u6d1b\u514b\uff0c\u6211\u662f\u8fea\u83ab\u3002\u6211\u6b63\u5728\u79cd\u83dc\u3002\u6211\u6b63\u5728\u6d47\u6c34\u3002\u8d34\u8d34\u3002", "Preview text, up to 100 characters")
	output := flag.String("out", "bin/tts-preview.wav", "Output WAV path")
	flag.Parse()
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Fatal("invalid .env file")
	}
	cfg, err := tts.ConfigFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	if *voice < 0 {
		log.Fatal("voice must be a positive ID or 0 for the configured voice")
	}
	if *voice != 0 {
		cfg.VoiceType = *voice
	}
	client, err := tts.NewClient(cfg)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	started := time.Now()
	var firstChunk time.Duration
	var encoded []byte
	chunks := 0
	err = client.Stream(ctx, *text, func(chunk []byte) error {
		if chunks == 0 {
			firstChunk = time.Since(started)
		}
		chunks++
		encoded = append(encoded, chunk...)
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
	samples := audio.DecodePCMU(encoded)
	var energy float64
	for _, sample := range samples {
		energy += float64(sample) * float64(sample)
	}
	if len(samples) == 0 || energy == 0 {
		log.Fatal("stream returned silent audio")
	}
	pcm := audio.PCM16LE(samples)
	var wav bytes.Buffer
	wav.WriteString("RIFF")
	_ = binary.Write(&wav, binary.LittleEndian, uint32(36+len(pcm)))
	wav.WriteString("WAVEfmt ")
	_ = binary.Write(&wav, binary.LittleEndian, uint32(16))
	_ = binary.Write(&wav, binary.LittleEndian, []uint16{1, 1})
	_ = binary.Write(&wav, binary.LittleEndian, []uint32{tts.SampleRate, tts.SampleRate * 2})
	_ = binary.Write(&wav, binary.LittleEndian, []uint16{2, 16})
	wav.WriteString("data")
	_ = binary.Write(&wav, binary.LittleEndian, uint32(len(pcm)))
	wav.Write(pcm)
	if err := os.MkdirAll(filepath.Dir(*output), 0755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*output, wav.Bytes(), 0600); err != nil {
		log.Fatal(err)
	}
	log.Printf("voice=%d sample_rate=%d chunks=%d first_chunk_ms=%d duration_ms=%d rms=%.0f output=%s",
		cfg.VoiceType, tts.SampleRate, chunks, firstChunk.Milliseconds(), len(samples)*1000/tts.SampleRate,
		math.Sqrt(energy/float64(len(samples))), *output)
}
