package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
)

func NewLiveRecorder(options sessiontrace.LiveRecorderOptions) session.LiveRecorder {
	return &liveRecorder{
		inner:      options.Inner,
		observer:   options.Observer,
		inputRate:  options.InputRate,
		outputRate: options.OutputRate,
	}
}

type liveRecorder struct {
	inner                 session.LiveRecorder
	observer              sessiontrace.RuntimeObserver
	inputRate, outputRate int
	sequence              atomic.Uint64
}

func (r *liveRecorder) RecordMessage(ctx context.Context, record session.LiveRecord) error {
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

func (r *liveRecorder) RecordAudio(ctx context.Context, record session.LiveAudioRecord) error {
	innerErr := error(nil)
	if r != nil && r.inner != nil {
		innerErr = r.inner.RecordAudio(ctx, record)
	}
	if r == nil {
		return innerErr
	}
	return errors.Join(innerErr, r.observeAudio(record))
}

func (r *liveRecorder) RecordEvent(ctx context.Context, event session.LiveEvent) error {
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

func (r *liveRecorder) Finalize(ctx context.Context, runErr error) error {
	if r == nil {
		return nil
	}
	if r.observer != nil {
		r.observe(sessiontrace.SessionRuntimeObservation{
			Kind:  sessiontrace.SessionRuntimeObservationTerminal,
			Clean: runErr == nil,
			Error: traceErrorText(runErr),
		})
	}
	if r.inner == nil {
		return nil
	}
	return r.inner.Finalize(ctx, runErr)
}

func (r *liveRecorder) observeMessage(record session.LiveRecord) {
	payload, marshalErr := json.Marshal(record.Message)
	clean := marshalErr == nil
	if marshalErr != nil {
		payload = nil
	}
	direction := "receive"
	if record.Direction == session.LiveRecordClient {
		direction = "send"
	}
	r.observe(sessiontrace.SessionRuntimeObservation{
		Kind:            sessiontrace.SessionRuntimeObservationKind("provider_wire_" + direction),
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
		r.observe(sessiontrace.SessionRuntimeObservation{
			Kind:            sessiontrace.SessionRuntimeObservationKind(special),
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

func (r *liveRecorder) observeAudio(record session.LiveAudioRecord) error {
	rate := record.Frame.Format.SampleRate
	if rate <= 0 {
		if record.Direction == session.LiveRecordClient {
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
	kind := sessiontrace.SessionRuntimeObservationAudioOutput
	if record.Direction == session.LiveRecordClient {
		kind = sessiontrace.SessionRuntimeObservationAudioInput
	}
	r.observe(sessiontrace.SessionRuntimeObservation{
		Kind:    kind,
		Payload: payload,
		Epoch:   record.Frame.Epoch,
		Clean:   marshalErr == nil,
		Error:   traceErrorText(marshalErr),
	})
	return marshalErr
}

func (r *liveRecorder) observeEvent(event session.LiveEvent) {
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
	if event.Kind == string(session.LiveEventTerminal) {
		return
	}
	r.observe(sessiontrace.SessionRuntimeObservation{
		Kind:      sessiontrace.SessionRuntimeObservationKind(event.Kind),
		Timestamp: event.Timestamp,
		Payload:   payload,
		Clean:     event.Error == nil && marshalErr == nil,
		Error:     traceErrorText(event.Error),
	})
}

func (r *liveRecorder) observe(observation sessiontrace.SessionRuntimeObservation) {
	if r == nil || r.observer == nil {
		return
	}
	observation.Tick = r.sequence.Add(1)
	r.observer.ObserveSessionRuntime(observation)
}

func traceMessageKind(message messages.StreamMessage) string {
	switch {
	case message.Type == messages.StreamTypeAudioStart || message.Type == messages.StreamTypeAudioDelta || message.Type == messages.StreamTypeAudioEnd:
		if message.Role == messages.RoleTool {
			return "tool_result"
		}
		return string(sessiontrace.SessionRuntimeObservationAudioOutput)
	case message.Type == messages.StreamTypeResponseCreate:
		return string(sessiontrace.SessionRuntimeObservationResponseCreate)
	case message.Type == messages.StreamTypeInputItemAdded || message.Type == messages.StreamTypeMessageEnd && message.Role == messages.RoleUser:
		return string(sessiontrace.SessionRuntimeObservationInputCommit)
	case message.Type == messages.StreamTypeMessageEnd:
		return string(sessiontrace.SessionRuntimeObservationTurnCompleted)
	case message.Type == messages.StreamTypeSessionClose:
		return string(sessiontrace.SessionRuntimeObservationTerminal)
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
