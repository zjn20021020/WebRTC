package home

import (
	"context"
	"errors"
	"testing"
)

func TestVoiceActionsRepeatAndHonorCancellation(t *testing.T) {
	for _, d := range Definitions() {
		if d.Name == GeneralQA {
			continue
		}
		t.Run(string(d.Name), func(t *testing.T) {
			calls := 0
			err := (VoiceExecutor{}).Execute(context.Background(), Request{Call: Call{Name: d.Name}}, func(text string) error {
				calls++
				if text != d.Speech {
					t.Error("wrong action speech")
				}
				return nil
			})
			if err != nil || calls != 10 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls = 0
			err = (VoiceExecutor{}).Execute(ctx, Request{Call: Call{Name: d.Name}}, func(string) error { calls++; cancel(); return nil })
			if !errors.Is(err, context.Canceled) || calls != 1 {
				t.Fatal("tool continued after cancellation")
			}
		})
	}
}
