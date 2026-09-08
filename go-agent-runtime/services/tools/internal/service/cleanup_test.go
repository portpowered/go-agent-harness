package service

import (
	"errors"
	"reflect"
	"testing"
	"time"

	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

func TestCleanupCoordinatorRunsOrderedCleanupExactlyOnce(t *testing.T) {
	firstErr := errors.New("first cleanup failed")
	lastErr := errors.New("last cleanup failed")
	var order []string

	coordinator := newCleanupCoordinator(
		func() error {
			order = append(order, "first")
			return firstErr
		},
		func() error {
			order = append(order, "panic")
			panic("cleanup panic")
		},
		func() error {
			order = append(order, "last")
			return lastErr
		},
	)

	gotErr := coordinator.Close()
	if gotErr == nil || !errors.Is(gotErr, firstErr) || !errors.Is(gotErr, lastErr) || !errors.Is(gotErr, public.ErrCapabilityClosePanic) {
		t.Fatalf("coordinator error = %v, want all cleanup failures", gotErr)
	}
	if want := []string{"first", "panic", "last"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("cleanup order = %#v, want %#v", order, want)
	}

	secondErr := coordinator.Close()
	if !errors.Is(secondErr, gotErr) {
		t.Fatalf("second close error = %v, want recorded first result %v", secondErr, gotErr)
	}
	if !reflect.DeepEqual(order, []string{"first", "panic", "last"}) {
		t.Fatalf("second close performed new work: %#v", order)
	}
}

func TestCleanupCoordinatorIgnoresNilCleanup(t *testing.T) {
	if err := newCleanupCoordinator(nil).Close(); err != nil {
		t.Fatalf("nil cleanup: %v", err)
	}
	var coordinator *cleanupCoordinator
	if err := coordinator.Close(); err != nil {
		t.Fatalf("nil coordinator: %v", err)
	}
}

func TestCleanupCoordinatorBoundsNonCooperativeCleanup(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	finished := make(chan struct{})
	secondCalls := 0
	secondErr := errors.New("second cleanup failed")

	coordinator := newCleanupCoordinatorWithTimeout(20*time.Millisecond,
		func() error {
			close(started)
			<-release
			close(finished)
			return nil
		},
		func() error {
			secondCalls++
			return secondErr
		},
	)

	closeDone := make(chan error, 1)
	go func() { closeDone <- coordinator.Close() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cleanup hook did not start")
	}

	var closeErr error
	select {
	case closeErr = <-closeDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("coordinator waited past the cleanup bound")
	}
	if !errors.Is(closeErr, public.ErrCapabilityCloseTimeout) || !errors.Is(closeErr, secondErr) {
		t.Fatalf("bounded cleanup error = %v, want timeout and later cleanup failure", closeErr)
	}
	if secondCalls != 1 {
		t.Fatalf("later cleanup calls = %d, want one after timeout", secondCalls)
	}

	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("timed-out cleanup hook did not finish after release")
	}
	if secondErrAgain := coordinator.Close(); !errors.Is(secondErrAgain, closeErr) {
		t.Fatalf("repeated close error = %v, want recorded error %v", secondErrAgain, closeErr)
	}
}
