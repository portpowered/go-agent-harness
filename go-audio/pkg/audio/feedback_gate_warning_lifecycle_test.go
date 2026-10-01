package audio

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/selfhearing"
)

// confirmFeedback drives a gate until it confirms acoustic feedback, which
// starts its one-time warning write.
func confirmFeedback(t *testing.T, gate *PCM16FeedbackGate) {
	t.Helper()
	for frameIndex := range 5 {
		if err := gate.WritePlayback(context.Background(), feedbackSignal(frameIndex, 17), func() error { return nil }); err != nil {
			t.Fatalf("observe playback frame %d: %v", frameIndex, err)
		}
	}
	for frameIndex := range 5 {
		if _, err := gate.FilterCapture(context.Background(), feedbackSignal(frameIndex, 17)); err != nil {
			t.Fatalf("filter looped capture frame %d: %v", frameIndex, err)
		}
	}
	if !gate.FeedbackConfirmed() {
		t.Fatal("feedback was never confirmed")
	}
}

type slowFeedbackWarningWriter struct {
	release  <-chan struct{}
	finished atomic.Bool
}

func (w *slowFeedbackWarningWriter) Write(data []byte) (int, error) {
	<-w.release
	w.finished.Store(true)
	return len(data), nil
}

func TestFeedbackGateCloseJoinsInFlightWarningWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		writer := &slowFeedbackWarningWriter{release: release}
		gate, err := NewPCM16FeedbackGate(selfhearing.DefaultSelfHearingConfig(), writer, SampleRate, SampleRate)
		if err != nil {
			t.Fatalf("new local feedback gate: %v", err)
		}
		confirmFeedback(t, gate)
		time.AfterFunc(feedbackWarningCloseBound/2, func() { close(release) })
		if err := gate.Close(); err != nil {
			t.Fatalf("Close() = %v, want nil after the warning write finished", err)
		}
		if !writer.finished.Load() {
			t.Fatal("Close returned before the in-flight warning write finished")
		}
	})
}

func TestFeedbackGateCloseReportsBlockedWarningWriterWithinBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		gate, err := NewPCM16FeedbackGate(selfhearing.DefaultSelfHearingConfig(), &slowFeedbackWarningWriter{release: release}, SampleRate, SampleRate)
		if err != nil {
			t.Fatalf("new local feedback gate: %v", err)
		}
		confirmFeedback(t, gate)
		begin := time.Now()
		if err := gate.Close(); !errors.Is(err, ErrFeedbackWarningWriterBlocked) {
			t.Fatalf("Close() = %v, want %v", err, ErrFeedbackWarningWriterBlocked)
		}
		if elapsed := time.Since(begin); elapsed != feedbackWarningCloseBound {
			t.Fatalf("Close waited %s, want the %s bound", elapsed, feedbackWarningCloseBound)
		}
	})
}
