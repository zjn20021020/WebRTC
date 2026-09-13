package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
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
	scene := flag.String("scene", "intent", "Fixture scene: intent, home, home-wait, home-replacement, home-praise, home-switch, home-plan or home-story")
	flag.Parse()
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
	fixtures := []struct{ name, text string }{
		{"ask-story", "\u8bf7\u7ed9\u6211\u8bb2\u4e00\u4e2a\u5c0f\u6545\u4e8b\u3002"},
		{"interrupt-question", "\u7b49\u4e00\u4e0b\uff0c\u4e0d\u8bb2\u6545\u4e8b\u4e86\uff0c\u8bf7\u544a\u8bc9\u6211\u4e00\u52a0\u4e00\u7b49\u4e8e\u51e0\u3002"},
		{"deferred-question", "\u4f60\u5148\u7ee7\u7eed\u8bb2\uff0c\u8bb2\u5b8c\u518d\u544a\u8bc9\u6211\u4e00\u52a0\u4e00\u7b49\u4e8e\u51e0\u3002"},
	}
	if *scene == "home" {
		fixtures = []struct{ name, text string }{
			{"home-water", "迪莫，你去浇下水。"},
			{"home-fertilize", "别浇水了去施肥。"},
			{"home-praise", "迪莫你真棒。"},
		}
	} else if *scene == "home-wait" {
		fixtures = []struct{ name, text string }{
			{"home-water-direct", "去浇水。"},
			{"home-wait-plant", "等一下再去种地。"},
		}
	} else if *scene == "home-replacement" {
		fixtures = []struct{ name, text string }{
			{"home-water-direct", "去浇水。"},
			{"home-replace-fertilize", "先别浇水了，去施肥。"},
			{"home-praise", "迪莫你真棒。"},
		}
	} else if *scene == "home-praise" {
		fixtures = []struct{ name, text string }{
			{"home-plant", "去种地。"},
			{"home-good-job", "干得不错。"},
		}
	} else if *scene == "home-switch" {
		fixtures = []struct{ name, text string }{
			{"home-plant-direct", "去种菜。"},
			{"home-fertilize-direct", "去施肥。"},
			{"home-question", "一加一等于几。"},
		}
	} else if *scene == "home-plan" {
		fixtures = []struct{ name, text string }{
			{"home-plan", "先种菜，再浇水，最后施肥。"},
			{"home-harvest-direct", "去收菜。"},
			{"home-wait-prefix", "等一下。"},
			{"home-wait-tail", "再去种地。"},
		}
	} else if *scene == "home-story" {
		fixtures = []struct{ name, text string }{
			{"home-story", "给我讲个故事吧。"},
			{"home-stop-story", "先别浇水了，给我讲个故事吧。"},
		}
	} else if *scene != "intent" {
		log.Fatal("unknown fixture scene")
	}
	for _, fixture := range fixtures {
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
