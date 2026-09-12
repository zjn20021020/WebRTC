package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/joho/godotenv"
	"webrtc-interrupt/internal/audio"
	"webrtc-interrupt/internal/tts"
)

// Generate public, fixed microphone inputs for the live WebRTC demonstration.
func main() {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Fatal("invalid .env file")
	}
	cfg, err := tts.ConfigFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	client, err := tts.NewClient(cfg)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll("bin", 0755); err != nil {
		log.Fatal(err)
	}
	for _, fixture := range []struct{ name, text string }{
		{"ask-story", "\u8bf7\u7ed9\u6211\u8bb2\u4e00\u4e2a\u5c0f\u6545\u4e8b\u3002"},
		{"interrupt-question", "\u7b49\u4e00\u4e0b\uff0c\u4e0d\u8bb2\u6545\u4e8b\u4e86\uff0c\u8bf7\u544a\u8bc9\u6211\u4e00\u52a0\u4e00\u7b49\u4e8e\u51e0\u3002"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		encoded, err := client.Synthesize(ctx, fixture.text)
		cancel()
		if err != nil {
			log.Fatal(err)
		}
		pcm := audio.PCM16LE(audio.DecodePCMU(encoded))
		var wav bytes.Buffer
		wav.WriteString("RIFF")
		_ = binary.Write(&wav, binary.LittleEndian, uint32(36+len(pcm)))
		wav.WriteString("WAVEfmt ")
		_ = binary.Write(&wav, binary.LittleEndian, uint32(16))
		_ = binary.Write(&wav, binary.LittleEndian, []uint16{1, 1})
		_ = binary.Write(&wav, binary.LittleEndian, []uint32{8000, 16000})
		_ = binary.Write(&wav, binary.LittleEndian, []uint16{2, 16})
		wav.WriteString("data")
		_ = binary.Write(&wav, binary.LittleEndian, uint32(len(pcm)))
		wav.Write(pcm)
		path := filepath.Join("bin", fixture.name+".wav")
		if err := os.WriteFile(path, wav.Bytes(), 0600); err != nil {
			log.Fatal(err)
		}
		log.Printf("fixture=%s samples=%d", path, len(encoded))
	}
}
