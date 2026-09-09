// Package strict implements the complete strict offline replay workflow.
package strict

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/internal/plan"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	providerWireSend    = "provider_wire_send"
	providerWireReceive = "provider_wire_receive"
	toolCallKind        = "tool_call"
	toolResultKind      = "tool_result"
	runtimeEventKind    = "runtime"
	sessionUpdateType   = "session.update"
	sessionClosedType   = "session.closed"
)

// ClockFactory creates a fresh virtual clock for each prepared replay. The
// origin comes from the bundle's recording epoch; sharing the returned clock
// across Prepare calls would couple otherwise independent runs.
type ClockFactory func(time.Time) *clock.Deterministic

type Dependencies struct {
	ClockFactory ClockFactory
	Runtime      replay.RuntimeFactory
	Admission    replay.CaptureAdmission
}

type Service struct {
	clockFactory ClockFactory
	runtime      replay.RuntimeFactory
	admission    replay.CaptureAdmission
}

var _ replay.StrictService = (*Service)(nil)

func New(deps Dependencies) *Service {
	admission := deps.Admission
	if admission == nil {
		admission = plan.New()
	}
	return &Service{clockFactory: deps.ClockFactory, runtime: deps.Runtime, admission: admission}
}

func (s *Service) Run(ctx context.Context, out io.Writer, request replay.Request) (replay.Result, error) {
	if out == nil {
		return replay.Result{}, fmt.Errorf("%w: output is nil", replay.ErrRuntimeFactoryRequired)
	}
	if s == nil || s.runtime == nil {
		return replay.Result{}, replay.ErrRuntimeFactoryRequired
	}
	prepared, err := s.Prepare(ctx, request)
	if err != nil {
		return replay.Result{}, err
	}
	runtime, err := s.runtime.New(prepared)
	if err != nil {
		return replay.Result{}, errors.Join(fmt.Errorf("construct offline replay runtime: %w", err), prepared.ValidateComplete())
	}
	if runtime == nil {
		return replay.Result{}, errors.Join(replay.ErrRuntimeFactoryRequired, prepared.ValidateComplete())
	}
	runErr := runtime.Run(ctx, out)
	validationErr := prepared.ValidateComplete()
	result := replay.Result{Capture: prepared.Capture, Scope: prepared.Scope, WireEvents: prepared.WireEvents, ToolCalls: prepared.ToolCalls}
	return result, errors.Join(runErr, validationErr)
}

func (s *Service) Prepare(ctx context.Context, request replay.Request) (replay.Prepared, error) {
	if err := contextError(ctx); err != nil {
		return replay.Prepared{}, err
	}
	if s == nil || s.clockFactory == nil {
		return replay.Prepared{}, replay.ErrDeterministicClockRequired
	}
	bundlePath := strings.TrimSpace(request.BundlePath)
	if bundlePath == "" {
		return replay.Prepared{}, fmt.Errorf("%w: bundle path is empty", replay.ErrBundleIncomplete)
	}
	tracePath, err := prepareTraceDirectory(ctx, bundlePath, s.admission)
	if err != nil {
		return replay.Prepared{}, err
	}
	events, err := readTimeline(tracePath)
	if err != nil {
		return replay.Prepared{}, err
	}
	origin, err := timelineOrigin(events)
	if err != nil {
		return replay.Prepared{}, err
	}
	deterministic := s.clockFactory(origin)
	if deterministic == nil {
		return replay.Prepared{}, replay.ErrDeterministicClockRequired
	}
	audioReplay, err := recording.OpenReplay(tracePath)
	if err != nil {
		return replay.Prepared{}, fmt.Errorf("%w: open audio trace: %w", replay.ErrBundleIncomplete, err)
	}
	audioReplay.Clock = deterministic
	capture, toolExecutor, wireTypes, wireCount, toolCount, err := deriveEvidence(events, request)
	if err != nil {
		return replay.Prepared{}, err
	}
	dialer, err := gwtesting.NewReplayWebSocketDialerFromCapture(capture)
	if err != nil {
		return replay.Prepared{}, fmt.Errorf("%w: construct replay dialer: %w", replay.ErrBundleMismatch, err)
	}
	state := &replayState{expected: wireCount, messageTypes: wireTypes}
	trackedDialer := transport.Dialer(&trackingDialer{inner: dialer, state: state})
	return replay.StrictPreparedBuilder{}.Build(
		capture,
		trackedDialer,
		toolExecutor,
		audioReplay,
		deterministic,
		deriveScope(events),
		wireCount,
		toolCount,
		func() error {
			if err := state.validate(); err != nil {
				return err
			}
			return toolExecutor.validateComplete()
		},
	), nil
}

func deriveScope(events []recording.Event) replay.EvidenceScope {
	scope := replay.EvidenceScope{Protocol: true, Tools: true}
	for _, event := range events {
		applyScopeEvent(&scope, event)
	}
	return scope
}

func applyScopeEvent(scope *replay.EvidenceScope, event recording.Event) {
	if event.Kind == "audio" {
		scope.RecordedPCM = true
		scope.RecordedRender = scope.RecordedRender || event.Tap == "speaker_rendered"
	}
	if event.Kind == runtimeEventKind && event.RuntimeKind == "audio_render_tap_unavailable" {
		scope.RenderTapUnavailable = true
	}
	if event.Kind == runtimeEventKind && event.RuntimeKind == providerWireSend && recordedProviderAudio(event.Payload) {
		scope.RecordedPCM = true
	}
}

func recordedProviderAudio(payload []byte) bool {
	_, _, wireType, err := decodeWireEnvelope(payload)
	return err == nil && wireType == "input_audio_buffer.append"
}
