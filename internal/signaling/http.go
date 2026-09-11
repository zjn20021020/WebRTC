package signaling

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"

	"github.com/pion/webrtc/v4"
	"webrtc-interrupt/internal/rtc"
)

type offerRequest struct {
	SDP  string         `json:"sdp"`
	Type webrtc.SDPType `json:"type"`
}

type answerResponse struct {
	SDP  string         `json:"sdp"`
	Type webrtc.SDPType `json:"type"`
}

type Handler struct {
	api *webrtc.API

	mu      sync.Mutex
	current *rtc.Session
}

func NewHandler(api *webrtc.API) http.Handler {
	return &Handler{api: api}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var request offerRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid JSON offer", http.StatusBadRequest)
		return
	}
	if request.Type != webrtc.SDPTypeOffer || request.SDP == "" {
		http.Error(w, "a non-empty SDP offer is required", http.StatusBadRequest)
		return
	}

	session, err := rtc.NewSession(h.api)
	if err != nil {
		http.Error(w, "create peer connection failed", http.StatusInternalServerError)
		log.Printf("create session: %v", err)
		return
	}

	h.mu.Lock()
	if h.current != nil {
		h.current.Close()
	}
	h.current = session
	h.mu.Unlock()

	offer := webrtc.SessionDescription{Type: request.Type, SDP: request.SDP}
	if err := session.PeerConnection.SetRemoteDescription(offer); err != nil {
		session.Close()
		http.Error(w, "set remote description failed", http.StatusBadRequest)
		log.Printf("set remote description: %v", err)
		return
	}

	answer, err := session.PeerConnection.CreateAnswer(nil)
	if err != nil {
		session.Close()
		http.Error(w, "create answer failed", http.StatusInternalServerError)
		log.Printf("create answer: %v", err)
		return
	}
	if err := session.PeerConnection.SetLocalDescription(answer); err != nil {
		session.Close()
		http.Error(w, "set local description failed", http.StatusInternalServerError)
		log.Printf("set local description: %v", err)
		return
	}

	<-webrtc.GatheringCompletePromise(session.PeerConnection)
	localDescription := session.PeerConnection.LocalDescription()
	if localDescription == nil {
		session.Close()
		http.Error(w, "local description is empty", http.StatusInternalServerError)
		return
	}

	if err := writeJSON(w, answerResponse{SDP: localDescription.SDP, Type: localDescription.Type}); err != nil && !errors.Is(err, http.ErrAbortHandler) {
		log.Printf("write answer: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, value any) error {
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(value)
}
