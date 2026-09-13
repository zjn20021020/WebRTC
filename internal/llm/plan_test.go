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

	"webrtc-interrupt/internal/home"
)

func planResponse(args string) []byte { return toolResponse("execute_plan", args, "", "tool_calls") }

func TestPlanValidatesAllStepsBeforeReturning(t *testing.T) {
	valid := `{"steps":[{"text":"先种菜","action":"plant"},{"action":"water","text":"浇水"},{"action":"general_qa","text":"一加一等于几？"}]}`
	plan, err := parsePlan(planResponse(valid))
	if err != nil || len(plan.Steps) != 3 || plan.Steps[0].Action != home.Plant || plan.Steps[1].Action != home.Water || plan.Steps[2].Text != "一加一等于几？" {
		t.Fatalf("wrong ordered plan: %+v %v", plan, err)
	}
	for _, args := range []string{
		`{"steps":[]}`, `{"steps":null}`,
		`{"steps":[{"action":"water","text":"浇水"},{"action":"delete_home","text":"删除"}]}`,
		`{"steps":[{"action":"water","text":"浇水","extra":1}]}`,
		`{"steps":[{"action":"water","action":"plant","text":"种菜"}]}`,
		`{"steps":[{"action":"water"}]}`,
		`{"steps":[{"action":"water","text":null}]}`,
		`{"steps":[{"action":"water","text":"   "}]}`,
		`{"steps":[{"action":"water","text":"浇水"}],"steps":[]}`,
		valid + `{}`, "好的，" + valid,
		`{"steps":[` + strings.Repeat(`{"action":"water","text":"浇水"},`, 6) + `{"action":"water","text":"浇水"}]}`,
	} {
		if got, err := parsePlan(planResponse(args)); !errors.Is(err, ErrInvalidActionResult) || len(got.Steps) != 0 {
			t.Errorf("accepted invalid or partial plan: %s => %+v %v", args, got, err)
		}
	}
}

func TestPlanRejectsRefusalAndMultipleNativeCalls(t *testing.T) {
	for _, mutate := range []func(map[string]any){
		func(m map[string]any) { m["refusal"] = "refused" },
		func(m map[string]any) { m["content"] = "好的" },
		func(m map[string]any) { m["tool_calls"] = append(m["tool_calls"].([]any), m["tool_calls"].([]any)[0]) },
	} {
		var value map[string]any
		_ = json.Unmarshal(planResponse(`{"steps":[{"action":"water","text":"浇水"}]}`), &value)
		mutate(value["choices"].([]any)[0].(map[string]any)["message"].(map[string]any))
		data, _ := json.Marshal(value)
		if _, err := parsePlan(data); err == nil {
			t.Fatal("accepted invalid native plan")
		}
	}
}

func TestPlanRequestSchemaAndCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools    []functionTool `json:"tools"`
			Messages []Message      `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Tools) != 1 || request.Tools[0].Function.Name != "execute_plan" {
			t.Error("missing ordered plan tool")
		}
		var input ActionInput
		_ = json.Unmarshal([]byte(request.Messages[1].Content), &input)
		if input.UserText == "wait" {
			w.Header().Set("Content-Type", "application/json")
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
			return
		}
		_, _ = w.Write(planResponse(`{"steps":[{"action":"water","text":"浇水"}]}`))
	}))
	defer server.Close()
	c, _ := NewClient(Config{APIKey: "fixture", BaseURL: server.URL, Model: DefaultModel})
	c.http = server.Client()
	if plan, err := c.PlanActions(context.Background(), ActionInput{UserText: "浇水"}); err != nil || len(plan.Steps) != 1 {
		t.Fatalf("%+v %v", plan, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.PlanActions(ctx, ActionInput{UserText: "wait"}); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("plan HTTP did not cancel")
	}
}
