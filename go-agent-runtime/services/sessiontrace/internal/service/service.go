package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
)

const (
	stagingDirectoryMode os.FileMode = 0o755
	claimFileMode        os.FileMode = 0o600
)

type Service struct{}

func New() *Service { return &Service{} }

var _ sessiontrace.Service = (*Service)(nil)

func (*Service) Prepare(request sessiontrace.Request) (sessiontrace.Prepared, error) {
	if !request.TraceAudio && request.RecordDirectory == "" {
		return nil, nil
	}
	if request.Clock == nil {
		return nil, sessiontrace.ErrClockRequired
	}
	path := "audio-trace"
	if request.RecordDirectory != "" {
		parent := filepath.Dir(filepath.Clean(request.RecordDirectory))
		if err := os.MkdirAll(parent, stagingDirectoryMode); err != nil {
			return nil, err
		}
		var err error
		path, err = os.MkdirTemp(parent, ".session-audio-trace-")
		if err != nil {
			return nil, err
		}
	}
	trace, err := recording.NewTrace(path, request.Clock)
	if err != nil {
		return nil, err
	}
	observer := &traceObserver{trace: trace, credentials: append([]string(nil), request.Credentials...)}
	runtime := combineObservers(request.RuntimeObserver, observer)
	return &prepared{
		path:       path,
		closeTrace: trace.Close,
		binding:    wrapBinding(request.Device, observer, runtime),
		observer:   runtime,
		timeout:    closeTimeout(request.CloseTimeout),
		closed:     make(chan struct{}),
	}, nil
}

type prepared struct {
	path       string
	binding    sessiontrace.DeviceBinding
	observer   sessiontrace.RuntimeObserver
	timeout    time.Duration
	once       sync.Once
	closed     chan struct{}
	closeErr   error
	closeTrace func() error
}

func (p *prepared) DeviceBinding() sessiontrace.DeviceBinding     { return p.binding }
func (p *prepared) RuntimeObserver() sessiontrace.RuntimeObserver { return p.observer }
func (p *prepared) StagedPath() string                            { return p.path }

func (p *prepared) Finish(ctx context.Context, bundle string, published bool) error {
	if ctx == nil {
		return errors.New("session trace finish context is required")
	}
	closeErr := p.close(ctx)
	if closeErr != nil {
		return p.retain(bundle, closeErr)
	}
	if !published {
		return p.retain(bundle, nil)
	}
	if bundle == "" {
		return nil
	}
	destination := filepath.Join(bundle, "audio-trace")
	claimPath := destination + ".claim"
	claim, err := os.OpenFile(claimPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, claimFileMode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return p.retain(bundle, sessiontrace.ErrDestinationExists)
		}
		return p.retain(bundle, err)
	}
	if err := claim.Close(); err != nil {
		removeClaim(claimPath)
		return p.retain(bundle, err)
	}
	defer removeClaim(claimPath)
	if _, err := os.Lstat(destination); err == nil {
		return p.retain(bundle, sessiontrace.ErrDestinationExists)
	} else if !errors.Is(err, os.ErrNotExist) {
		return p.retain(bundle, err)
	}
	if err := os.Rename(p.path, destination); err != nil {
		return p.retain(bundle, fmt.Errorf("attach audio trace to bundle: %w", err))
	}
	return nil
}

func (p *prepared) close(ctx context.Context) error {
	p.once.Do(func() {
		go func() {
			p.closeErr = p.closeTrace()
			close(p.closed)
		}()
	})
	timer := time.NewTimer(p.timeout)
	defer timer.Stop()
	select {
	case <-p.closed:
		return p.closeErr
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("%w after %s", sessiontrace.ErrCloseTimeout, p.timeout)
	}
}

func (p *prepared) retain(bundle string, err error) error {
	return errors.Join(err, fmt.Errorf("audio evidence retained at %s", p.path))
}

func removeClaim(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
}

func closeTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return sessiontrace.DefaultCloseTimeout
	}
	return timeout
}

type traceObserver struct {
	trace       *recording.Trace
	credentials []string
}

func (*traceObserver) ObserveProviderBoundaries() bool { return true }
func (*traceObserver) RetainCommitPayload() bool       { return false }

