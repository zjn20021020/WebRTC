package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/pprof"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/pion/webrtc/v4"
	"webrtc-interrupt/internal/asr"
	"webrtc-interrupt/internal/dialogue"
	"webrtc-interrupt/internal/llm"
	"webrtc-interrupt/internal/signaling"
	"webrtc-interrupt/internal/tts"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	diagnostics := flag.Bool("diagnostics", false, "Enable loopback-only goroutine diagnostics")
	flag.Parse()
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatal("invalid .env file")
	}
	asrConfig := asr.ConfigFromEnv()
	log.Printf("ASR configured=%t model=%s sample_rate=%d", asrConfig.Enabled(), asrConfig.Model, asr.SampleRate)
	llmConfig := llm.ConfigFromEnv()
	ttsConfig, err := tts.ConfigFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	var model dialogue.LanguageModel
	var speech dialogue.SpeechSynthesizer
	if llmConfig.Enabled() {
		model, err = llm.NewClient(llmConfig)
		if err != nil {
			log.Fatal(err)
		}
	}
	if ttsConfig.Enabled() {
		speech, err = tts.NewClient(ttsConfig)
		if err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("LLM configured=%t model=%s; TTS configured=%t voice=%d sample_rate=%d", llmConfig.Enabled(), llmConfig.Model, ttsConfig.Enabled(), ttsConfig.VoiceType, tts.SampleRate)

	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypePCMU,
			ClockRate: 8000,
			Channels:  1,
		},
		PayloadType: 0,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		log.Fatalf("register codecs: %v", err)
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine))

	mux := http.NewServeMux()
	mux.HandleFunc("/api/audio-output", signaling.AudioOutput)
	if *diagnostics {
		mux.HandleFunc("/api/diagnostics", signaling.BrowserDiagnostics)
		mux.HandleFunc("/debug/goroutines", func(w http.ResponseWriter, r *http.Request) {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil || !net.ParseIP(host).IsLoopback() {
				http.Error(w, "loopback only", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_ = pprof.Lookup("goroutine").WriteTo(w, 2)
		})
	}
	signalingHandler := signaling.NewHandler(api, asrConfig, model, speech)
	mux.Handle("/api/offer", signalingHandler)
	mux.Handle("/", http.FileServer(http.Dir("web")))

	server := &http.Server{Addr: *addr, Handler: loggingMiddleware(mux)}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		signalingHandler.Close()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	log.Printf("WebRTC demo listening on http://localhost%s", *addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
