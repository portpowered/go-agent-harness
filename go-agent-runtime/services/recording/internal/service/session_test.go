package service

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimesession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
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

type configuredCaptureInferencer struct {
	result       messages.Session
	request      inference.SessionRequest
	inputFormat  models.AudioFormat
	inputRate    models.SampleRate
	outputFormat models.AudioFormat
	outputRate   models.SampleRate
}

func (f *configuredCaptureInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return f.result, nil
}

func (f *configuredCaptureInferencer) Request() inference.SessionRequest { return f.request }

func (f *configuredCaptureInferencer) SetSessionAudioInput(format models.AudioFormat, rate models.SampleRate) {
	f.inputFormat, f.inputRate = format, rate
}

func (f *configuredCaptureInferencer) SetSessionAudioOutput(format models.AudioFormat, rate models.SampleRate) {
	f.outputFormat, f.outputRate = format, rate
}

type captureSession struct {
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
	closed    bool
}

type providerCaptureDialer struct{}

func (providerCaptureDialer) Dial(string, map[string]string) (transport.Conn, error) { return nil, nil }

type providerRecorderDialer struct {
	conn *providerRecorderConn
	err  error
}

func (d providerRecorderDialer) Dial(string, map[string]string) (transport.Conn, error) {
	if d.conn == nil && d.err == nil {
		return nil, nil
	}
	return d.conn, d.err
}

type providerRecorderConn struct {
	inbound  []byte
	read     bool
	closed   bool
	writeErr error
}

func (c *providerRecorderConn) ReadMessage() (int, []byte, error) {
	if c.read || c.inbound == nil {
		return 0, nil, io.EOF
	}
	c.read = true
	return 1, append([]byte(nil), c.inbound...), nil
}

func (c *providerRecorderConn) WriteMessage(_ int, _ []byte) error { return c.writeErr }
func (c *providerRecorderConn) Close() error                       { c.closed = true; return nil }

type providerRecorderSession struct {
	done    chan struct{}
	conn    transport.Conn
	once    sync.Once
	inbound *messages.TypedBuffer[messages.StreamMessage]
}

func (*providerRecorderSession) Send(context.Context, messages.StreamMessage) bool { return false }
func (s *providerRecorderSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.inbound
}
func (s *providerRecorderSession) Done() <-chan struct{} { return s.done }
func (s *providerRecorderSession) Close() error {
	s.once.Do(func() {
		close(s.done)
		_ = s.conn.Close()
	})
	return nil
}

type providerRecorderInferencer struct{ dialer transport.Dialer }

func (i providerRecorderInferencer) ConnectSession(context.Context) (messages.Session, error) {
	conn, err := i.dialer.Dial("wss://provider.invalid/session", nil)
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, errors.New("provider dialer returned a nil connection")
	}
	if err := conn.WriteMessage(1, []byte(`{"type":"session.update"}`)); err != nil {
		return nil, err
	}
	if _, _, err := conn.ReadMessage(); err != nil {
		return nil, err
	}
	return &providerRecorderSession{
		done: make(chan struct{}), conn: conn,
		inbound: messages.NewTypedBuffer[messages.StreamMessage](1),
	}, nil
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

type acceptedCaptureSession struct {
	done    chan struct{}
	once    sync.Once
	inbound *messages.TypedBuffer[messages.StreamMessage]
}

type missingDoneCaptureSession struct {
	inbound  *messages.TypedBuffer[messages.StreamMessage]
	closeErr error
	closed   bool
}

func (*missingDoneCaptureSession) Send(context.Context, messages.StreamMessage) bool { return false }
func (s *missingDoneCaptureSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.inbound
}
func (*missingDoneCaptureSession) Done() <-chan struct{} { return nil }
func (s *missingDoneCaptureSession) Close() error {
	s.closed = true
	return s.closeErr
}

