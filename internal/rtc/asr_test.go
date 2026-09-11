package rtc

import (
	"context"
	"errors"
	"testing"
	"time"

	"webrtc-interrupt/internal/asr"
)

func TestSlowASRDoesNotBlockMicrophone(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	session := &Session{asrContext: ctx, asrCancel: cancel, asrInput: make(chan []byte, 1), asrDone: make(chan struct{})}
	session.enqueueASR([]byte{0, 0})
	done := make(chan struct{})
	go func() { session.enqueueASR([]byte{1, 0}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("microphone blocked on a full ASR queue")
	}
	if !errors.Is(context.Cause(ctx), asr.ErrAudioBacklog) {
		t.Fatal("backpressure did not report ASR backlog")
	}
}
