package rtc

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"webrtc-interrupt/internal/audio"
	"webrtc-interrupt/internal/interrupt"
)

const (
	pcmuFrameSamples = 160
	pcmuSampleRate   = 8000
)

// Session owns the peer connection and the long-lived outbound audio track.
// Later ASR, TTS and turn coordination code will attach to this boundary.
type Session struct {
	PeerConnection *webrtc.PeerConnection
	OutboundTrack  *webrtc.TrackLocalStaticRTP

	closeOnce   sync.Once
	stop        chan struct{}
	controlMu   sync.RWMutex
	control     *webrtc.DataChannel
	vad         *interrupt.Detector
	fixedFrames [][]byte
}

func NewSession(api *webrtc.API) (*Session, error) {
	peerConnection, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, err
	}

	outboundTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypePCMU,
			ClockRate: 8000,
			Channels:  1,
		},
		"audio",
		"server-audio",
	)
	if err != nil {
		_ = peerConnection.Close()
		return nil, err
	}
	if _, err := peerConnection.AddTrack(outboundTrack); err != nil {
		_ = peerConnection.Close()
		return nil, err
	}

	session := &Session{
		PeerConnection: peerConnection,
		OutboundTrack:  outboundTrack,
		stop:           make(chan struct{}),
		vad:            interrupt.NewDetector(700, 200*time.Millisecond, 500*time.Millisecond, 20*time.Millisecond),
		fixedFrames:    audio.GenerateTestToneFrames(pcmuSampleRate, pcmuFrameSamples),
	}

	peerConnection.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		log.Printf("inbound track: kind=%s codec=%s/%d", track.Kind(), track.Codec().MimeType, track.Codec().ClockRate)
		session.readInbound(track)
	})
	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("peer connection state: %s", state)
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			session.Close()
		}
	})
	peerConnection.OnDataChannel(func(channel *webrtc.DataChannel) {
		session.controlMu.Lock()
		session.control = channel
		session.controlMu.Unlock()
		log.Printf("data channel: label=%s", channel.Label())
		channel.OnOpen(func() { log.Printf("data channel open: label=%s", channel.Label()) })
		channel.OnClose(func() {
			log.Printf("data channel closed: label=%s", channel.Label())
			session.controlMu.Lock()
			if session.control == channel {
				session.control = nil
			}
			session.controlMu.Unlock()
		})
	})

	go session.writeSilence()
	return session, nil
}

func (s *Session) readInbound(track *webrtc.TrackRemote) {
	var packets uint64
	for {
		packet, _, err := track.ReadRTP()
		if err != nil {
			log.Printf("inbound track ended after %d packets: %v", packets, err)
			return
		}
		packets++
		if track.Codec().MimeType == webrtc.MimeTypePCMU {
			rms := audio.RMS(audio.DecodePCMU(packet.Payload))
			if event, ok := s.vad.Update(rms); ok {
				log.Printf("vad event=%s packets=%d pcmu_rms=%.1f", event, packets, rms)
				s.sendControl(string(event), rms)
			}
			if packets%50 == 0 {
				log.Printf("inbound audio: packets=%d sequence=%d timestamp=%d bytes=%d pcmu_rms=%.1f", packets, packet.SequenceNumber, packet.Timestamp, len(packet.Payload), rms)
			}
		} else if packets%50 == 0 {
			log.Printf("inbound audio: packets=%d sequence=%d timestamp=%d bytes=%d codec=%s (decoder unavailable)", packets, packet.SequenceNumber, packet.Timestamp, len(packet.Payload), track.Codec().MimeType)
		}
	}
}

func (s *Session) sendControl(event string, rms float64) {
	message, err := json.Marshal(struct {
		Event string  `json:"event"`
		RMS   float64 `json:"rms"`
	}{Event: event, RMS: rms})
	if err != nil {
		return
	}
	s.controlMu.RLock()
	channel := s.control
	s.controlMu.RUnlock()
	if channel != nil {
		if err := channel.SendText(string(message)); err != nil {
			log.Printf("send control event: %v", err)
		}
	}
}

func (s *Session) writeSilence() {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	var sequence uint16
	var timestamp uint32
	for {
		select {
		case <-ticker.C:
			payload := make([]byte, pcmuFrameSamples)
			if len(s.fixedFrames) > 0 {
				copy(payload, s.fixedFrames[0])
			} else {
				for i := range payload {
					payload[i] = 0xff // PCMU silence
				}
			}
			packet := &rtp.Packet{
				Header:  rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: sequence, Timestamp: timestamp},
				Payload: payload,
			}
			if err := s.OutboundTrack.WriteRTP(packet); err != nil {
				// The track is unbound until SDP negotiation finishes. Keep the
				// clock running so the first bound writer receives fresh packets.
				continue
			}
			if len(s.fixedFrames) > 0 {
				s.fixedFrames = s.fixedFrames[1:]
			}
			sequence++
			timestamp += 160
		case <-s.stop:
			return
		}
	}
}

func (s *Session) Close() {
	s.closeOnce.Do(func() {
		close(s.stop)
		if err := s.PeerConnection.Close(); err != nil {
			log.Printf("close peer connection: %v", err)
		}
	})
}
