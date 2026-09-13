package asr

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestV2SigningDeclaresActualAudioRate(t *testing.T) {
	config := testConfig()
	config.Model = ModelLargeV2
	address, err := signedURL(config, endpoint, "voice-id", time.Unix(1700000000, 0), "1")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(address)
	q := parsed.Query()
	for key, want := range map[string]string{"engine_model_type": ModelLargeV2, "input_sample_rate": "8000", "voice_format": "1", "result_mod": "1", "sentence_strategy": "0", "needvad": "1"} {
		if q.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, q.Get(key), want)
		}
	}
	if q.Has("vad_silence_time") || q.Has("filter_punc") {
		t.Fatal("legacy-only parameters sent to V2")
	}
	legacy, err := signedURL(testConfig(), endpoint, "voice-id", time.Unix(1700000000, 0), "1")
	if err != nil {
		t.Fatal(err)
	}
	old, _ := url.Parse(legacy)
	if old.Query().Get("signature") == q.Get("signature") {
		t.Fatal("V2 audio and model parameters were not signed")
	}
}

func TestV2SentenceSnapshotsAndPCMStreaming(t *testing.T) {
	pcm := bytes.Repeat([]byte{0x34, 0x12}, 1600)
	address, providerDone := serveProvider(t, func(connection *websocket.Conn, request *http.Request) error {
		q := request.URL.Query()
		if q.Get("engine_model_type") != ModelLargeV2 || q.Get("input_sample_rate") != "8000" || q.Get("result_mod") != "1" {
			return errors.New("V2 handshake configuration missing")
		}
		voiceID := q.Get("voice_id")
		if err := connection.WriteJSON(map[string]any{"code": 0, "voice_id": voiceID}); err != nil {
			return err
		}
		kind, received, err := connection.ReadMessage()
		if err != nil {
			return err
		}
		if kind != websocket.BinaryMessage || !bytes.Equal(received, pcm) {
			return errors.New("8kHz PCM altered or sent at the wrong chunk size")
		}
		if _, _, err := connection.ReadMessage(); err != nil {
			return err
		}
		sentence := func(id, state int, text string) map[string]any {
			return map[string]any{"sentence_id": id, "sentence_type": state, "sentence": text, "start_time": 20, "end_time": 200}
		}
		for _, snapshot := range [][]map[string]any{
			{sentence(0, 0, "water")},
			{sentence(0, 1, "water please"), sentence(1, 0, "then plant")},
			{sentence(0, 1, "water please"), sentence(1, 1, "then plant later")},
			{sentence(0, 0, "stale partial"), sentence(1, 1, "then plant later")},
		} {
			message := map[string]any{"code": 0, "voice_id": voiceID, "sentences": map[string]any{"sentence_list": snapshot}}
			if err := connection.WriteJSON(message); err != nil {
				return err
			}
		}
		return connection.WriteJSON(map[string]any{"code": 0, "voice_id": voiceID, "final": 1})
	})
	config := testConfig()
	config.Model = ModelLargeV2
	input := make(chan []byte, 1)
	input <- pcm
	close(input)
	var events []Event
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := run(ctx, config, address, input, func(event Event) { events = append(events, event) }); err != nil {
		t.Fatal(err)
	}
	if err := <-providerDone; err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 || events[0].Model != ModelLargeV2 || events[0].Status != "listening" {
		t.Fatalf("unexpected events: %+v", events)
	}
	for i, want := range []string{"asr_partial", "asr_final", "asr_partial", "asr_final"} {
		if events[i+1].Event != want || events[i+1].BeginTime != 20 || events[i+1].EndTime != 200 {
			t.Errorf("unexpected sentence event: %+v", events[i+1])
		}
	}
	if events[1].UtteranceID != events[2].UtteranceID || events[3].UtteranceID != events[4].UtteranceID || events[1].UtteranceID == events[3].UtteranceID {
		t.Fatal("sentence IDs lost identity across snapshots")
	}
}

func TestQuotaErrorIsActionableAndRedacted(t *testing.T) {
	for _, duringRecognition := range []bool{false, true} {
		address, providerDone := serveProvider(t, func(connection *websocket.Conn, request *http.Request) error {
			voiceID := request.URL.Query().Get("voice_id")
			if duringRecognition {
				if err := connection.WriteJSON(map[string]any{"code": 0, "voice_id": voiceID}); err != nil {
					return err
				}
			}
			return connection.WriteJSON(map[string]any{"code": 4004, "voice_id": voiceID, "message": "test-secret-key signed-url"})
		})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := run(ctx, testConfig(), address, make(chan []byte), func(Event) {})
		cancel()
		var provider *ProviderError
		if !errors.As(err, &provider) || provider.Code != 4004 {
			t.Fatalf("missing typed quota error: %v", err)
		}
		event := FailureEvent(err, "8k_zh")
		if event.Status != "quota_exhausted" || event.Code != 4004 || !strings.Contains(event.Detail, "资源包") || !strings.Contains(event.Detail, "8k_zh") {
			t.Fatalf("quota problem hidden: %+v", event)
		}
		if strings.Contains(err.Error()+event.Detail, "test-secret-key") || strings.Contains(err.Error()+event.Detail, "signed-url") {
			t.Fatal("provider message leaked")
		}
		if err := <-providerDone; err != nil {
			t.Fatal(err)
		}
	}
}
