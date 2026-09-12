package tts

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"webrtc-interrupt/internal/audio"
)

func streamFixture(t *testing.T, serve func(*websocket.Conn, string)) *Client {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("Action") != "TextToStreamAudioWS" || q.Get("SampleRate") != "8000" || q.Get("Codec") != "pcm" || q.Get("VoiceType") != "603002" || q.Get("Signature") == "" {
			t.Error("incorrect streaming request")
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		serve(conn, q.Get("SessionId"))
	}))
	t.Cleanup(s.Close)
	c, err := NewClient(Config{AppID: "1250000000", SecretID: "fixture-id", SecretKey: "fixture-key", VoiceType: 603002})
	if err != nil {
		t.Fatal(err)
	}
	c.endpoint = "ws" + strings.TrimPrefix(s.URL, "http") + "/stream_ws"
	return c
}

func TestStreamingAudioArrivesBeforeFinalAndPreservesSamples(t *testing.T) {
	received := make(chan struct{}, 1)
	pcm := audio.PCM16LE([]int16{-32768, 32767, -1000, 1000, 0})
	c := streamFixture(t, func(conn *websocket.Conn, id string) {
		_ = conn.WriteJSON(map[string]any{"code": 0, "session_id": id, "final": 0})
		for _, chunk := range [][]byte{pcm[:1], pcm[1:3], pcm[3:7], pcm[7:]} {
			_ = conn.WriteMessage(websocket.BinaryMessage, chunk)
		}
		select {
		case <-received:
		case <-time.After(time.Second):
			t.Error("client waited for final before emitting audio")
		}
		_ = conn.WriteJSON(map[string]any{"code": 0, "session_id": id, "final": 1})
	})
	var result []byte
	err := c.Stream(context.Background(), "\u4f60\u597d & +?", func(chunk []byte) error {
		result = append(result, chunk...)
		select {
		case received <- struct{}{}:
		default:
		}
		return nil
	})
	if err != nil || !bytes.Equal(result, audio.EncodePCMU([]int16{-32768, 32767, -1000, 1000, 0})) {
		t.Fatalf("chunk PCM sample boundaries corrupted: %v", err)
	}
}

func TestStreamRejectsInvalidCompletion(t *testing.T) {
	for _, scenario := range []string{"odd", "empty", "missing_final", "wrong_session", "before_ack", "cloud_error"} {
		t.Run(scenario, func(t *testing.T) {
			c := streamFixture(t, func(conn *websocket.Conn, id string) {
				if scenario == "before_ack" {
					_ = conn.WriteMessage(websocket.BinaryMessage, []byte{1, 2})
					return
				}
				if scenario == "wrong_session" {
					id = "wrong"
				}
				code := 0
				if scenario == "cloud_error" {
					code = 10003
				}
				_ = conn.WriteJSON(map[string]any{"code": code, "session_id": id, "message": "fixture-secret-key"})
				if scenario == "missing_final" {
					_ = conn.WriteMessage(websocket.BinaryMessage, []byte{1, 2})
					return
				}
				if scenario == "odd" {
					_ = conn.WriteMessage(websocket.BinaryMessage, []byte{1})
				}
				_ = conn.WriteJSON(map[string]any{"code": 0, "session_id": id, "final": 1})
			})
			err := c.Stream(context.Background(), "hello", func([]byte) error { return nil })
			if err == nil || strings.Contains(err.Error(), "fixture-secret-key") {
				t.Fatalf("missing or unsafe error: %v", err)
			}
		})
	}
}

func TestStreamCancellationClosesProviderDuringReadAndQueueWait(t *testing.T) {
	for _, blockedCallback := range []bool{false, true} {
		closed := make(chan struct{})
		started := make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		c := streamFixture(t, func(conn *websocket.Conn, id string) {
			_ = conn.WriteJSON(map[string]any{"code": 0, "session_id": id})
			if blockedCallback {
				_ = conn.WriteMessage(websocket.BinaryMessage, []byte{1, 2})
			} else {
				close(started)
			}
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			_, _, _ = conn.ReadMessage()
			close(closed)
		})
		done := make(chan error, 1)
		go func() {
			done <- c.Stream(ctx, "hello", func([]byte) error { close(started); <-ctx.Done(); return ctx.Err() })
		}()
		<-started
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancellation left streaming client blocked")
		}
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("provider socket stayed open")
		}
	}
}
