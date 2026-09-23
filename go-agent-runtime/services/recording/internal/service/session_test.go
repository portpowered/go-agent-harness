package service

import (
	"context"
	"errors"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimesession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
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

type providerCaptureDialer struct{}

func (providerCaptureDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return nil, errors.New("unexpected provider dial")
}

type providerRecorderDialer struct{ conn transport.Conn }

func (d providerRecorderDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return d.conn, nil
}

type providerRecorderConn struct {
	inbound []byte
	read    bool
	writes  [][]byte
	closed  bool
}

func (c *providerRecorderConn) ReadMessage() (int, []byte, error) {
	if c.read {
		return 0, nil, io.EOF
	}
	c.read = true
	return 1, append([]byte(nil), c.inbound...), nil
}

func (c *providerRecorderConn) WriteMessage(_ int, payload []byte) error {
	c.writes = append(c.writes, append([]byte(nil), payload...))
	return nil
}

func (c *providerRecorderConn) Close() error {
	c.closed = true
	return nil
}

type providerRecorderSession struct {
	done chan struct{}
	conn transport.Conn
	once sync.Once
}

func (*providerRecorderSession) Send(context.Context, messages.StreamMessage) bool      { return false }
func (*providerRecorderSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return nil }
func (s *providerRecorderSession) Done() <-chan struct{}                                { return s.done }
func (s *providerRecorderSession) Close() error {
	var closeErr error
	s.once.Do(func() {
		closeErr = s.conn.Close()
		close(s.done)
	})
	return closeErr
}

type providerRecorderInferencer struct{ dialer transport.Dialer }

func (i providerRecorderInferencer) ConnectSession(context.Context) (messages.Session, error) {
	conn, err := i.dialer.Dial("wss://provider.invalid/session", nil)
	if err != nil {
		return nil, err
	}
	if err := conn.WriteMessage(1, []byte(`{"type":"session.update"}`)); err != nil {
		return nil, err
	}
	if _, _, err := conn.ReadMessage(); err != nil {
		return nil, err
	}
	return &providerRecorderSession{done: make(chan struct{}), conn: conn}, nil
}

func TestRecordProviderSessionPublishesOrderedProtectedWireCapture(t *testing.T) {
	service := New(clock.Real{})
	conn := &providerRecorderConn{inbound: []byte(`{"type":"session.created"}`)}
	destination := filepath.Join(t.TempDir(), "provider.capture.json")
	owner, err := service.RecordProviderSession(service, recording.ProviderSessionOptions{
		Destination: destination,
		Provider:    "fixture",
		Model:       "fixture-model",
		Dialer:      providerRecorderDialer{conn: conn},
		Build: func(dialer transport.Dialer) (messages.SessionInferencer, error) {
			return providerRecorderInferencer{dialer: dialer}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := owner.ConnectSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.FlushCapture(); err != nil {
		t.Fatal(err)
	}
	loaded, err := gatewaytesting.LoadSessionCaptureForReplay(destination)
	if err != nil {
		t.Fatal(err)
	}
	records := loaded.Capture.Records
	if len(records) != 2 {
		t.Fatalf("capture record count = %d, want 2", len(records))
	}
	if records[0].Direction != gatewaytesting.DirectionClientToServer || records[0].Type != "session.update" || string(records[0].Payload) != `{"type":"session.update"}` {
		t.Fatalf("first capture record = %#v, want outbound session.update", records[0])
	}
	if records[1].Direction != gatewaytesting.DirectionServerToClient || records[1].Type != "session.created" || string(records[1].Payload) != `{"type":"session.created"}` {
		t.Fatalf("second capture record = %#v, want inbound session.created", records[1])
	}
	if !conn.closed {
		t.Fatal("provider websocket connection was not closed with its session")
	}
}

func TestRecordProviderSessionReleasesAdmissionWhenProviderBuildFails(t *testing.T) {
	service := New(clock.Real{})
	destination := filepath.Join(t.TempDir(), "provider.capture.json")
	buildErr := errors.New("provider construction failed")
	_, err := service.RecordProviderSession(service, recording.ProviderSessionOptions{
		Destination: destination,
		Provider:    "fixture",
		Model:       "fixture-model",
		Dialer:      providerCaptureDialer{},
		Build: func(transport.Dialer) (messages.SessionInferencer, error) {
			return nil, buildErr
		},
	})
	if !errors.Is(err, buildErr) {
		t.Fatalf("provider build error = %v, want original cause", err)
	}
	sink, err := service.OpenProviderCapture(recording.ProviderCaptureOptions{Destination: destination})
	if err != nil {
		t.Fatalf("failed provider build retained destination admission: %v", err)
	}
	if err := sink.Abort(); err != nil {
		t.Fatalf("release retry admission: %v", err)
	}
}

func TestRunLiveEvidenceReturnsLatchedObservationFailure(t *testing.T) {
	service := New(clock.Real{})
	var observeErr error
	err := service.RunLiveEvidence(t.Context(), recording.LiveEvidenceOptions{
		Destination: filepath.Join(t.TempDir(), "recording"), OutputAudioRate: 24000,
	}, func(ctx context.Context, evidence recording.LiveEvidence) error {
		observeErr = evidence.ObserveMessage(ctx, runtimesession.LiveRecordAgent, messages.StreamMessage{
			Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{1}),
		})
		if observeErr == nil {
			return errors.New("invalid audio observation unexpectedly succeeded")
		}
		return nil
	})
	if !errors.Is(err, observeErr) {
		t.Fatalf("run error = %v, want latched observation failure %v", err, observeErr)
	}
}
