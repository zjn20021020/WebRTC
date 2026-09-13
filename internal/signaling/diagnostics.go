package signaling

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
)

// Diagnostic metadata only: no raw audio, transcript or device identifier.
// Enabled explicitly by the local development server's diagnostics flag.
func BrowserDiagnostics(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		http.Error(w, "loopback only", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var sample struct {
		Session          string  `json:"session"`
		Context          string  `json:"context"`
		Track            string  `json:"track"`
		Muted            bool    `json:"muted"`
		Enabled          bool    `json:"enabled"`
		EchoCancellation *bool   `json:"echo_cancellation"`
		NoiseSuppression *bool   `json:"noise_suppression"`
		AutoGainControl  *bool   `json:"auto_gain_control"`
		OutputKind       string  `json:"output_kind"`
		OutputReason     string  `json:"output_reason"`
		Playback         bool    `json:"playback"`
		PageVisible      bool    `json:"page_visible"`
		ResponseState    string  `json:"response_state"`
		FrameAgeMS       float64 `json:"frame_age_ms"`
		RMS              float64 `json:"rms"`
		Energy           float64 `json:"energy"`
		Packets          uint64  `json:"packets"`
		Bytes            uint64  `json:"bytes"`
		FrameGapMS       float64 `json:"frame_gap_ms"`
		LongTaskMS       float64 `json:"long_task_ms"`
		ControlEvents    uint64  `json:"control_events"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&sample) != nil || len(sample.Session) > 64 || len(sample.Context) > 16 || len(sample.Track) > 16 || len(sample.ResponseState) > 32 || len(sample.OutputKind) > 16 || len(sample.OutputReason) > 64 {
		http.Error(w, "invalid diagnostics", http.StatusBadRequest)
		return
	}
	data, _ := json.Marshal(sample)
	log.Printf("browser_audio %s", data)
	w.WriteHeader(http.StatusNoContent)
}
