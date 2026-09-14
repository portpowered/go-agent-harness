package livehost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegateway "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func TestDurationExitTranslationPreservesIndependentFailures(t *testing.T) {
	recordingFailure := errors.New("recording publication failed")
	duration := runtimeSession.ErrLiveDurationExceeded
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{name: "success"},
		{name: "duration", err: duration},
		{name: "wrapped duration", err: fmt.Errorf("session: %w", duration)},
		{name: "independent failure", err: recordingFailure, want: recordingFailure},
		{name: "joined failure", err: errors.Join(duration, recordingFailure), want: recordingFailure},
		{name: "wrapped joined failure", err: fmt.Errorf("session: %w", errors.Join(duration, recordingFailure)), want: recordingFailure},
		{name: "nested joined failure", err: errors.Join(fmt.Errorf("session: %w", errors.Join(duration, recordingFailure)), duration), want: recordingFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := suppressExpectedDuration(test.err)
			if test.want == nil {
				if got != nil {
					t.Fatalf("expected normal process exit, got %v", got)
				}
				return
			}
			if !errors.Is(got, test.want) {
				t.Fatalf("process exit lost independent failure: got %v, want %v", got, test.want)
			}
		})
	}
}

func TestDurationExitTranslationPreservesTypedScheduleEvidence(t *testing.T) {
	schedule := &runtimeSession.LiveScheduledAudioIncompleteError{Completed: 1, Dispatched: 2, Scheduled: 3}
	for _, cause := range []error{schedule, errors.Join(runtimeSession.ErrLiveDurationExceeded, schedule)} {
		got := suppressExpectedDuration(cause)
		var retained *runtimeSession.LiveScheduledAudioIncompleteError
		if !errors.As(got, &retained) || retained != schedule {
			t.Fatalf("process exit lost typed schedule evidence: %v", got)
		}
	}
}

func TestInterruptibleAudioSourceCloseStopsBlockedRead(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	t.Cleanup(func() {
		if err := read.Close(); err != nil {
			t.Errorf("close pipe reader: %v", err)
		}
	})
	t.Cleanup(func() {
		if err := write.Close(); err != nil {
			t.Errorf("close pipe writer: %v", err)
		}
	})

	duplicate, err := devicegateway.OpenInterruptibleInput(read)
	if err != nil {
		t.Fatalf("duplicate input: %v", err)
	}
	source, err := audio.NewFileSource("-", duplicate)
	if err != nil {
		if closeErr := duplicate.Close(); closeErr != nil {
			t.Fatalf("create file source: %v (close duplicate: %v)", err, closeErr)
		}
		t.Fatalf("create file source: %v", err)
	}
	interruptible := &interruptibleAudioSource{source: source, input: duplicate}

	readErr := make(chan error, 1)
	go func() {
		_, readErrValue := interruptible.ReadSamples(context.Background(), make([]int16, audio.FrameSize))
		readErr <- readErrValue
	}()
	select {
	case err := <-readErr:
		t.Fatalf("blocked input read returned before close: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	if err := interruptible.Close(); err != nil {
		t.Fatalf("close interruptible source: %v", err)
	}
	select {
	case err := <-readErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("input read error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("closing interruptible source did not stop blocked read")
	}

	if _, err := write.Write([]byte{0x01}); err != nil {
		t.Fatalf("write through caller-owned input after duplicate close: %v", err)
	}
	var sample [1]byte
	if count, err := read.Read(sample[:]); err != nil || count != 1 || sample[0] != 0x01 {
		t.Fatalf("caller-owned input after duplicate close = (%d, %v, %x), want one byte", count, err, sample[0])
	}
}
