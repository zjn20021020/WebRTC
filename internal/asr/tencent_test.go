package asr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"webrtc-interrupt/internal/audio"
)

func testConfig() Config {
	return Config{AppID: "1250000000", SecretID: "AKIDexample", SecretKey: "test-secret-key", Model: "8k_zh"}
}

func TestConfigRequiresAllCredentials(t *testing.T) {
	t.Setenv("TENCENT_APP_ID", "1250000000")
	t.Setenv("TENCENT_SECRET_ID", "AKIDexample")
	t.Setenv("TENCENT_SECRET_KEY", "")
	if ConfigFromEnv().Enabled() {
		t.Fatal("partially configured credentials must not enable ASR")
	}
	t.Setenv("TENCENT_SECRET_KEY", "test-secret-key")
	config := ConfigFromEnv()
	if !config.Enabled() || config.Model != "8k_zh" {
		t.Fatal("Tencent 8kHz engine was not selected")
	}
}

func serveProvider(t *testing.T, handler func(*websocket.Conn, *http.Request) error) (string, <-chan error) {
	t.Helper()
	done := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			done <- err
			return
		}
		defer connection.Close()
		_ = connection.SetReadDeadline(time.Now().Add(3 * time.Second))
		done <- handler(connection, r)
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http"), done
}

func TestTencentStreamingProtocol(t *testing.T) {
	pcm := make([]int16, 3360) // 420ms, including a final partial packet.
	for i := range pcm {
		if i%2 == 0 {
			pcm[i] = -32768
		} else {
			pcm[i] = 32767
		}
	}
	wantAudio := audio.PCM16LE(pcm)
	address, providerDone := serveProvider(t, func(connection *websocket.Conn, request *http.Request) error {
		q := request.URL.Query()
		if request.URL.Path != "/asr/v2/1250000000" || q.Get("engine_model_type") != "8k_zh" || q.Get("voice_format") != "1" || q.Get("filter_modal") != "0" || q.Get("signature") == "" {
			return errors.New("unexpected handshake parameters")
		}
		voiceID := q.Get("voice_id")
		if err := connection.WriteJSON(map[string]any{"code": 0, "voice_id": voiceID}); err != nil {
			return err
		}
		var received []byte
		var arrivals []time.Time
		for {
			kind, data, err := connection.ReadMessage()
			if err != nil {
				return err
			}
			if kind == websocket.TextMessage {
				var end map[string]string
				if json.Unmarshal(data, &end) != nil || end["type"] != "end" {
					return errors.New("missing end command")
				}
				break
			}
			if kind != websocket.BinaryMessage {
				return errors.New("audio must be binary")
			}
			arrivals = append(arrivals, time.Now())
			if len(data) > 3200 {
				return errors.New("audio packet exceeds 200ms")
			}
			received = append(received, data...)
			if len(arrivals) == 1 {
				if err := connection.WriteJSON(map[string]any{
					"code": 0, "voice_id": voiceID,
					"result": map[string]any{"slice_type": 1, "index": 0, "voice_text_str": "wait", "start_time": 20},
				}); err != nil {
					return err
				}
			}
		}
		if !bytes.Equal(received, wantAudio) {
			return errors.New("PCM bytes were lost or reordered")
		}
		if len(arrivals) != 3 || arrivals[2].Sub(arrivals[0]) < 350*time.Millisecond {
			return errors.New("audio was sent faster than real time")
		}
		for _, message := range []map[string]any{
			{"code": 0, "voice_id": "stale-voice", "result": map[string]any{"slice_type": 2, "voice_text_str": "stale"}},
			{"code": 0, "voice_id": voiceID, "result": map[string]any{"slice_type": 2, "index": 0, "voice_text_str": "wait please", "start_time": 20, "end_time": 420}},
			{"code": 0, "voice_id": voiceID, "final": 1},
		} {
			if err := connection.WriteJSON(message); err != nil {
				return err
			}
		}
		return nil
	})
	input := make(chan []byte, 21)
	for i := 0; i < len(wantAudio); i += 320 {
		input <- wantAudio[i : i+320]
	}
	close(input)
	var events []Event
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := run(ctx, testConfig(), address, input, func(event Event) { events = append(events, event) }); err != nil {
		t.Fatal(err)
	}
	if err := <-providerDone; err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Status != "listening" || events[1].Event != "asr_partial" || events[2].Event != "asr_final" {
		t.Fatalf("unexpected events: %+v", events)
	}
	if events[1].UtteranceID != events[2].UtteranceID || events[2].Text != "wait please" {
		t.Fatalf("sentence identity changed: %+v", events)
	}
}

func TestCancellationClosesProviderConnection(t *testing.T) {
	address, providerDone := serveProvider(t, func(connection *websocket.Conn, request *http.Request) error {
		if err := connection.WriteJSON(map[string]any{"code": 0, "voice_id": request.URL.Query().Get("voice_id")}); err != nil {
			return err
		}
		_, _, err := connection.ReadMessage()
		if err == nil {
			return errors.New("cancelled stream still sending data")
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := run(ctx, testConfig(), address, make(chan []byte), func(Event) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context cancelled", err)
	}
	if err := <-providerDone; err != nil {
		t.Fatal(err)
	}
}

func TestProviderRejectionDoesNotLeakCredentials(t *testing.T) {
	address, providerDone := serveProvider(t, func(connection *websocket.Conn, request *http.Request) error {
		return connection.WriteJSON(map[string]any{"code": 4001, "voice_id": request.URL.Query().Get("voice_id"), "message": "test-secret-key"})
	})
	err := run(context.Background(), testConfig(), address, make(chan []byte), func(Event) { t.Error("rejected task emitted a result") })
	if err == nil || !strings.Contains(err.Error(), "4001") || strings.Contains(err.Error(), "test-secret-key") {
		t.Fatalf("unsafe or missing error: %v", err)
	}
	if err := <-providerDone; err != nil {
		t.Fatal(err)
	}
}

func TestSignedURLRejectsWrongSampleRate(t *testing.T) {
	config := testConfig()
	config.Model = "16k_zh"
	if _, err := signedURL(config, endpoint, "voice-id", time.Now(), "1"); err == nil {
		t.Fatal("16kHz engine accepted 8kHz PCM")
	}
}

func TestSignedURLCanonicalQuery(t *testing.T) {
	address, err := signedURL(testConfig(), endpoint, "00000000-0000-0000-0000-000000000001", time.Unix(1700000000, 0), "12345")
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	if target.Scheme != "wss" || target.Host != "asr.cloud.tencent.com" {
		t.Fatalf("unexpected URL host: %s", target.Host)
	}
	query := target.Query()
	if query.Get("expired") != "1700003600" || query.Get("timestamp") != "1700000000" {
		t.Fatal("incorrect signature lifetime")
	}
	if got := query.Get("signature"); got != "bfavSghfdtywoxv23plHl9uJ36o=" {
		t.Fatalf("unexpected HMAC-SHA1 signature: %s", got)
	}
	if !strings.Contains(target.RawQuery, "%3D") {
		t.Fatal("signature padding was not URL-encoded")
	}
}
