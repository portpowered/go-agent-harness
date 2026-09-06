package service

import (
	"context"
	"errors"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

type captureInferencer struct {
	result messages.Session
	err    error
}

func (f captureInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return f.result, f.err
}

type captureSession struct {
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
	closed    bool
}

func (*captureSession) Send(context.Context, messages.StreamMessage) bool      { return false }
func (*captureSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return nil }
func (s *captureSession) Done() <-chan struct{}                                { return s.done }
func (s *captureSession) Close() error {
	s.closeOnce.Do(func() {
		s.closed = true
		if s.done != nil {
			close(s.done)
		}
	})
	return s.closeErr
}

func newCaptureOwner(t *testing.T, inner messages.SessionInferencer) *recordingSessionInferencer {
	t.Helper()
	writer := gatewaytesting.NewRecordingWebSocketDialer(nil, "fixture", "fixture")
	owner, err := New(clock.Real{}).TrackSession(inner, writer, filepath.Join(t.TempDir(), "capture.json"))
	if err != nil {
		t.Fatal(err)
	}
	concrete, ok := owner.(*recordingSessionInferencer)
	if !ok {
		t.Fatalf("unexpected capture handle %T", owner)
	}
	return concrete
}

func TestRecordingConnectFailureRetainsCaptureAndCause(t *testing.T) {
	connectErr := errors.New("connection refused")
	owner := newCaptureOwner(t, captureInferencer{err: connectErr})
	if _, err := owner.ConnectSession(t.Context()); !errors.Is(err, connectErr) {
		t.Fatalf("connect error = %v, want original cause", err)
	}
	if err := owner.FlushCapture(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(owner.path); err != nil || len(data) == 0 {
		t.Fatalf("failed connection capture missing: bytes=%d error=%v", len(data), err)
	}
	if _, err := owner.ConnectSession(t.Context()); err == nil {
		t.Fatal("capture owner admitted a second connection")
	}
}

func TestRecordingRejectsInvalidSessionAndClosesResources(t *testing.T) {
	closeErr := errors.New("close failed")
	missingDone := &captureSession{closeErr: closeErr}
	for _, tc := range []struct {
		name    string
		session messages.Session
	}{
		{name: "nil session"},
		{name: "missing termination signal", session: missingDone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := newCaptureOwner(t, captureInferencer{result: tc.session})
			if _, err := owner.ConnectSession(t.Context()); err == nil {
				t.Fatal("invalid session admitted")
			} else if tc.session != nil && !errors.Is(err, closeErr) {
				t.Fatalf("cleanup cause was lost: %v", err)
			}
			if err := owner.FlushCapture(); err != nil {
				t.Fatal(err)
			}
		})
	}
	if !missingDone.closed {
		t.Fatal("invalid session resource was not closed")
	}
}

func TestRecordingFinalizesOnceAfterSessionTermination(t *testing.T) {
	provider := &captureSession{done: make(chan struct{})}
	owner := newCaptureOwner(t, captureInferencer{result: provider})
	connected, err := owner.ConnectSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owner.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("capture published before termination: %v", err)
	}
	if err := connected.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.FlushCapture(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(owner.path); err != nil {
		t.Fatal(err)
	}
	if err := owner.FlushCapture(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owner.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repeated finalization rewrote capture: %v", err)
	}
}

func TestRecordingReportsDurabilityFailure(t *testing.T) {
	owner := newCaptureOwner(t, captureInferencer{err: errors.New("connect failed")})
	owner.path = filepath.Join(t.TempDir(), "missing", "capture.json")
	if _, err := owner.ConnectSession(t.Context()); err == nil {
		t.Fatal("expected connection failure")
	}
	if err := owner.FlushCapture(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("capture persistence failure = %v, want filesystem cause", err)
	}
}

func TestRecordingAdmissionRequiresDependenciesAndLiveContext(t *testing.T) {
	writer := gatewaytesting.NewRecordingWebSocketDialer(nil, "fixture", "fixture")
	inner := captureInferencer{}
	for _, tc := range []struct {
		name   string
		inner  messages.SessionInferencer
		writer *gatewaytesting.RecordingWebSocketDialer
		path   string
	}{
		{name: "session", writer: writer, path: "capture.json"},
		{name: "destination", inner: inner, writer: writer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(clock.Real{}).TrackSession(tc.inner, tc.writer, tc.path); err == nil {
				t.Fatal("missing dependency admitted")
			}
		})
	}
	if _, err := New(clock.Real{}).TrackSession(inner, nil, "capture.json"); err == nil {
		t.Fatal("missing writer admitted")
	}
	owner := newCaptureOwner(t, inner)
	//lint:ignore SA1012 Exercise the capture owner's nil-context admission rejection.
	if _, err := owner.ConnectSession(nil); err == nil {
		t.Fatal("nil context admitted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := owner.ConnectSession(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission: %v", err)
	}
	if _, err := os.Stat(owner.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled admission wrote evidence: %v", err)
	}
}
