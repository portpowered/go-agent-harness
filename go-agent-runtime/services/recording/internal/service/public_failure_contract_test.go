package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimesession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestRunLiveEvidenceFinalizesWithNilRequestContext(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "nil-context-recording")
	service := New(clock.Real{})
	if err := service.RunLiveEvidence(nil, recording.LiveEvidenceOptions{Destination: destination}, func(context.Context, recording.LiveEvidence) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), string(messages.TerminalReasonLoopSynthesizedCompletion)) {
		t.Fatalf("nil-context run was not finalized: %s", manifest)
	}
}

func TestRunLiveEvidenceRejectsMissingCallbackBeforeOpeningEvidence(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "missing-callback-recording")
	service := New(clock.Real{})
	if err := service.RunLiveEvidence(t.Context(), recording.LiveEvidenceOptions{Destination: destination}, nil); err == nil {
		t.Fatal("missing recording callback was accepted")
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing callback opened a recording destination: %v", err)
	}
}

func TestRunLiveEvidencePreservesCancellationCompletion(t *testing.T) {
	for _, tc := range []struct {
		name       string
		completion recording.LiveCompletion
	}{
		{name: "user cancelled", completion: recording.LiveCompletion{UserCancelled: true}},
		{name: "room cancellation only", completion: recording.LiveCompletion{RoomCancellationOnly: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "cancelled-recording")
			service := New(clock.Real{})
			err := service.RunLiveEvidence(t.Context(), recording.LiveEvidenceOptions{Destination: destination}, func(ctx context.Context, evidence recording.LiveEvidence) error {
				return evidence.SetCompletion(ctx, tc.completion)
			})
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(manifest), string(messages.TerminalReasonCancellation)) || !strings.Contains(string(manifest), string(messages.TerminalProvenanceCLI)) {
				t.Fatalf("cancellation completion was not preserved: %s", manifest)
			}
		})
	}
}

func TestRecordProviderSessionReleasesClaimWhenBuilderReturnsNil(t *testing.T) {
	service := New(clock.Real{})
	destination := filepath.Join(t.TempDir(), "nil-provider.capture.json")
	_, err := service.RecordProviderSession(service, recording.ProviderSessionOptions{
		Destination: destination,
		Provider:    "fixture",
		Model:       "fixture-model",
		Dialer:      providerCaptureDialer{},
		Build: func(transport.Dialer) (messages.SessionInferencer, error) {
			return nil, nil
		},
	})
	if err == nil {
		t.Fatal("provider builder returning nil inferencer was admitted")
	}
	sink, err := service.OpenProviderCapture(recording.ProviderCaptureOptions{Destination: destination})
	if err != nil {
		t.Fatalf("nil provider builder retained destination claim: %v", err)
	}
	if err := sink.Abort(); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestRecordProviderSessionUsesGenericTypeForUnlabeledWireMessages(t *testing.T) {
	service := New(clock.Real{})
	conn := &providerRecorderConn{inbound: []byte(`{"event":"session.created"}`)}
	destination := filepath.Join(t.TempDir(), "unlabeled-provider.capture.json")
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
	if len(loaded.Capture.Records) != 2 || loaded.Capture.Records[1].Type != "websocket.message" {
		t.Fatalf("unlabeled provider event type = %#v, want generic websocket.message", loaded.Capture.Records)
	}
}

func TestRunLiveEvidencePublishesBrowserArtifact(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "browser-recording")
	artifact := []byte("{\"type\":\"navigation\",\"url\":\"https://example.test/\"}\n")
	var providerPath string
	err := New(clock.Real{}).RunLiveEvidence(t.Context(), recording.LiveEvidenceOptions{Destination: destination}, func(ctx context.Context, evidence recording.LiveEvidence) error {
		provider, ok := evidence.(recording.ProviderCapture)
		if !ok {
			return errors.New("recording did not expose its provider capture path")
		}
		providerPath = provider.ProviderCapturePath()
		return evidence.RecordBrowserArtifact(ctx, &transcript.BrowserArtifact{Format: transcript.BrowserEventsVersion, Data: artifact})
	})
	if err != nil {
		t.Fatal(err)
	}
	if providerPath == "" {
		t.Fatal("recording did not expose its provider capture path")
	}
	got, err := os.ReadFile(filepath.Join(destination, transcript.BrowserArtifactDefaultPath))
	if err != nil || string(got) != string(artifact) {
		t.Fatalf("published browser artifact = %q, error = %v", got, err)
	}
}