func (o *traceObserver) ObserveSessionRuntime(observation sessiontrace.RuntimeObservation) {
	payload := observation.Payload
	errText := observation.Error
	if len(o.credentials) > 0 {
		if runtimePayloadNeedsRedaction(observation.Kind) {
			payload = append([]byte(nil), payload...)
		}
		for _, secret := range o.credentials {
			if secret == "" {
				continue
			}
			errText = strings.ReplaceAll(errText, secret, "[REDACTED]")
			if runtimePayloadNeedsRedaction(observation.Kind) {
				payload = bytes.ReplaceAll(payload, []byte(secret), []byte("[REDACTED]"))
			}
		}
	}
	o.trace.ObserveRuntime(recording.RuntimeEvent{Kind: string(observation.Kind), Tick: observation.Tick, InputCommit: observation.InputCommit, ResponseID: observation.ResponseID, ResponsePurpose: string(observation.ResponsePurpose), StreamID: observation.StreamID, LoopPassID: observation.LoopPassID, Epoch: observation.Epoch, TurnsCompleted: observation.TurnsCompleted, Clean: observation.Clean, Error: errText, Payload: payload})
}

func runtimePayloadNeedsRedaction(kind sessiontrace.SessionRuntimeObservationKind) bool {
	switch kind {
	case "tool_call", "tool_result", "provider_wire_send", "provider_wire_receive":
		return true
	case sessiontrace.SessionRuntimeObservationAudioOutput,
		sessiontrace.SessionRuntimeObservationAudioInput,
		sessiontrace.SessionRuntimeObservationAudioPlaybackReceipt,
		sessiontrace.SessionRuntimeObservationAudioRenderTapUnavailable,
		sessiontrace.SessionRuntimeObservationInputCommit,
		sessiontrace.SessionRuntimeObservationResponseCreate,
		sessiontrace.SessionRuntimeObservationTurnCompleted,
		sessiontrace.SessionRuntimeObservationTerminal:
		return false
	default:
		return false
	}
}

type observerChain []sessiontrace.RuntimeObserver

func (c observerChain) ObserveSessionRuntime(observation sessiontrace.RuntimeObservation) {
	for _, observer := range c {
		if observer == nil {
			continue
		}
		copyObservation := observation
		copyObservation.Payload = append([]byte(nil), observation.Payload...)
		if observation.FinalAccounting != nil {
			accounting := *observation.FinalAccounting
			copyObservation.FinalAccounting = &accounting
		}
		observer.ObserveSessionRuntime(copyObservation)
	}
}

func (c observerChain) ObserveProviderBoundaries() bool {
	for _, observer := range c {
		if preference, ok := observer.(sessiontrace.ProviderBoundaryObserver); ok && preference.ObserveProviderBoundaries() {
			return true
		}
	}
	return false
}

func (c observerChain) RetainCommitPayload() bool {
	for _, observer := range c {
		if _, isTrace := observer.(*traceObserver); isTrace {
			continue
		}
		preference, ok := observer.(sessiontrace.CommitPayloadObserver)
		if !ok || preference.RetainCommitPayload() {
			return true
		}
	}
	return false
}

func combineObservers(observers ...sessiontrace.RuntimeObserver) sessiontrace.RuntimeObserver {
	filtered := make(observerChain, 0, len(observers))
	for _, observer := range observers {
		if observer != nil {
			filtered = append(filtered, observer)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func wrapBinding(binding sessiontrace.DeviceBinding, trace *traceObserver, runtime sessiontrace.RuntimeObserver) sessiontrace.DeviceBinding {
	priorPreGate := binding.PreGateSamplesObserver
	binding.PreGateSamplesObserver = func(rate int, samples []int16) {
		trace.trace.CaptureMicrophonePreGate(rate, samples)
		if priorPreGate != nil {
			priorPreGate(rate, samples)
		}
	}
	priorUploaded := binding.UploadedSamplesObserver
	binding.UploadedSamplesObserver = func(rate int, samples []int16) {
		trace.trace.CaptureMicrophoneUploaded(rate, samples)
		if priorUploaded != nil {
			priorUploaded(rate, samples)
		}
	}
	priorPlayback := binding.PlaybackSamplesObserver
	binding.PlaybackSamplesObserver = func(ctx context.Context, rate int, samples []int16) error {
		traceErr := trace.trace.CaptureSpeakerEnqueued(ctx, rate, samples)
		if priorPlayback != nil {
			return errors.Join(traceErr, priorPlayback(ctx, rate, samples))
		}
		return traceErr
	}
	priorRendered := binding.RenderedSamplesObserver
	binding.RenderedSamplesObserver = func(rate int, samples []int16) {
		trace.trace.CaptureSpeakerRendered(rate, samples)
		if priorRendered != nil {
			priorRendered(rate, samples)
		}
	}
	priorUnavailable := binding.RenderedSamplesUnavailable
	binding.RenderedSamplesUnavailable = func() {
		runtime.ObserveSessionRuntime(sessiontrace.RuntimeObservation{Kind: "audio_render_tap_unavailable", Clean: false, Error: "selected audio backend does not expose physical render callbacks"})
		if priorUnavailable != nil {
			priorUnavailable()
		}
	}
	return binding
}
