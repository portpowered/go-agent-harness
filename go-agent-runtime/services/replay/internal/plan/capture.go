package plan

// This file contains the small capture-derived plan used by self-driving
// replay. Provider handshake bytes stay with the provider service; this plan
// only recovers the first text action or bounded audio append chunks needed to
// drive the reusable live session.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	replaycapture "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/internal/capture"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/internal/engine"
)

const (
	replayAudioChunkLimit = 96 * 1024
	replayAppend          = "input_audio_buffer.append"
	replaySessionUpdate   = "session.update"
	replayCreateItem      = "conversation.item.create"
	replayTruncateItem    = "conversation.item.truncate"
)

// errSelfDrivingPlanUnavailable distinguishes a valid realtime capture whose
// client actions require the caller to drive them from a malformed capture.
// Inspection can still classify the former, while the explicit self-driving
// loader keeps its documented empty-plan behavior for that shape.
var errSelfDrivingPlanUnavailable = errors.New("self-driving replay plan unavailable")

type Service struct{}

type captureReplay struct {
	inner *engine.MessageSession
}

// New constructs an inert replay planner.
func New() *Service { return &Service{} }

func (*Service) OpenHTTPReplay(path string) (any, error) {
	return replaycapture.NewHTTPReplay(path)
}

func (*Service) EncodeStreamMessage(message messages.StreamMessage) ([]byte, error) {
	return replaycapture.MarshalStreamMessage(message)
}

func (*Service) DecodeStreamMessage(data []byte) (messages.StreamMessage, error) {
	return replaycapture.UnmarshalStreamMessage(json.RawMessage(data))
}

func (s *Service) Replay(ctx context.Context, path string) (replay.CaptureReplay, error) {
	if err := replayContextError(ctx); err != nil {
		return nil, err
	}
	capturePath, err := s.ResolveCapturePath(ctx, path)
	if err != nil {
		return nil, err
	}
	loaded, err := loadReplayCapture(ctx, capturePath)
	if err != nil {
		return nil, fmt.Errorf("replay session capture %s: %w", path, err)
	}
	return &captureReplay{inner: engine.NewMessageSession(loaded.Capture.Records, ctx, false)}, nil
}

func (s *Service) NewSessionInferencer(ctx context.Context, path string) (messages.SessionInferencer, error) {
	if err := replayContextError(ctx); err != nil {
		return nil, err
	}
	capturePath, err := s.ResolveCapturePath(ctx, path)
	if err != nil {
		return nil, err
	}
	return sessionCaptureInferencer{path: capturePath}, nil
}

func (s *Service) AnalyzeTiming(ctx context.Context, path string) (replay.CaptureTimingReport, error) {
	if err := replayContextError(ctx); err != nil {
		return replay.CaptureTimingReport{}, err
	}
	capturePath, err := s.ResolveCapturePath(ctx, path)
	if err != nil {
		return replay.CaptureTimingReport{}, err
	}
	loaded, err := loadReplayCapture(ctx, capturePath)
	if err != nil {
		return replay.CaptureTimingReport{}, fmt.Errorf("load capture timing source %s: %w", path, err)
	}
	return analyzeCaptureTiming(loaded.Capture)
}

type sessionCaptureInferencer struct{ path string }

func (i sessionCaptureInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if err := replayContextError(ctx); err != nil {
		return nil, err
	}
	loaded, err := loadReplayCapture(ctx, i.path)
	if err != nil {
		return nil, err
	}
	return engine.NewMessageSession(loaded.Capture.Records, ctx, true), nil
}

func (r *captureReplay) Receive() <-chan messages.StreamMessage {
	if r == nil || r.inner == nil {
		return nil
	}
	return r.inner.Receive().Chan()
}

func (r *captureReplay) Drain(ctx context.Context, consume func(messages.StreamMessage) error) error {
	if err := replayContextError(ctx); err != nil {
		return err
	}
	if r == nil || r.inner == nil {
		return errors.New("replay capture is unavailable")
	}
	if consume == nil {
		return errors.New("replay message consumer is required")
	}
	input := r.Receive()
	done := r.Done()
	if input == nil || done == nil {
		return errors.New("replay capture stream is unavailable")
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return r.drainReady(ctx, input, consume)
		case message, ok := <-input:
			if !ok {
				return r.Err()
			}
			if err := consume(message); err != nil {
				return err
			}
		}
	}
}

