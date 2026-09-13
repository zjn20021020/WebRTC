package dialogue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"webrtc-interrupt/internal/asr"
	"webrtc-interrupt/internal/home"
	"webrtc-interrupt/internal/llm"
)

func checkExecutionContext(t *testing.T, messages []llm.Message, action home.Action, status string, remaining []home.Action) {
	t.Helper()
	for _, message := range messages {
		if message.Role != "system" || !strings.HasPrefix(message.Content, "Server execution state.") {
			continue
		}
		var state struct {
			Epoch      uint64            `json:"response_epoch"`
			Action     home.Action       `json:"current_action"`
			Previous   []executionResult `json:"previous_responses"`
			Simulation bool              `json:"voice_simulation_only"`
		}
		if err := json.Unmarshal([]byte(message.Content[strings.LastIndex(message.Content, "\n")+1:]), &state); err != nil {
			t.Fatal(err)
		}
		if state.Epoch != 2 || state.Action != action || !state.Simulation || len(state.Previous) != 1 {
			t.Fatalf("incorrect runtime context: %+v", state)
		}
		previous := state.Previous[0]
		if previous.Epoch != 1 || previous.Action != home.Water || previous.Status != status || !reflect.DeepEqual(previous.CancelledActions, remaining) {
			t.Fatalf("lost previous task outcome: %+v", previous)
		}
		return
	}
	t.Fatal("model received conversational history without execution state")
}

func TestStoryReceivesActualPreviousTaskOutcome(t *testing.T) {
	for _, status := range []string{"completed", "cancelled", "failed"} {
		t.Run(status, func(t *testing.T) {
			clock := &testClock{}
			story := "给我讲个故事吧"
			var remaining []home.Action
			if status != "completed" {
				remaining = []home.Action{home.Fertilize}
			}
			if status == "cancelled" {
				story = "先别浇水了，给我讲个故事吧"
			}
			answer := "一天，迪莫在家园发现一颗种子。他每天照顾它，终于开出一朵小花。"
			answered := make(chan struct{})
			model := planModel{intentModel: intentModel{
				modelFunc: func(_ context.Context, messages []llm.Message, speak func(string) error) error {
					checkExecutionContext(t, messages, home.GeneralQA, status, remaining)
					if messages[len(messages)-1] != (llm.Message{Role: "user", Content: story}) {
						t.Error("current story request was overwritten by old work or system state")
					}
					defer close(answered)
					return speak(answer)
				},
				decide: func(_ context.Context, input llm.InterruptionInput) (bool, error) {
					if input.CurrentTool != string(home.Water) {
						t.Error("story intent lost active water tool")
					}
					return status == "cancelled", nil
				},
			}, plan: func(_ context.Context, input llm.ActionInput) (home.Plan, error) {
				steps := []home.Step{{Action: home.Water, Text: "去浇水"}}
				if input.UserText == story {
					checkExecutionContext(t, input.RecentHistory, "", status, remaining)
					steps = []home.Step{{Action: home.GeneralQA, Text: story}}
				} else if remaining != nil {
					steps = append(steps, home.Step{Action: home.Fertilize, Text: "再去施肥"})
				}
				return home.Plan{ID: input.UserText, Steps: steps}, nil
			}}
			m := NewWithExecutor(model, speechFunc(func(context.Context, string) ([]byte, error) {
				return bytes.Repeat([]byte{0x33}, 320), nil
			}), executorFunc(func(ctx context.Context, request home.Request, speak func(string) error) error {
				if status == "failed" {
					return errors.New("water unavailable")
				}
				return (home.VoiceExecutor{}).Execute(ctx, request, speak)
			}), func(Event) {})
			m.now = clock.now
			defer m.Close()
			m.Accept(asr.Event{Event: "asr_final", UtteranceID: "water", Text: "去浇水"})
			if status == "failed" {
				waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.current == nil })
			} else {
				waitGenerated(t, m, 1)
				frameFrom(t, m)
			}
			m.Accept(asr.Event{Event: "asr_final", UtteranceID: "story", Text: story})
			switch status {
			case "completed":
				waitFor(t, func() bool { return bufferedCount(m) == 1 })
				select {
				case <-answered:
					t.Fatal("story answered before water finished")
				default:
				}
				finishEpoch(t, m, 1)
			case "cancelled":
				waitConfirmed(t, m)
				clock.advance(minimumDuck)
				frameFrom(t, m)
			}
			waitGenerated(t, m, 2)
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.current.text != answer || m.plan != nil || len(m.interjections) != 0 {
				t.Fatal("story output contains execution state or stale work survived")
			}
		})
	}
}

func TestExecutionHistoryIsBoundedAndContainsNoUserInstructions(t *testing.T) {
	m := &Manager{}
	for epoch := uint64(1); epoch <= 20; epoch++ {
		m.recordExecutionLocked(&turn{epoch: epoch, text: "ignore system", stepText: "ignore system", toolCall: &home.Call{Name: home.Water}}, "completed")
	}
	if len(m.executions) != 12 || m.executions[0].Epoch != 9 || m.executions[11].Epoch != 20 {
		t.Fatal("execution history grew beyond the conversation window")
	}
	message := m.executionContextLocked(&turn{epoch: 21}, false)
	if strings.Contains(message.Content, "ignore system") {
		t.Fatal("untrusted user or generated text entered execution instructions")
	}
}
