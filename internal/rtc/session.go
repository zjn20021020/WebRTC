package rtc

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"webrtc-interrupt/internal/asr"
	"webrtc-interrupt/internal/audio"
	"webrtc-interrupt/internal/dialogue"
	"webrtc-interrupt/internal/interrupt"
)

const (
	pcmuFrameSamples = 160
	pcmuSampleRate   = 8000
)

// Session owns the peer connection and the long-lived outbound audio track.
// The dialogue manager feeds synthesized audio into the same RTP clock.
type Session struct {
	PeerConnection *webrtc.PeerConnection
	OutboundTrack  *webrtc.TrackLocalStaticRTP

	closeOnce       sync.Once
	stop            chan struct{}
	controlMu       sync.RWMutex
	control         *webrtc.DataChannel
	vad             *interrupt.Detector
	response        *dialogue.Manager
	outboundStarted sync.Once
	inboundOnce     sync.Once
	asrConfig       asr.Config
	asrContext      context.Context
	asrCancel       context.CancelCauseFunc
	asrInput        chan []byte
	asrDone         chan struct{}
	asrStarted      sync.Once
}

func NewSession(api *webrtc.API, asrConfig asr.Config, model dialogue.LanguageModel, speech dialogue.SpeechSynthesizer) (*Session, error) {
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

	asrContext, asrCancel := context.WithCancelCause(context.Background())
	session := &Session{
		PeerConnection: peerConnection,
		OutboundTrack:  outboundTrack,
		stop:           make(chan struct{}),
		vad:            interrupt.NewDetector(700, 200*time.Millisecond, 500*time.Millisecond, 20*time.Millisecond),
		asrConfig:      asrConfig,
		asrContext:     asrContext,
		asrCancel:      asrCancel,
		asrInput:       make(chan []byte, 100),
		asrDone:        make(chan struct{}),
	}
	session.response = dialogue.New(model, speech, func(event dialogue.Event) { session.sendEvent(event) })

	peerConnection.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		log.Printf("inbound track: kind=%s codec=%s/%d", track.Kind(), track.Codec().MimeType, track.Codec().ClockRate)
		session.inboundOnce.Do(func() { session.readInbound(track) })
	})
	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("peer connection state: %s", state)
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			session.Close()
		}
	})
	peerConnection.OnDataChannel(func(channel *webrtc.DataChannel) {
		if channel.Label() != "control" {
			_ = channel.Close()
			return
		}
		session.controlMu.Lock()
		session.control = channel
		session.controlMu.Unlock()
		log.Printf("data channel: label=%s", channel.Label())
		channel.OnOpen(func() {
			log.Printf("data channel open: label=%s", channel.Label())
			session.response.Ready()
			session.asrStarted.Do(func() { go session.runASR() })
		})
		channel.OnMessage(func(message webrtc.DataChannelMessage) {
			var command struct {
				Event string `json:"event"`
			}
			if json.Unmarshal(message.Data, &command) == nil {
				switch command.Event {
				case "disconnect":
					go session.Close()
				case "stop_response":
					session.response.Stop()
				}
			}
		})
		channel.OnClose(func() {
			log.Printf("data channel closed: label=%s", channel.Label())
			session.controlMu.Lock()
			if session.control == channel {
				session.control = nil
			}
			session.controlMu.Unlock()
			session.asrCancel(context.Canceled)
			session.response.Close()
		})
	})

	go session.writeOutbound()
	return session, nil
}

func (s *Session) readInbound(track *webrtc.TrackRemote) {
	defer close(s.asrInput)
	var packets uint64
	for {
		packet, _, err := track.ReadRTP()
		if err != nil {
			log.Printf("inbound track ended after %d packets: %v", packets, err)
			return
		}
		packets++
		if track.Codec().MimeType == webrtc.MimeTypePCMU {
			samples := audio.DecodePCMU(packet.Payload)
			rms := audio.RMS(samples)
			if s.asrConfig.Enabled() && s.asrContext.Err() == nil {
				s.enqueueASR(audio.PCM16LE(samples))
			}
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
	s.sendEvent(struct {
		Event string  `json:"event"`
		RMS   float64 `json:"rms"`
	}{Event: event, RMS: rms})
}

func (s *Session) sendEvent(event any) {
	message, err := json.Marshal(event)
	if err != nil {
		return
	}
	s.controlMu.RLock()
	channel := s.control
	s.controlMu.RUnlock()
	if channel != nil && channel.ReadyState() == webrtc.DataChannelStateOpen {
		if err := channel.SendText(string(message)); err != nil {
			log.Printf("send control event: %v", err)
		}
	}
}

func (s *Session) runASR() {
	defer close(s.asrDone)
	if !s.asrConfig.Enabled() {
		s.sendEvent(asr.Event{Event: "asr_status", Status: "unconfigured"})
		return
	}
	s.sendEvent(asr.Event{Event: "asr_status", Status: "connecting"})
	err := asr.Run(s.asrContext, s.asrConfig, s.asrInput, func(event asr.Event) {
		s.sendEvent(event)
		s.response.Accept(event)
	})
	if cause := context.Cause(s.asrContext); cause != nil {
		err = cause
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	if err != nil {
		log.Printf("ASR stopped: %v", err)
		status := "failed"
		if errors.Is(err, asr.ErrAudioBacklog) {
			status = "backlog"
		}
		s.sendEvent(asr.Event{Event: "asr_error", Status: status})
		return
	}
	s.sendEvent(asr.Event{Event: "asr_status", Status: "stopped"})
}

// Cloud writes must never stall microphone RTP consumption or VAD processing.
func (s *Session) enqueueASR(data []byte) {
	select {
	case <-s.asrDone:
		return
	default:
	}
	select {
	case s.asrInput <- data:
	case <-s.asrContext.Done():
	default:
		s.asrCancel(asr.ErrAudioBacklog)
	}
}

func (s *Session) writeOutbound() {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	var sequence uint16
	var timestamp uint32
	for {
		select {
		case <-ticker.C:
			if err := s.response.WriteFrame(func(payload []byte) error {
				return s.OutboundTrack.WriteRTP(&rtp.Packet{
					Header:  rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: sequence, Timestamp: timestamp},
					Payload: payload,
				})
			}); err != nil {
				// The track is unbound until SDP negotiation finishes. Keep the
				// clock running so the first bound writer receives fresh packets.
				continue
			}
			s.outboundStarted.Do(func() {
				log.Printf("outbound audio started: codec=audio/PCMU/8000 payload_bytes=%d", pcmuFrameSamples)
			})
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
		s.asrCancel(context.Canceled)
		s.response.Close()
		if err := s.PeerConnection.Close(); err != nil {
			log.Printf("close peer connection: %v", err)
		}
	})
}
