package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStreamingRequestAndAnswer(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string            `json:"model"`
			Messages []Message         `json:"messages"`
			Stream   bool              `json:"stream"`
			Thinking map[string]string `json:"thinking"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-key" || body.Model != DefaultModel || !body.Stream || body.Thinking["type"] != "disabled" || len(body.Messages) != 1 {
			t.Error("incorrect streaming request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			": keep-alive\r\n\r\n",
			"data: {\ndata: \"choices\":[{\"delta\":{\"content\":\"Hello\",\"reasoning_content\":\"private\"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{\"content\":\" world.\"},\"finish_reason\":\"stop\"}]}\n\n",
			"data: {\"choices\":[]}\n\ndata: [DONE]\n\n",
		} {
			_, _ = w.Write([]byte(event))
			w.(http.Flusher).Flush()
		}
	}))
	defer server.Close()
	client, err := NewClient(Config{APIKey: "fixture-key", BaseURL: server.URL + "/v1/", Model: DefaultModel})
	if err != nil {
		t.Fatal(err)
	}
	client.http = server.Client()
	var answer string
	err = client.Stream(context.Background(), []Message{{Role: "user", Content: "hello"}}, func(s string) error { answer += s; return nil })
	if err != nil || answer != "Hello world." {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
}

func TestIncompleteAndInvalidStreams(t *testing.T) {
	for _, stream := range []string{
		"data: [DONE]\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"unfinished\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\n",
		"data: {\"error\":{\"message\":\"credential-from-provider\"}}\n\n",
		"data: invalid-json\n\n",
	} {
		if err := readStream(strings.NewReader(stream), func(string) error { return nil }); err == nil || strings.Contains(err.Error(), "credential-from-provider") {
			t.Fatalf("unsafe or missing stream error: %v", err)
		}
	}
}

func TestRequestCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	c, _ := NewClient(Config{APIKey: "fixture", BaseURL: server.URL, Model: DefaultModel})
	c.http = server.Client()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Stream(ctx, nil, func(string) error { return nil }) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not close stream")
	}
}

func TestConfiguredURLForms(t *testing.T) {
	for _, path := range []string{"", "/v1", "/v1/", "/chat/completions", "/v1/chat/completions"} {
		c, err := NewClient(Config{BaseURL: "https://api.deepseek.com" + path})
		if err != nil || strings.Count(c.address, "chat/completions") != 1 {
			t.Fatalf("path=%q err=%v", path, err)
		}
	}
	for _, address := range []string{"http://api.deepseek.com", "https://secret:password@api.deepseek.com", "https://api.deepseek.com?key=secret"} {
		if _, err := NewClient(Config{BaseURL: address}); err == nil {
			t.Fatal("invalid URL accepted")
		}
	}
	t.Setenv("DEEPSEEK_MODEL", "")
	t.Setenv("DEEPSEEK_URL", "")
	if c := ConfigFromEnv(); c.Model != DefaultModel || c.BaseURL != "https://api.deepseek.com" {
		t.Fatal("incorrect defaults")
	}
}