func (r *captureReplay) drainReady(ctx context.Context, input <-chan messages.StreamMessage, consume func(messages.StreamMessage) error) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case message, ok := <-input:
			if !ok {
				return r.Err()
			}
			if err := consume(message); err != nil {
				return err
			}
		default:
			return r.Err()
		}
	}
}

func (r *captureReplay) Done() <-chan struct{} {
	if r == nil || r.inner == nil {
		return nil
	}
	return r.inner.Done()
}

func (r *captureReplay) Err() error {
	if r == nil || r.inner == nil {
		return nil
	}
	return r.inner.Err()
}

func (r *captureReplay) Close() error {
	if r == nil || r.inner == nil {
		return nil
	}
	return r.inner.Close()
}

// InspectCapture performs the complete replay admission once and returns the
// host-facing metadata needed to select a session route. This keeps capture
// format detection, provider metadata, integrity warnings, and self-driving
// planning behind the replay service boundary.
func (s *Service) InspectCapture(ctx context.Context, path string) (replay.CaptureInspection, error) {
	if err := replayContextError(ctx); err != nil {
		return replay.CaptureInspection{}, err
	}
	sourcePath := path
	capturePath, err := s.ResolveCapturePath(ctx, path)
	if err != nil {
		return replay.CaptureInspection{}, err
	}
	loaded, err := loadReplayCapture(ctx, capturePath)
	if err != nil {
		return replay.CaptureInspection{}, fmt.Errorf("inspect live replay %s: %w", sourcePath, err)
	}
	inspection := replay.CaptureInspection{
		SourcePath:       sourcePath,
		CapturePath:      capturePath,
		Kind:             captureKind(loaded.Capture.Records),
		Provider:         loaded.Capture.Provider.Name,
		Model:            loaded.Capture.Provider.Model,
		IntegrityWarning: loaded.IntegrityWarning(sourcePath),
		Facts:            captureFacts(loaded.Capture),
	}
	inspection.InitialTools, inspection.InitialToolsKnown = initialToolNames(loaded.Capture.Records)
	if !inspection.IsRealtime() {
		return inspection, nil
	}
	if err := engine.ValidateWebSocketCapture(loaded.Capture); err != nil {
		return replay.CaptureInspection{}, fmt.Errorf("validate realtime replay capture %s: %w", sourcePath, err)
	}
	inspection.Facts.RealtimeWebSocketReplayable = true
	plan, err := loadLivePlanFromCapture(ctx, capturePath, loaded.Capture)
	if err != nil {
		if !errors.Is(err, errSelfDrivingPlanUnavailable) {
			return inspection, err
		}
		// Preserve valid opening actions while leaving response-target correlation
		// unavailable; the strict loader still rejects incomplete self-driving plans.
		fallback, fallbackErr := replayCallerActionPlan(capturePath, loaded.Capture.Records)
		if fallbackErr != nil {
			return inspection, fallbackErr
		}
		inspection.LivePlan = &fallback
		return inspection, nil
	}
	if err != nil {
		return replay.CaptureInspection{}, err
	}
	inspection.LivePlan = &plan
	return inspection, nil
}

// TraceCapture admits a provider capture and returns only the copied event
// projection needed by the host audio trace. Capture format and integrity
// validation remain inside replay; the returned payloads cannot mutate the
// admitted source.
func (s *Service) TraceCapture(ctx context.Context, path string) ([]replay.CaptureTraceEvent, error) {
	if err := replayContextError(ctx); err != nil {
		return nil, err
	}
	capturePath, err := s.ResolveCapturePath(ctx, path)
	if err != nil {
		return nil, err
	}
	loaded, err := loadReplayCapture(ctx, capturePath)
	if err != nil {
		return nil, fmt.Errorf("trace replay capture %s: %w", path, err)
	}
	events := make([]replay.CaptureTraceEvent, 0, len(loaded.Capture.Records))
	for _, record := range loaded.Capture.Records {
		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
		events = append(events, replay.CaptureTraceEvent{
			Sequence:  record.Sequence,
			Direction: string(record.Direction),
			Type:      record.Type,
			Payload:   append([]byte(nil), payload...),
		})
	}
	return events, nil
}

func loadReplayCapture(ctx context.Context, path string) (replaycapture.ReplayLoad, error) {
	return replaycapture.LoadReplayCapture(ctx, path)
}

func replayContextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("replay planning requires a context")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return nil
}
