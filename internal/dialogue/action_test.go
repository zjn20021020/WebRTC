package dialogue

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"webrtc-interrupt/internal/asr"
	"webrtc-interrupt/internal/home"
	"webrtc-interrupt/internal/llm"
)

type actionModel struct {
	intentModel
	classify func(context.Context, llm.ActionInput) (home.Call, error)
}

func (m actionModel) ClassifyAction(ctx context.Context, input llm.ActionInput) (home.Call, error) {
	return m.classify(ctx, input)
}

type executorFunc func(context.Context, home.Request, func(string) error) error

func (f executorFunc) Execute(ctx context.Context, r home.Request, speak func(string) error) error {
	return f(ctx, r, speak)
}

func TestHomeToolCancellationThenDeferredAffection(t *testing.T) {
	var mu sync.Mutex
	var events []Event
	waterStarted, waterStopped := make(chan struct{}), make(chan struct{})
	model := actionModel{intentModel: intentModel{
		modelFunc: func(context.Context, []llm.Message, func(string) error) error {
			t.Error("action called general answer LLM")
			return nil
		},
		decide: func(_ context.Context, input llm.InterruptionInput) (bool, error) {
			if input.CurrentTool == "" {
				t.Error("active tool missing from interruption context")
			}
			return strings.Contains(input.UserText, "施肥"), nil
		},
	}, classify: func(_ context.Context, input llm.ActionInput) (home.Call, error) {
		action := home.Water
		if strings.Contains(input.UserText, "施肥") {
			action = home.Fertilize
		}
		if strings.Contains(input.UserText, "真棒") {
			action = home.Affection
		}
		return home.Call{ID: input.UserText, Name: action}, nil
	}}
	executor := executorFunc(func(ctx context.Context, r home.Request, speak func(string) error) error {
		if r.Call.Name != home.Water {
			return (home.VoiceExecutor{}).Execute(ctx, r, speak)
		}
		if err := speak("我正在浇水。"); err != nil {
			return err
		}
		close(waterStarted)
		<-ctx.Done()
		defer close(waterStopped)
		if err := speak("不应播放。"); !errors.Is(err, context.Canceled) {
			t.Error("late tool output accepted")
		}
		return ctx.Err()
	})
	m := NewWithExecutor(model, speechFunc(func(context.Context, string) ([]byte, error) { return bytes.Repeat([]byte{0x33}, 320), nil }), executor, func(e Event) { mu.Lock(); events = append(events, e); mu.Unlock() })
	defer m.Close()
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "water", Text: "迪莫去浇水"})
	<-waterStarted
	waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return len(m.current.frames) > 0 })
	frameFrom(t, m)
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "fertilize", Text: "别浇水了去施肥"})
	waitConfirmed(t, m)
	time.Sleep(minimumDuck)
	frameFrom(t, m)
	select {
	case <-waterStopped:
	case <-time.After(time.Second):
		t.Fatal("tool context not cancelled")
	}
	waitFor(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.current != nil && m.current.epoch == 2 && m.current.generated
	})
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "praise", Text: "迪莫你真棒"})
	waitFor(t, func() bool { return bufferedCount(m) == 1 })
	m.mu.Lock()
	if m.current.epoch != 2 || strings.Count(m.current.text, "我正在施肥。") != 10 {
		t.Error("fertilizing was not preserved for ten repetitions")
	}
	m.mu.Unlock()
	finishEpoch(t, m, 2)
	waitFor(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.current != nil && m.current.epoch == 3 && m.current.generated
	})
	m.mu.Lock()
	if strings.Count(m.current.text, "贴贴。") != 10 {
		t.Error("affection must repeat ten times")
	}
	m.mu.Unlock()
	finishEpoch(t, m, 3)
	mu.Lock()
	defer mu.Unlock()
	completed, affection := -1, -1
	for i, e := range events {
		if e.Event != "tool_status" {
			continue
		}
		if e.Epoch == 1 && e.Status == "completed" {
			t.Error("cancelled watering completed")
		}
		if e.Epoch == 2 && e.Status == "completed" {
			completed = i
		}
		if e.Epoch == 3 && e.Status == "running" {
			affection = i
		}
	}
	if completed < 0 || affection <= completed {
		t.Fatal("affection started before fertilizing playback completed")
	}
}

func TestActionFailureClarifiesWithoutDispatch(t *testing.T) {
	for _, failure := range []error{llm.ErrInvalidActionResult, context.DeadlineExceeded, errors.New("offline")} {
		t.Run(failure.Error(), func(t *testing.T) {
			var fallback Event
			model := actionModel{classify: func(context.Context, llm.ActionInput) (home.Call, error) {
				return home.Call{ID: "bad", Name: home.Water}, failure
			}}
			m := NewWithExecutor(model, speechFunc(func(context.Context, string) ([]byte, error) { return []byte{0x33}, nil }), executorFunc(func(context.Context, home.Request, func(string) error) error {
				t.Error("fallback executed a tool")
				return nil
			}), func(e Event) {
				if e.Event == "action_result" {
					fallback = e
				}
			})
			defer m.Close()
			m.Accept(asr.Event{Event: "asr_final", UtteranceID: "request", Text: "water"})
			waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.current != nil && m.current.generated })
			m.mu.Lock()
			defer m.mu.Unlock()
			if !fallback.Fallback || fallback.Reason == "" || !strings.Contains(m.current.text, "再说一遍") {
				t.Fatal("clarification fallback not observable")
			}
		})
	}
}

func TestCancelledClassificationCannotDispatchLateTool(t *testing.T) {
	started, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	model := actionModel{classify: func(_ context.Context, input llm.ActionInput) (home.Call, error) {
		if input.UserText == "old" {
			close(started)
			<-release
			defer close(exited)
			return home.Call{ID: "old", Name: home.Water}, nil
		}
		return home.Call{ID: "new", Name: home.Harvest}, nil
	}}
	m := NewWithExecutor(model, speechFunc(func(context.Context, string) ([]byte, error) { return []byte{0x33}, nil }), executorFunc(func(ctx context.Context, r home.Request, speak func(string) error) error {
		if r.Call.Name != home.Harvest {
			t.Error("cancelled classifier dispatched a stale action")
		}
		return (home.VoiceExecutor{}).Execute(ctx, r, speak)
	}), func(Event) {})
	defer m.Close()
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "old", Text: "old"})
	<-started
	m.Stop(1)
	m.Accept(asr.Event{Event: "asr_final", UtteranceID: "new", Text: "new"})
	close(release)
	<-exited
	waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.current != nil && m.current.generated })
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current.toolCall.Name != home.Harvest || m.current.epoch != 2 {
		t.Fatal("late classification changed new turn")
	}
}
