package tts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"webrtc-interrupt/internal/audio"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixtureClient(t *testing.T, pcm []byte) *Client {
	t.Helper()
	c, err := NewClient(Config{AppID: "1250000000", SecretID: "fixture-id", SecretKey: "fixture-key", VoiceType: 1001})
	if err != nil {
		t.Fatal(err)
	}
	c.api.WithHttpTransport(transportFunc(func(r *http.Request) (*http.Response, error) {
		var req struct {
			Text, SessionId, Codec string
			SampleRate, VoiceType  int
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Codec != "pcm" || req.SampleRate != 8000 || req.VoiceType != 1001 || r.Header.Get("X-TC-Action") != "TextToVoice" || r.Header.Get("Authorization") == "" {
			t.Error("incorrect Tencent PCM request")
		}
		body, _ := json.Marshal(map[string]any{"Response": map[string]any{"Audio": base64.StdEncoding.EncodeToString(pcm), "SessionId": req.SessionId}})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
	}))
	return c
}

func TestSynthesisFormat(t *testing.T) {
	samples := []int16{-30000, -1000, 0, 1000, 30000}
	c := fixtureClient(t, audio.PCM16LE(samples))
	got, err := c.Synthesize(context.Background(), "hello")
	if err != nil || !bytes.Equal(got, audio.EncodePCMU(samples)) {
		t.Fatalf("incorrect PCM16 little-endian decoding: %v", err)
	}
}

func TestMalformedAudioRejected(t *testing.T) {
	for _, pcm := range [][]byte{nil, {1}, []byte("RIFF0000WAVE")} {
		if _, err := fixtureClient(t, pcm).Synthesize(context.Background(), "hello"); err == nil {
			t.Fatal("malformed audio accepted")
		}
	}
}

func TestCloudErrorDoesNotExposeProviderMessage(t *testing.T) {
	c := fixtureClient(t, []byte{0, 0})
	c.api.WithHttpTransport(transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"Response":{"Error":{"Code":"AuthFailure.SecretIdNotFound","Message":"fixture-secret-key"},"RequestId":"fixture"}}`))}, nil
	}))
	_, err := c.Synthesize(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "AuthFailure.SecretIdNotFound") || strings.Contains(err.Error(), "fixture-secret-key") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestSynthesisCancellation(t *testing.T) {
	c := fixtureClient(t, []byte{0, 0})
	c.api.WithHttpTransport(transportFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() }))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Synthesize(ctx, "hello"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