func TestRunLiveEvidencePersistsDurationExpiredPartialAudio(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "duration-recording")
	err := New(clock.Real{}).RunLiveEvidence(t.Context(), recording.LiveEvidenceOptions{
		Destination: destination, OutputAudioRate: 24000,
	}, func(ctx context.Context, evidence recording.LiveEvidence) error {
		for _, message := range []messages.StreamMessage{
			{Type: messages.StreamTypeSessionOpen},
			{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("partial transcript")},
			{Type: messages.StreamTypeAudioDelta, ResponseID: "response-1", Value: messages.NewAudioDeltaValue([]byte{1, 0, 2, 0})},
		} {
			if err := evidence.ObserveMessage(ctx, runtimesession.LiveRecordAgent, message); err != nil {
				return err
			}
		}
		return evidence.SetCompletion(ctx, recording.LiveCompletion{DurationExpired: true, SawSessionOpen: true, OutputObserved: true})
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil || !strings.Contains(string(manifest), "max_duration") || !strings.Contains(string(manifest), "partial") {
		t.Fatalf("duration-expired manifest = %s, error = %v", manifest, err)
	}
	audio, err := os.ReadFile(filepath.Join(destination, "audio", "out-000.pcm"))
	if err != nil || string(audio) != string([]byte{1, 0, 2, 0}) {
		t.Fatalf("partial audio = %v, error = %v", audio, err)
	}
}

func TestRunLiveEvidencePreservesPublicTerminalOutcomes(t *testing.T) {
	tests := []struct {
		name     string
		message  messages.StreamMessage
		contains []string
		absent   string
	}{
		{
			name: "provider failure",
			message: messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValueWithTerminal(
				"provider disconnected", "upstream", messages.TerminalReasonTerminalFailure,
				messages.TerminalProvenanceProvider, messages.TerminalOutputNone,
			)},
			contains: []string{string(messages.TerminalReasonTerminalFailure), string(messages.TerminalProvenanceProvider)},
		},
		{
			name:     "legacy provider error defaults",
			message:  messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue("legacy provider failure")},
			contains: []string{string(messages.TerminalReasonTerminalFailure), string(messages.TerminalProvenanceProvider), string(messages.TerminalOutputNone)},
		},
		{
			name: "provider session close",
			message: messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValueWithTerminal(
				"session-1", "completed", "provider_completion", messages.TerminalReasonProviderAuthoredCompletion,
				messages.TerminalProvenanceProvider, messages.TerminalOutputComplete,
			)},
			contains: []string{"provider_completion", string(messages.TerminalReasonProviderAuthoredCompletion), string(messages.TerminalOutputComplete)},
			absent:   string(messages.TerminalReasonLoopSynthesizedCompletion),
		},
		{
			name:     "informational error",
			message:  messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewNonTerminalErrorValue("provider notice", "warning")},
			contains: []string{string(messages.TerminalReasonLoopSynthesizedCompletion)},
			absent:   string(messages.TerminalReasonTerminalFailure),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "recording")
			err := New(clock.Real{}).RunLiveEvidence(t.Context(), recording.LiveEvidenceOptions{Destination: destination}, func(ctx context.Context, evidence recording.LiveEvidence) error {
				return evidence.ObserveMessage(ctx, runtimesession.LiveRecordAgent, tc.message)
			})
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range tc.contains {
				if !strings.Contains(string(manifest), value) {
					t.Fatalf("manifest lacks %q: %s", value, manifest)
				}
			}
			if tc.absent != "" && strings.Contains(string(manifest), tc.absent) {
				t.Fatalf("manifest unexpectedly contains %q: %s", tc.absent, manifest)
			}
		})
	}
}

type providerSinkFailureStub struct {
	appendErr  error
	commitErr  error
	discardErr error
	flushErr   error
	aborted    bool
}