func (*acceptedCaptureSession) Send(context.Context, messages.StreamMessage) bool { return true }
func (s *acceptedCaptureSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.inbound
}
func (s *acceptedCaptureSession) Done() <-chan struct{} { return s.done }
func (s *acceptedCaptureSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

func TestTrackInjectedSessionReleasesClaimAfterConnectFailure(t *testing.T) {
	service := New(clock.Real{})
	destination := filepath.Join(t.TempDir(), "failed-injected.capture.json")
	connectErr := errors.New("injected provider unavailable")
	failed, err := service.TrackInjectedSession(captureInferencer{err: connectErr}, destination)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failed.ConnectSession(t.Context()); !errors.Is(err, connectErr) {
		t.Fatalf("injected connect failure = %v, want original cause", err)
	}

	provider := &acceptedCaptureSession{done: make(chan struct{}), inbound: messages.NewTypedBuffer[messages.StreamMessage](1)}
	reacquired, err := service.TrackInjectedSession(captureInferencer{result: provider}, destination)
	if err != nil {
		t.Fatalf("failed injected connection retained destination claim: %v", err)
	}
	session, err := reacquired.ConnectSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reacquired.FlushCapture(); err != nil {
		t.Fatal(err)
	}
}

func TestTrackInjectedSessionClosesAndReleasesInvalidSession(t *testing.T) {
	service := New(clock.Real{})
	destination := filepath.Join(t.TempDir(), "missing-done.capture.json")
	closeErr := errors.New("invalid provider session close failed")
	provider := &missingDoneCaptureSession{inbound: messages.NewTypedBuffer[messages.StreamMessage](1), closeErr: closeErr}
	owner, err := service.TrackInjectedSession(captureInferencer{result: provider}, destination)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ConnectSession(t.Context()); !errors.Is(err, closeErr) || !strings.Contains(err.Error(), "termination signal") {
		t.Fatalf("session without Done signal = %v, want termination error and cleanup cause", err)
	}
	if !provider.closed {
		t.Fatal("session without a termination signal was not closed")
	}
	if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	valid := &acceptedCaptureSession{done: make(chan struct{}), inbound: messages.NewTypedBuffer[messages.StreamMessage](1)}
	reacquired, err := service.TrackInjectedSession(captureInferencer{result: valid}, destination)
	if err != nil {
		t.Fatalf("invalid injected session retained destination claim: %v", err)
	}
	session, err := reacquired.ConnectSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reacquired.FlushCapture(); err != nil {
		t.Fatal(err)
	}
}

func TestTrackInjectedSessionForwardsOptionalConfigurationAndFlushDestination(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "injected.capture.json")
	provider := &acceptedCaptureSession{done: make(chan struct{}), inbound: messages.NewTypedBuffer[messages.StreamMessage](1)}
	request := inference.SessionRequest{Config: models.SessionConfig{Model: "gpt-realtime", Voice: "alloy"}}
	inner := &configuredCaptureInferencer{result: provider, request: request}
	owner, err := New(clock.Real{}).TrackInjectedSession(inner, destination)
	if err != nil {
		t.Fatal(err)
	}
	configuration, ok := owner.(interface {
		Request() inference.SessionRequest
		SetSessionAudioInput(models.AudioFormat, models.SampleRate)
		SetSessionAudioOutput(models.AudioFormat, models.SampleRate)
	})
	if !ok {
		t.Fatalf("injected capture %T does not forward optional provider configuration", owner)
	}
	if got := configuration.Request(); !reflect.DeepEqual(got, request) {
		t.Fatalf("injected session request = %#v, want %#v", got, request)
	}
	configuration.SetSessionAudioInput(models.AudioFormatPCM16, models.SampleRate16000)
	configuration.SetSessionAudioOutput(models.AudioFormatG711Ulaw, models.SampleRate8000)
	if inner.inputFormat != models.AudioFormatPCM16 || inner.inputRate != models.SampleRate16000 || inner.outputFormat != models.AudioFormatG711Ulaw || inner.outputRate != models.SampleRate8000 {
		t.Fatalf("injected audio configuration was not forwarded: input=%s/%d output=%s/%d", inner.inputFormat, inner.inputRate, inner.outputFormat, inner.outputRate)
	}
	session, err := owner.ConnectSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "copy.capture.json")
	if err := owner.FlushToFile(copyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := gatewaytesting.LoadSessionCaptureForReplay(copyPath); err != nil {
		t.Fatalf("alternate injected flush is not a valid protected capture: %v", err)
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
	configuration, ok := recording.SessionCapture(owner).(interface {
		Request() inference.SessionRequest
		SetSessionAudioInput(models.AudioFormat, models.SampleRate)
		SetSessionAudioOutput(models.AudioFormat, models.SampleRate)
	})
	if !ok || !reflect.DeepEqual(configuration.Request(), inference.SessionRequest{}) {
		t.Fatalf("session without optional provider configuration returned %#v", configuration)
	}
	configuration.SetSessionAudioInput(models.AudioFormatPCM16, models.SampleRate16000)
	configuration.SetSessionAudioOutput(models.AudioFormatG711Ulaw, models.SampleRate8000)
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

func TestServiceClaimReportsContentionAndCanReleaseRepeatedly(t *testing.T) {
	service := New(clock.Real{})
	destination := filepath.Join(t.TempDir(), "capture.json")
	claim, err := service.Claim(recording.ClaimOptions{Destination: destination, Kind: recording.ClaimKindCapture})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Claim(recording.ClaimOptions{Destination: destination, Kind: recording.ClaimKindCapture}); err == nil {
		t.Fatal("second recording owner acquired the active destination")
	} else {
		var claimErr *recording.ClaimError
		if !errors.As(err, &claimErr) || !errors.Is(err, recording.ErrLiveEvidenceClaimed) {
			t.Fatalf("contention error = %v, want typed claimed error", err)
		}
		if strings.Contains(claimErr.Error(), "Authorization") || strings.Contains(claimErr.Error(), "token=") {
			t.Fatalf("contention error exposed secret material: %v", claimErr)
		}
	}
	if err := claim.Release(); err != nil {
		t.Fatal(err)
	}
	if err := claim.Release(); err != nil {
		t.Fatalf("repeat release = %v, want idempotent success", err)
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

func TestTrackSessionForwardsProviderConfigurationAndAlternateFlush(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "provider.capture.json")
	inner := &configuredCaptureInferencer{
		result:  &captureSession{done: make(chan struct{})},
		request: inference.SessionRequest{Config: models.SessionConfig{Model: "gpt-realtime", Voice: "alloy"}},
	}
	owner, err := New(clock.Real{}).TrackSession(inner, gatewaytesting.NewRecordingWebSocketDialer(nil, "openai", "gpt-realtime"), destination)
	if err != nil {
		t.Fatal(err)
	}
	configuration, ok := owner.(interface {
		Request() inference.SessionRequest
		SetSessionAudioInput(models.AudioFormat, models.SampleRate)
		SetSessionAudioOutput(models.AudioFormat, models.SampleRate)
	})
	if !ok || !reflect.DeepEqual(configuration.Request(), inner.request) {
		t.Fatalf("recording session %T did not preserve the optional provider configuration", owner)
	}
	configuration.SetSessionAudioInput(models.AudioFormatPCM16, models.SampleRate16000)
	configuration.SetSessionAudioOutput(models.AudioFormatG711Ulaw, models.SampleRate8000)
	if inner.inputRate != models.SampleRate16000 || inner.outputRate != models.SampleRate8000 {
		t.Fatalf("recording session audio config = %d/%d, want 16000/8000", inner.inputRate, inner.outputRate)
	}
	session, err := owner.ConnectSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "copy.capture.json")
	if err := owner.FlushToFile(copyPath); err != nil {
		t.Fatal(err)
	}
	loaded, err := gatewaytesting.LoadSessionCaptureForReplay(copyPath)
	if err != nil || !loaded.IntegrityVerified {
		t.Fatalf("alternate provider flush = %#v, error = %v", loaded, err)
	}
}

func TestTrackInjectedSessionRejectsIncompleteContract(t *testing.T) {
	service := New(clock.Real{})
	destination := filepath.Join(t.TempDir(), "provider.capture.json")
	for _, tc := range []struct {
		name  string
		inner messages.SessionInferencer
		path  string
	}{
		{name: "missing inferencer", path: destination},
		{name: "missing destination", inner: captureInferencer{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.TrackInjectedSession(tc.inner, tc.path); err == nil {
				t.Fatal("incomplete injected recording contract was admitted")
			}
		})
	}
}
