package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"webrtc-interrupt/internal/home"
)

func toolResponse(name home.Action, args, content, finish string) []byte {
	data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": finish, "message": map[string]any{
		"content": content, "tool_calls": []any{map[string]any{"id": "call-1", "type": "function", "function": map[string]any{"name": name, "arguments": args}}},
	}}}})
	return data
}

func TestActionProtocolRejectsInvalidCalls(t *testing.T) {
	for _, d := range home.Definitions() {
		call, err := parseAction(toolResponse(d.Name, "{ }", "", "tool_calls"))
		if err != nil || call.Name != d.Name {
			t.Fatalf("%s: %v", d.Name, err)
		}
	}
	for _, data := range [][]byte{
		toolResponse(home.Water, `{}`, "Okay", "tool_calls"),
		toolResponse(home.Water, `{}`, "", "length"),
		toolResponse("delete_home", `{}`, "", "tool_calls"),
		toolResponse(home.Water, `{"times":10}`, "", "tool_calls"),
		toolResponse(home.Affection, `{"additionalProperties":{}}`, "", "tool_calls"),
		toolResponse(home.Water, `{"crop":"a","crop":"b"}`, "", "tool_calls"),
		toolResponse(home.Water, `null`, "", "tool_calls"),
		toolResponse(home.Water, `{} {}`, "", "tool_calls"),
		toolResponse(home.Water, "```json\n{}\n```", "", "tool_calls"),
		[]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"tool_calls\":[]}"}}]}`),
		[]byte(`{"choices":[],"error":{"message":"private"}}`),
	} {
		if _, err := parseAction(data); !errors.Is(err, ErrInvalidActionResult) {
			t.Fatalf("invalid call accepted: %s", data)
		}
	}
	for _, mutation := range []func(map[string]any){
		func(m map[string]any) { m["refusal"] = "refused" },
		func(m map[string]any) { m["tool_calls"] = append(m["tool_calls"].([]any), m["tool_calls"].([]any)[0]) },
		func(m map[string]any) { m["tool_calls"].([]any)[0].(map[string]any)["id"] = "" },
	} {
		var response map[string]any
		_ = json.Unmarshal(toolResponse(home.Water, "{}", "", "tool_calls"), &response)
		mutation(response["choices"].([]any)[0].(map[string]any)["message"].(map[string]any))
		data, _ := json.Marshal(response)
		if _, err := parseAction(data); !errors.Is(err, ErrInvalidActionResult) {
			t.Fatalf("invalid envelope accepted: %s", data)
		}
	}
}

type actionTransportFunc func(*http.Request) (*http.Response, error)

func (f actionTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestActionStrictSchemaRoutingAndValidation(t *testing.T) {
	for _, tc := range []struct {
		base, path string
		strict     bool
	}{
		{"https://api.deepseek.com", "/beta/chat/completions", true},
		{"https://api.deepseek.com/v1/", "/beta/chat/completions", true},
		{"https://api.deepseek.com/beta/chat/completions", "/beta/chat/completions", true},
		{"https://gateway.example/v1", "/v1/chat/completions", false},
		{"https://api.deepseek.com/proxy", "/proxy/chat/completions", false},
	} {
		t.Run(tc.base, func(t *testing.T) {
			c, err := NewClient(Config{APIKey: "fixture", BaseURL: tc.base, Model: DefaultModel})
			if err != nil {
				t.Fatal(err)
			}
			original := c.address
			c.http = &http.Client{Transport: actionTransportFunc(func(r *http.Request) (*http.Response, error) {
				var body struct {
					Tools []functionTool `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if r.URL.Path != tc.path || len(body.Tools) != 6 {
					t.Fatalf("wrong action route or tools: %s", r.URL)
				}
				for _, tool := range body.Tools {
					p := tool.Function.Parameters
					if tool.Function.Strict != tc.strict || p["type"] != "object" || p["additionalProperties"] != false || len(p["properties"].(map[string]any)) != 0 || len(p["required"].([]any)) != 0 {
						t.Fatal("incorrect strict empty-object schema")
					}
				}
				// Provider strict mode does not remove our execution-boundary validation.
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(toolResponse(home.Affection, `{"additionalProperties":{}}`, "", "tool_calls"))))}, nil
			})}
			_, err = c.ClassifyAction(context.Background(), ActionInput{UserText: "干得不错。"})
			if ActionValidationReason(err) != "nonempty_arguments" || c.address != original {
				t.Fatalf("strict output bypassed validation or changed the shared endpoint: %v", err)
			}
		})
	}
}

func TestActionRequestAndCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools      []functionTool    `json:"tools"`
			ToolChoice string            `json:"tool_choice"`
			Messages   []Message         `json:"messages"`
			Thinking   map[string]string `json:"thinking"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Tools) != 6 || request.ToolChoice != "required" || request.Thinking["type"] != "disabled" {
			t.Error("invalid native function request")
		}
		if len(request.Messages) != 2 || !strings.Contains(request.Messages[0].Content, "action classifier") || strings.Contains(request.Messages[0].Content, home.RoleSkill) {
			t.Error("classifier must use classification instructions without the speaking role skill")
		}
		var input ActionInput
		_ = json.Unmarshal([]byte(request.Messages[1].Content), &input)
		if input.UserText == "repair" {
			if !strings.Contains(request.Messages[0].Content, "Protocol repair") || strings.Contains(request.Messages[1].Content, "Repair") {
				t.Error("repair instruction is missing or leaked into user data")
			}
		}
		if input.UserText == "wait" {
			w.Header().Set("Content-Type", "application/json")
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
			return
		}
		_, _ = w.Write(toolResponse(home.Water, "{}", "", "tool_calls"))
	}))
	defer server.Close()
	c, _ := NewClient(Config{APIKey: "fixture", BaseURL: server.URL, Model: DefaultModel})
	c.http = server.Client()
	if call, err := c.ClassifyAction(context.Background(), ActionInput{UserText: "water"}); err != nil || call.Name != home.Water {
		t.Fatalf("%v %v", call, err)
	}
	if _, err := c.ClassifyAction(context.Background(), ActionInput{UserText: "repair", Repair: true}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.ClassifyAction(ctx, ActionInput{UserText: "wait"}); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("action request ignored cancellation")
	}
}

func TestActionValidationDiagnosticsDoNotExposeResponseText(t *testing.T) {
	for _, tc := range []struct {
		data   []byte
		reason string
	}{
		{toolResponse(home.Fertilize, "{}", "private-provider-prose", "tool_calls"), "unexpected_content"},
		{toolResponse(home.Fertilize, "{}", "", "length"), "truncated"},
		{toolResponse(home.Fertilize, "{}", "", "stop"), "no_tool_finish"},
		{toolResponse(home.Fertilize, `{"secret":"private-argument"}`, "", "tool_calls"), "nonempty_arguments"},
		{toolResponse("private-unknown-function", "{}", "", "tool_calls"), "unknown_tool"},
	} {
		_, err := parseAction(tc.data)
		if !errors.Is(err, ErrInvalidActionResult) || ActionValidationReason(err) != tc.reason || strings.Contains(err.Error(), "private") {
			t.Fatalf("unsafe or missing diagnostic: %v", err)
		}
	}
}
