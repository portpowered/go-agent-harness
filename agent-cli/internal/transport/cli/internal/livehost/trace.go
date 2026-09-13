package livehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeRecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeSessionTrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
)

const (
	websocketTextMessage               = 1
	websocketBinaryMessage             = 2
	liveTraceDirectoryMode os.FileMode = 0o755
)

// liveTraceRecorder keeps the livehost path on the same staged trace contract
// as the legacy session dispatcher. The semantic recorder remains the owner
// of the record-dir bundle; this wrapper owns only the independently staged
// audio trace and projects the already-published raw provider capture into its
// runtime timeline before it is attached.
type liveTraceRecorder struct {
	inner        runtimeSession.LiveRecorder
	trace        *recording.Trace
	tracePath    string
	destination  string
	providerPath string
	providerRate int

	finalizeOnce sync.Once
	finalizeErr  error
}

func newLiveTraceRecorder(inner runtimeSession.LiveRecorder, destination, providerPath string, providerRate int, source clock.Source) (*liveTraceRecorder, error) {
	if strings.TrimSpace(destination) == "" {
		return nil, errors.New("live trace recorder destination is empty")
	}
	if source == nil {
		return nil, errors.New("live trace clock is required")
	}
	parent := filepath.Dir(filepath.Clean(destination))
	if err := os.MkdirAll(parent, liveTraceDirectoryMode); err != nil {
		return nil, fmt.Errorf("create live trace parent: %w", err)
	}
	tracePath, err := os.MkdirTemp(parent, ".session-audio-trace-")
	if err != nil {
		return nil, fmt.Errorf("stage live audio trace: %w", err)
	}
	trace, err := recording.NewTrace(tracePath, source)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open live audio trace: %w", err), os.RemoveAll(tracePath))
	}
	return &liveTraceRecorder{
		inner:        inner,
		trace:        trace,
		tracePath:    tracePath,
		destination:  destination,
		providerPath: strings.TrimSpace(providerPath),
		providerRate: providerRate,
	}, nil
}

func liveTraceSource(scheduler clock.Scheduler) clock.Source {
	if scheduler != nil {
		return scheduler
	}
	return clock.Real{}
}

func (r *liveTraceRecorder) RecordMessage(ctx context.Context, record runtimeSession.LiveRecord) error {
	if r == nil {
		return runtimeRecording.ErrLiveEvidenceClosed
	}
	if r.inner == nil {
		return nil
	}
	return r.inner.RecordMessage(ctx, record)
}

func (r *liveTraceRecorder) RecordAudio(ctx context.Context, record runtimeSession.LiveAudioRecord) error {
	if r == nil {
		return runtimeRecording.ErrLiveEvidenceClosed
	}
	innerErr := error(nil)
	if r.inner != nil {
		innerErr = r.inner.RecordAudio(ctx, record)
	}
	traceErr := r.captureAudio(ctx, record)
	return errors.Join(innerErr, traceErr)
}

func (r *liveTraceRecorder) RecordEvent(ctx context.Context, event runtimeSession.LiveEvent) error {
	if r == nil {
		return runtimeRecording.ErrLiveEvidenceClosed
	}
	if r.inner == nil {
		return nil
	}
	return r.inner.RecordEvent(ctx, event)
}

func (r *liveTraceRecorder) captureAudio(ctx context.Context, record runtimeSession.LiveAudioRecord) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if r.trace == nil || len(record.Frame.Samples) == 0 {
		return nil
	}
	rate := record.Frame.Format.SampleRate
	if record.Direction == runtimeSession.LiveRecordAgent && r.providerRate > 0 {
		rate = r.providerRate
	}
	if rate <= 0 {
		return fmt.Errorf("live audio trace sample rate is unavailable")
	}
	samples := record.Frame.Samples
	switch record.Direction {
	case runtimeSession.LiveRecordClient:
		if record.Admission == runtimeSession.LiveAudioQueueAdmitted {
			r.trace.CaptureMicrophonePreGate(rate, samples)
		} else {
			r.trace.CaptureMicrophoneUploaded(rate, samples)
		}
	case runtimeSession.LiveRecordAgent:
		return r.trace.CaptureSpeakerEnqueued(ctx, rate, samples)
	}
	return nil
}