func (s *providerSinkFailureStub) Append(gatewaytesting.CapturedSessionEvent) error {
	return s.appendErr
}
func (s *providerSinkFailureStub) Commit(int) error  { return s.commitErr }
func (s *providerSinkFailureStub) Discard(int) error { return s.discardErr }
func (s *providerSinkFailureStub) FlushToFile(string, gatewaytesting.SessionCapture) error {
	return s.flushErr
}
func (s *providerSinkFailureStub) Abort() error { s.aborted = true; return nil }

type providerServiceFailureStub struct {
	sink recording.ProviderCaptureSink
	err  error
}

func (s providerServiceFailureStub) OpenProviderCapture(recording.ProviderCaptureOptions) (recording.ProviderCaptureSink, error) {
	return s.sink, s.err
}

func TestRecordProviderSessionAbortsCaptureAfterSinkFailures(t *testing.T) {
	writeErr := errors.New("provider write failed")
	appendErr := errors.New("append failed")
	commitErr := errors.New("commit failed")
	discardErr := errors.New("discard failed")
	flushErr := errors.New("flush failed")
	tests := []struct {
		name      string
		sink      *providerSinkFailureStub
		writeErr  error
		wantError error
	}{
		{name: "append", sink: &providerSinkFailureStub{appendErr: appendErr}, wantError: appendErr},
		{name: "commit", sink: &providerSinkFailureStub{commitErr: commitErr}, wantError: commitErr},
		{name: "discard", sink: &providerSinkFailureStub{discardErr: discardErr}, writeErr: writeErr, wantError: discardErr},
		{name: "flush", sink: &providerSinkFailureStub{flushErr: flushErr}, wantError: flushErr},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conn := &providerRecorderConn{inbound: []byte(`{"type":"session.created"}`), writeErr: tc.writeErr}
			owner, err := New(clock.Real{}).RecordProviderSession(providerServiceFailureStub{sink: tc.sink}, recording.ProviderSessionOptions{
				Destination: filepath.Join(t.TempDir(), "provider.capture.json"), Provider: "fixture", Model: "fixture-model",
				Dialer: providerRecorderDialer{conn: conn},
				Build: func(dialer transport.Dialer) (messages.SessionInferencer, error) {
					return providerRecorderInferencer{dialer: dialer}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			session, connectErr := owner.ConnectSession(t.Context())
			if tc.writeErr != nil {
				if !errors.Is(connectErr, tc.writeErr) {
					t.Fatalf("connect error = %v, want provider write failure", connectErr)
				}
			} else if connectErr != nil {
				t.Fatal(connectErr)
			} else if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			if err := owner.FlushCapture(); !errors.Is(err, tc.wantError) {
				t.Fatalf("capture finalization error = %v, want %v", err, tc.wantError)
			}
			if !tc.sink.aborted {
				t.Fatal("failed provider capture was not aborted")
			}
		})
	}
}

func TestOpenLiveSemanticEvidencePublishesTerminalSidecarOnFinalize(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "provider.capture.json")
	recorder, err := New(clock.Real{}).OpenLiveSemanticEvidence(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	sidecarPath := filepath.Join(filepath.Dir(capturePath), "provider.capture.jsonl")
	if _, err := os.Stat(sidecarPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("semantic sidecar published before a terminal event: %v", err)
	}
	terminal := messages.NewSessionCloseValueWithTerminal(
		"session-1", "completed", "completed", messages.TerminalReasonProviderAuthoredCompletion,
		messages.TerminalProvenanceProvider, messages.TerminalOutputComplete,
	)
	if err := recorder.RecordEvent(t.Context(), runtimesession.LiveEvent{
		Kind: string(runtimesession.LiveEventTerminal), Timestamp: time.Unix(1_750_000_000, 0), Terminal: terminal,
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Finalize(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	sidecar, err := os.ReadFile(sidecarPath)
	if err != nil || !strings.Contains(string(sidecar), "runtime-event") {
		t.Fatalf("terminal semantic sidecar = %s, error = %v", sidecar, err)
	}
}

func TestRunLiveEvidenceFinalizesCallbackFailure(t *testing.T) {
	cause := errors.New("recording callback failed")
	destination := filepath.Join(t.TempDir(), "failed-callback")
	err := New(clock.Real{}).RunLiveEvidence(t.Context(), recording.LiveEvidenceOptions{Destination: destination}, func(context.Context, recording.LiveEvidence) error {
		return cause
	})
	if !errors.Is(err, cause) {
		t.Fatalf("callback error = %v, want original failure", err)
	}
	manifest, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil || !strings.Contains(string(manifest), string(messages.TerminalReasonTerminalFailure)) {
		t.Fatalf("callback failure manifest = %s, error = %v", manifest, err)
	}
}

func TestRecordProviderSessionRejectsIncompletePublicContract(t *testing.T) {
	service := New(clock.Real{})
	base := recording.ProviderSessionOptions{
		Destination: filepath.Join(t.TempDir(), "provider.capture.json"), Provider: "fixture", Model: "fixture-model",
		Dialer: providerCaptureDialer{},
		Build:  func(transport.Dialer) (messages.SessionInferencer, error) { return captureInferencer{}, nil },
	}
	missingDestination := base
	missingDestination.Destination = ""
	missingProvider := base
	missingProvider.Provider = ""
	missingModel := base
	missingModel.Model = ""
	missingDialer := base
	missingDialer.Dialer = nil
	missingBuilder := base
	missingBuilder.Build = nil
	openErr := errors.New("provider sink unavailable")
	for _, tc := range []struct {
		name    string
		service recording.ProviderCaptureService
		options recording.ProviderSessionOptions
		want    error
	}{
		{name: "missing capture service", options: base},
		{name: "missing destination", service: service, options: missingDestination},
		{name: "missing provider", service: service, options: missingProvider},
		{name: "missing model", service: service, options: missingModel},
		{name: "missing dialer", service: service, options: missingDialer},
		{name: "missing builder", service: service, options: missingBuilder},
		{name: "nil sink", service: providerServiceFailureStub{}, options: base},
		{name: "sink error", service: providerServiceFailureStub{err: openErr}, options: base, want: openErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.RecordProviderSession(tc.service, tc.options)
			if err == nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("invalid provider contract error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRecordProviderSessionFinalizesDialFailuresWithoutWireEvents(t *testing.T) {
	dialErr := errors.New("provider dial failed")
	for _, tc := range []struct {
		name   string
		dialer transport.Dialer
		cause  error
		text   string
	}{
		{name: "dial error", dialer: providerRecorderDialer{err: dialErr}, cause: dialErr},
		{name: "nil connection", dialer: providerRecorderDialer{}, text: "nil connection"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "provider.capture.json")
			owner, err := New(clock.Real{}).RecordProviderSession(New(clock.Real{}), recording.ProviderSessionOptions{
				Destination: destination, Provider: "fixture", Model: "fixture-model", Dialer: tc.dialer,
				Build: func(dialer transport.Dialer) (messages.SessionInferencer, error) {
					return providerRecorderInferencer{dialer: dialer}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owner.ConnectSession(t.Context()); tc.cause != nil && !errors.Is(err, tc.cause) || tc.text != "" && (err == nil || !strings.Contains(err.Error(), tc.text)) {
				t.Fatalf("provider dial result = %v", err)
			}
			if err := owner.FlushCapture(); err != nil {
				t.Fatal(err)
			}
			loaded, err := gatewaytesting.LoadSessionCaptureForReplay(destination)
			if err != nil || len(loaded.Capture.Records) != 0 {
				t.Fatalf("failed dial capture = %#v, error = %v", loaded, err)
			}
		})
	}
}

func TestRunLiveEvidenceFinalizesBeforeRethrowingCallbackPanic(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "panic-recording")
	var panicValue any
	func() {
		defer func() { panicValue = recover() }()
		_ = New(clock.Real{}).RunLiveEvidence(t.Context(), recording.LiveEvidenceOptions{Destination: destination}, func(context.Context, recording.LiveEvidence) error {
			panic("callback panic")
		})
	}()
	if panicValue != "callback panic" {
		t.Fatalf("callback panic = %v", panicValue)
	}
	manifest, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil || !strings.Contains(string(manifest), string(messages.TerminalReasonTerminalFailure)) {
		t.Fatalf("panic finalization manifest = %s, error = %v", manifest, err)
	}
}
