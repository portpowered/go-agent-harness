package probe

import (
	"context"
	"errors"
	"testing"
)

func TestDuplexProgressWaitForOutputSequenceMatchesAcrossReads(t *testing.T) {
	state := newDuplexProgressState()
	progress := &DuplexProgress{state: state}
	want := []byte{1, 0x42, 0x52, 0x42}
	done := make(chan error, 1)
	go func() {
		done <- progress.WaitForOutputSequence(context.Background(), want)
	}()

	state.noteOutput(DuplexOutputEvent{Bytes: 3}, []byte{9, 1, 0x42})
	select {
	case err := <-done:
		t.Fatalf("partial sequence returned early: %v", err)
	default:
	}
	state.noteOutput(DuplexOutputEvent{Bytes: 2}, []byte{0x52, 0x42})
	if err := <-done; err != nil {
		t.Fatalf("WaitForOutputSequence: %v", err)
	}
}

func TestDuplexProgressWaitForOutputSequenceMatchesRecentOutput(t *testing.T) {
	state := newDuplexProgressState()
	state.noteOutput(DuplexOutputEvent{Bytes: 6}, []byte("prefix"))
	state.noteOutput(DuplexOutputEvent{Bytes: 6}, []byte("marker"))

	err := (&DuplexProgress{state: state}).WaitForOutputSequence(context.Background(), []byte("marker"))
	if err != nil {
		t.Fatalf("WaitForOutputSequence: %v", err)
	}
}

func TestDuplexProgressWaitForOutputSequenceHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (&DuplexProgress{state: newDuplexProgressState()}).WaitForOutputSequence(ctx, []byte("missing"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitForOutputSequence error = %v, want context.Canceled", err)
	}
}

func TestDuplexProgressWaitForOutputSequenceReportsClosedOutput(t *testing.T) {
	state := newDuplexProgressState()
	state.noteOutputClosed()
	err := (&DuplexProgress{state: state}).WaitForOutputSequence(context.Background(), []byte("missing"))
	if !errors.Is(err, ErrDuplexPipe) {
		t.Fatalf("WaitForOutputSequence error = %v, want ErrDuplexPipe", err)
	}
}

func TestDuplexProgressWaitForOutputSequenceRejectsOversizedSequence(t *testing.T) {
	err := (&DuplexProgress{state: newDuplexProgressState()}).WaitForOutputSequence(
		context.Background(),
		make([]byte, duplexProgressOutputWindow+1),
	)
	if !errors.Is(err, ErrDuplexConfigInvalid) {
		t.Fatalf("WaitForOutputSequence error = %v, want ErrDuplexConfigInvalid", err)
	}
}
