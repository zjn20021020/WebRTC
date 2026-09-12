package tts

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"webrtc-interrupt/internal/audio"
)

const streamEndpoint = "wss://tts.cloud.tencent.com/stream_ws"

func streamURL(config Config, address, session, text string, now time.Time) (string, error) {
	if !config.Enabled() {
		return "", errors.New("Tencent streaming TTS credentials are not configured")
	}
	if _, err := strconv.ParseUint(config.AppID, 10, 64); err != nil {
		return "", errors.New("invalid Tencent TTS AppID")
	}
	u, err := url.Parse(address)
	if err != nil || u.Host == "" || (u.Scheme != "wss" && u.Scheme != "ws") {
		return "", errors.New("invalid Tencent TTS endpoint")
	}
	q := url.Values{
		"Action": {"TextToStreamAudioWS"}, "AppId": {config.AppID}, "SecretId": {config.SecretID},
		"Timestamp": {strconv.FormatInt(now.Unix(), 10)}, "Expired": {strconv.FormatInt(now.Add(time.Hour).Unix(), 10)},
		"SessionId": {session}, "Text": {text}, "VoiceType": {strconv.FormatInt(config.VoiceType, 10)},
		"SampleRate": {"8000"}, "Codec": {"pcm"}, "Speed": {"0"}, "Volume": {"0"},
	}
	// Tencent signs the raw sorted values, including Chinese Text; only the
	// final request URL is percent-encoded. url.Values.Encode is not canonical.
	keys := make([]string, 0, len(q))
	for key := range q {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+q.Get(key))
	}
	signer := hmac.New(sha1.New, []byte(config.SecretKey))
	_, _ = signer.Write([]byte("GET" + u.Host + u.Path + "?" + strings.Join(parts, "&")))
	q.Set("Signature", base64.StdEncoding.EncodeToString(signer.Sum(nil)))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

type streamEvent struct {
	Code      *int   `json:"code"`
	SessionID string `json:"session_id"`
	Final     int    `json:"final"`
}

// Stream emits 8kHz mono PCMU as soon as PCM samples arrive. Cancelling closes
// the WebSocket even while the reader or downstream audio queue is blocked.
func (c *Client) Stream(ctx context.Context, text string, emit func([]byte) error) (resultErr error) {
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 100 {
		return errors.New("TTS segment must contain 1 to 100 characters")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			resultErr = ctx.Err()
		}
	}()
	session := uuid.NewString()
	address, err := streamURL(c.config, c.endpoint, session, text, time.Now())
	if err != nil {
		return err
	}
	dialer := websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: 10 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, address, nil)
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		if resp != nil {
			return fmt.Errorf("Tencent streaming TTS handshake failed (HTTP %d)", resp.StatusCode)
		}
		return errors.New("Tencent streaming TTS connection failed")
	}
	defer conn.Close()
	conn.SetReadLimit(2 * 1024 * 1024)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	acknowledged, received := false, false
	var pending []byte
	total := 0
	for {
		_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		kind, data, err := conn.ReadMessage()
		if err != nil {
			return errors.New("Tencent streaming TTS ended before final acknowledgement")
		}
		switch kind {
		case websocket.TextMessage:
			var event streamEvent
			if json.Unmarshal(data, &event) != nil || event.Code == nil {
				return errors.New("invalid Tencent streaming TTS response")
			}
			if *event.Code != 0 {
				return fmt.Errorf("Tencent streaming TTS failed (code %d)", *event.Code)
			}
			if event.SessionID != session {
				return errors.New("Tencent streaming TTS session mismatch")
			}
			acknowledged = true
			if event.Final == 1 {
				if !received || len(pending) != 0 {
					return errors.New("Tencent streaming TTS returned incomplete PCM16")
				}
				return nil
			}
		case websocket.BinaryMessage:
			if !acknowledged {
				return errors.New("Tencent streaming TTS audio preceded acknowledgement")
			}
			total += len(data)
			if total > 2*1024*1024 {
				return errors.New("Tencent streaming TTS audio exceeds segment limit")
			}
			pending = append(pending, data...)
			if !received && bytesAreWAV(pending) {
				return errors.New("Tencent streaming TTS returned a WAV header instead of PCM")
			}
			n := len(pending) / 2
			if n == 0 {
				continue
			}
			samples := make([]int16, n)
			for i := range samples {
				samples[i] = int16(binary.LittleEndian.Uint16(pending[i*2:]))
			}
			if err := emit(audio.EncodePCMU(samples)); err != nil {
				return err
			}
			received = true
			pending = append(pending[:0], pending[n*2:]...)
		default:
			return errors.New("unexpected Tencent streaming TTS message")
		}
	}
}
