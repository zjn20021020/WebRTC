package signaling

import (
	"encoding/json"
	"net"
	"net/http"

	"webrtc-interrupt/internal/audiooutput"
)

func AudioOutput(w http.ResponseWriter, r *http.Request) {
	audioOutput(w, r, audiooutput.Current)
}

func audioOutput(w http.ResponseWriter, r *http.Request, lookup func() audiooutput.Route) {
	w.Header().Set("Cache-Control", "no-store")
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		http.Error(w, "local output information requires loopback access", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(lookup())
}