func (r *liveTraceRecorder) Finalize(ctx context.Context, runErr error) error {
	if r == nil {
		return nil
	}
	r.finalizeOnce.Do(func() {
		innerErr := error(nil)
		if r.inner != nil {
			innerErr = r.inner.Finalize(ctx, runErr)
		}
		projectionErr := error(nil)
		if innerErr == nil && runErr == nil && r.providerCapturePath() != "" {
			projectionErr = projectProviderCapture(r.trace, r.providerCapturePath())
		}
		closeErr := r.trace.Close()
		result := errors.Join(runErr, innerErr, projectionErr, closeErr)
		if result == nil {
			result = attachLiveTrace(r.tracePath, r.destination)
		} else {
			result = errors.Join(result, fmt.Errorf("live session audio trace retained at %s", r.tracePath))
		}
		r.finalizeErr = result
	})
	return r.finalizeErr
}

func (r *liveTraceRecorder) providerCapturePath() string {
	if r == nil {
		return ""
	}
	return r.providerPath
}

func attachLiveTrace(staged, bundle string) error {
	destination := filepath.Join(bundle, "audio-trace")
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("audio trace destination exists; evidence retained at %s", staged)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect audio trace destination: %w; evidence retained at %s", err, staged)
	}
	if err := os.Rename(staged, destination); err != nil {
		return fmt.Errorf("attach audio trace: %w; evidence retained at %s", err, staged)
	}
	return nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

var _ runtimeSession.LiveRecorder = (*liveTraceRecorder)(nil)

type publicTraceRecorder struct {
	inner                 runtimeSession.LiveRecorder
	run                   *publicTraceRun
	binding               runtimeSessionTrace.DeviceBinding
	observer              runtimeSessionTrace.RuntimeObserver
	inputRate, outputRate int
	sequence              *atomic.Uint64
}

func (r *publicTraceRecorder) RecordMessage(ctx context.Context, record runtimeSession.LiveRecord) error {
	innerErr := error(nil)
	if r != nil && r.inner != nil {
		innerErr = r.inner.RecordMessage(ctx, record)
	}
	if r == nil {
		return innerErr
	}
	r.observeMessage(record)
	return innerErr
}

func (r *publicTraceRecorder) RecordAudio(ctx context.Context, record runtimeSession.LiveAudioRecord) error {
	innerErr := error(nil)
	if r != nil && r.inner != nil {
		innerErr = r.inner.RecordAudio(ctx, record)
	}
	if r == nil {
		return innerErr
	}
	frame := record.Frame
	frame.Samples = append([]int16(nil), record.Frame.Samples...)
	record.Frame = frame
	traceErr := r.observeAudio(ctx, record)
	return errors.Join(innerErr, traceErr)
}

func (r *publicTraceRecorder) RecordEvent(ctx context.Context, event runtimeSession.LiveEvent) error {
	innerErr := error(nil)
	if r != nil && r.inner != nil {
		innerErr = r.inner.RecordEvent(ctx, event)
	}
	if r == nil {
		return innerErr
	}
	r.observeEvent(event)
	return innerErr
}

func (r *publicTraceRecorder) Finalize(ctx context.Context, runErr error) error {
	if r == nil {
		return nil
	}
	if r.run != nil {
		r.run.observeTerminal(runErr)
	}
	if r.inner == nil {
		return nil
	}
	return r.inner.Finalize(ctx, runErr)
}

func (r *publicTraceRecorder) observeMessage(record runtimeSession.LiveRecord) {
	payload, marshalErr := json.Marshal(record.Message)
	clean := marshalErr == nil
	if marshalErr != nil {
		payload = nil
	}
	direction := "receive"
	if record.Direction == runtimeSession.LiveRecordClient {
		direction = "send"
	}
	r.observe(runtimeSessionTrace.SessionRuntimeObservation{
		Kind:            runtimeSessionTrace.SessionRuntimeObservationKind("provider_wire_" + direction),
		Timestamp:       record.Timestamp,
		Payload:         payload,
		ResponseID:      record.Message.ResponseID,
		ResponsePurpose: record.Message.ResponsePurpose,
		StreamID:        record.Message.ActorStreamID,
		LoopPassID:      record.Message.LoopPassID,
		Clean:           clean,
		Error:           traceErrorText(marshalErr),
	})
	if special := traceMessageKind(record.Message); special != "" {
		r.observe(runtimeSessionTrace.SessionRuntimeObservation{
			Kind:            runtimeSessionTrace.SessionRuntimeObservationKind(special),
			Timestamp:       record.Timestamp,
			Payload:         payload,
			ResponseID:      record.Message.ResponseID,
			ResponsePurpose: record.Message.ResponsePurpose,
			StreamID:        record.Message.ActorStreamID,
			LoopPassID:      record.Message.LoopPassID,
			Clean:           clean,
			Error:           traceErrorText(marshalErr),
		})
	}
}

