package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/pion/webrtc/v4"
	"webrtc-interrupt/internal/signaling"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	flag.Parse()

	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		log.Fatalf("register codecs: %v", err)
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine))

	mux := http.NewServeMux()
	mux.Handle("/api/offer", signaling.NewHandler(api))
	mux.Handle("/", http.FileServer(http.Dir("web")))

	server := &http.Server{Addr: *addr, Handler: loggingMiddleware(mux)}
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
