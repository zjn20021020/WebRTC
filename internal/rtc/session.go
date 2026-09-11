package rtc

import (
	"log"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// Session owns the peer connection and the long-lived outbound audio track.
// Later ASR, TTS and turn coordination code will attach to this boundary.
type Session struct {
	PeerConnection *webrtc.PeerConnection
	OutboundTrack  *webrtc.TrackLocalStaticRTP

	closeOnce sync.Once
	stop      chan struct{}
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
		log.Printf("data channel: label=%s", channel.Label())
		channel.OnOpen(func() { log.Printf("data channel open: label=%s", channel.Label()) })
		channel.OnClose(func() { log.Printf("data channel closed: label=%s", channel.Label()) })
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
		if packets%50 == 0 {
			log.Printf("inbound audio: packets=%d sequence=%d timestamp=%d bytes=%d", packets, packet.SequenceNumber, packet.Timestamp, len(packet.Payload))
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
			packet := &rtp.Packet{
				Header:  rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: sequence, Timestamp: timestamp},
				Payload: make([]byte, 160),
			}
			for i := range packet.Payload {
				packet.Payload[i] = 0xff // PCMU silence
			}
			if err := s.OutboundTrack.WriteRTP(packet); err != nil {
				// The track is unbound until SDP negotiation finishes. Keep the
				// clock running so the first bound writer receives fresh packets.
				continue
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