func (r *publicTraceRecorder) observeAudio(ctx context.Context, record runtimeSession.LiveAudioRecord) error {
	rate := record.Frame.Format.SampleRate
	if rate <= 0 {
		if record.Direction == runtimeSession.LiveRecordClient {
			rate = r.inputRate
		} else {
			rate = r.outputRate
		}
	}
	samples := append([]int16(nil), record.Frame.Samples...)
	payload, marshalErr := json.Marshal(struct {
		Direction string `json:"direction"`
		Admission string `json:"admission"`
		Rate      int    `json:"sample_rate"`
		Samples   int    `json:"sample_count"`
		StreamID  string `json:"stream_id,omitempty"`
		Epoch     uint64 `json:"epoch,omitempty"`
	}{string(record.Direction), string(record.Admission), rate, len(samples), record.Frame.StreamID, record.Frame.Epoch})
	kind := runtimeSessionTrace.SessionRuntimeObservationAudioOutput
	if record.Direction == runtimeSession.LiveRecordClient {
		kind = runtimeSessionTrace.SessionRuntimeObservationAudioInput
	}
	r.observe(runtimeSessionTrace.SessionRuntimeObservation{
		Kind:    kind,
		Payload: payload,
		Epoch:   record.Frame.Epoch,
		Clean:   marshalErr == nil,
		Error:   traceErrorText(marshalErr),
	})
	return marshalErr
}

func (r *publicTraceRecorder) observeEvent(event runtimeSession.LiveEvent) {
	payload, marshalErr := json.Marshal(struct {
		Sequence      uint64 `json:"sequence"`
		Kind          string `json:"kind"`
		SessionID     string `json:"session_id,omitempty"`
		ParticipantID string `json:"participant_id,omitempty"`
		ResponseID    string `json:"response_id,omitempty"`
		ItemID        string `json:"item_id,omitempty"`
		ToolCallID    string `json:"tool_call_id,omitempty"`
		State         string `json:"state,omitempty"`
		Reason        string `json:"reason,omitempty"`
		Dropped       uint64 `json:"dropped,omitempty"`
	}{event.Sequence, event.Kind, event.SessionID, event.ParticipantID, event.ResponseID, event.ItemID, event.ToolCallID, event.State, event.Reason, event.Dropped})
	kind := event.Kind
	if event.Kind == string(runtimeSession.LiveEventTerminal) {
		if r.run != nil {
			r.run.observeTerminal(event.Error)
		}
		return
	}
	r.observe(runtimeSessionTrace.SessionRuntimeObservation{
		Kind:      runtimeSessionTrace.SessionRuntimeObservationKind(kind),
		Timestamp: event.Timestamp,
		Payload:   payload,
		Clean:     event.Error == nil && marshalErr == nil,
		Error:     traceErrorText(event.Error),
	})
}

func (r *publicTraceRecorder) observe(observation runtimeSessionTrace.SessionRuntimeObservation) {
	if r == nil || r.observer == nil {
		return
	}
	if r.sequence != nil {
		observation.Tick = r.sequence.Add(1)
	}
	r.observer.ObserveSessionRuntime(observation)
}

func traceMessageKind(message messages.StreamMessage) string {
	switch {
	case message.Type == messages.StreamTypeAudioStart || message.Type == messages.StreamTypeAudioDelta || message.Type == messages.StreamTypeAudioEnd:
		if message.Role == messages.RoleTool {
			return "tool_result"
		}
		return string(runtimeSessionTrace.SessionRuntimeObservationAudioOutput)
	case message.Type == messages.StreamTypeResponseCreate:
		return string(runtimeSessionTrace.SessionRuntimeObservationResponseCreate)
	case message.Type == messages.StreamTypeInputItemAdded || message.Type == messages.StreamTypeMessageEnd && message.Role == messages.RoleUser:
		return string(runtimeSessionTrace.SessionRuntimeObservationInputCommit)
	case message.Type == messages.StreamTypeMessageEnd:
		return string(runtimeSessionTrace.SessionRuntimeObservationTurnCompleted)
	case message.Type == messages.StreamTypeSessionClose:
		return string(runtimeSessionTrace.SessionRuntimeObservationTerminal)
	case message.Type == messages.StreamTypeToolCallStart || message.Type == messages.StreamTypeToolCallDelta || message.Type == messages.StreamTypeToolCallEnd:
		return "tool_call"
	case message.Role == messages.RoleTool:
		return "tool_result"
	default:
		return ""
	}
}

func traceErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
