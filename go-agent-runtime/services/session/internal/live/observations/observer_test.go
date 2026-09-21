package observations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessiontrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/stretchr/testify/require"
)

type memoryRecorder struct {
	messages []session.LiveRecord
	frames   []session.LiveAudioRecord
	events   []session.LiveEvent
	contexts []context.Context
	err      error
}

func (r *memoryRecorder) RecordMessage(ctx context.Context, value session.LiveRecord) error {
	r.messages = append(r.messages, value)
	r.contexts = append(r.contexts, ctx)
	return r.err
}

func (r *memoryRecorder) RecordAudio(ctx context.Context, value session.LiveAudioRecord) error {
	r.frames = append(r.frames, value)
	r.contexts = append(r.contexts, ctx)
	return r.err
}

func (r *memoryRecorder) RecordEvent(ctx context.Context, value session.LiveEvent) error {
	r.events = append(r.events, value)
	r.contexts = append(r.contexts, ctx)
	return r.err
}

func (*memoryRecorder) Finalize(context.Context, error) error { return nil }

func TestMessageFallbackPreservesPCMAndSingleTimestamp(t *testing.T) {
	r := &memoryRecorder{}
	now := time.Unix(42, 0)
	ticks := 0
	o := New(r, t.Context, func() time.Time { ticks++; return now }, 16000, 24000)
	o.Message(session.LiveRecord{Direction: session.LiveRecordAgent, Message: messages.StreamMessage{
		Type: messages.StreamTypeAudioDelta, ResponseID: "response", Value: &messages.AudioDeltaValue{Content: []byte{1, 0, 255, 255}},
	}})
	require.NoError(t, o.Error())
	require.Equal(t, 1, ticks)
	require.Len(t, r.frames, 1)
	require.Equal(t, []int16{1, -1}, r.frames[0].Frame.Samples)
	require.Equal(t, "response", r.frames[0].Frame.PlaybackResponse.ResponseID)
	require.Equal(t, session.LiveAudioMessageObserved, r.frames[0].Admission)
	require.Equal(t, now, r.frames[0].Timestamp)
	require.Equal(t, now, r.messages[0].Timestamp)
	o.Message(session.LiveRecord{Direction: session.LiveRecordAgent, Timestamp: now, Message: messages.StreamMessage{Type: messages.StreamTypeAudioEnd}})
	require.Len(t, r.frames, 2)
	require.True(t, r.frames[1].Frame.EndOfResponse)
	require.Empty(t, r.frames[1].Frame.Samples)
	o.SetMediaAttached(true)
	o.Message(r.messages[0])
	require.Len(t, r.messages, 3)
	require.Len(t, r.frames, 2, "media endpoint observations must not duplicate normalized messages")
}

func TestInvalidFallbackRetainsMessageWithoutInventingSamples(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rate  int
		value *messages.AudioDeltaValue
	}{
		{name: "unknown rate", value: &messages.AudioDeltaValue{Content: []byte{1, 0}}},
		{name: "missing value", rate: 24000},
		{name: "odd bytes", rate: 24000, value: &messages.AudioDeltaValue{Content: []byte{1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &memoryRecorder{}
			o := New(r, t.Context, nil, 16000, tc.rate)
			o.Message(session.LiveRecord{Direction: session.LiveRecordAgent, Message: messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: tc.value}})
			require.Error(t, o.Error())
			require.Len(t, r.messages, 1)
			require.Empty(t, r.frames)
		})
	}
}

func TestRecorderFailureDoesNotStopSubsequentObservations(t *testing.T) {
	failure := errors.New("recorder full")
	r := &memoryRecorder{err: failure}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	o := New(r, func() context.Context { return ctx }, nil, 16000, 24000)
	o.Event(session.LiveEvent{})
	r.err = errors.New("later failure")
	o.Frame(true, audio.PCMFrame{Samples: []int16{2}})
	o.Frame(false, audio.PCMFrame{Samples: []int16{3}})
	o.QueueFrame(audio.PCMFrame{Samples: []int16{4}})
	require.ErrorIs(t, o.Error(), failure)
	require.Len(t, r.events, 1)
	require.Len(t, r.frames, 3)
	require.Equal(t, audio.PCM16DeviceFormat(16000), r.frames[0].Frame.Format)
	require.Equal(t, session.LiveRecordClient, r.frames[0].Direction)
	require.Equal(t, session.LiveAudioMediaBridged, r.frames[0].Admission)
	require.Equal(t, audio.PCM16DeviceFormat(24000), r.frames[1].Frame.Format)
	require.Equal(t, session.LiveRecordAgent, r.frames[1].Direction)
	require.Equal(t, session.LiveAudioMediaBridged, r.frames[1].Admission)
	require.Equal(t, session.LiveAudioQueueAdmitted, r.frames[2].Admission)
	require.NoError(t, context.Cause(ctx))
	for _, observed := range r.contexts {
		require.Same(t, ctx, observed)
	}
}

func TestPartialFormatIsPreservedAsInvalidEvidence(t *testing.T) {
	r := &memoryRecorder{}
	o := New(r, t.Context, nil, 16000, 24000)
	format := audio.DeviceFormat{SampleRate: 8000}
	o.Frame(false, audio.PCMFrame{Format: format, Samples: []int16{1}})
	require.Error(t, o.Error())
	require.Len(t, r.frames, 1)
	require.Equal(t, format, r.frames[0].Frame.Format)
}

func TestRuntimeTracePublishesBoundariesAndFinalAccounting(t *testing.T) {
	var observed []sessiontrace.SessionRuntimeObservation
	now := time.Unix(42, 0)
	trace := NewRuntimeTrace(sessiontrace.RuntimeObserverFunc(func(value sessiontrace.RuntimeObservation) {
		observed = append(observed, value)
	}), func() time.Time { return now }, nil)

	trace.CapturedAudio(audio.PCMFrame{StreamID: "capture-1", Samples: []int16{1, -1}})
	trace.Message(messages.StreamMessage{Type: messages.StreamTypeInputItemAdded}, false)
	trace.CapturedAudio(audio.PCMFrame{StreamID: "capture-2", Samples: []int16{2}})
	trace.InputCommit(false)
	trace.ResponseCreate(messages.StreamMessage{
		ResponseID: "response-1", ActorStreamID: "provider-stream", LoopPassID: 3,
	})

	trace.Message(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant}, false)
	audioPayload := []byte{1, 0, 255, 255}
	trace.Message(messages.StreamMessage{
		Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant,
		ResponseID: "response-1", ActorStreamID: "assistant-stream", LoopPassID: 4,
		Value: messages.NewAudioDeltaValue(audioPayload),
	}, false)
	audioPayload[0] = 99
	trace.Message(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("ok")}, false)
	trace.Message(messages.StreamMessage{Type: messages.StreamTypeImageDelta, Role: messages.RoleAssistant, Value: messages.NewImageDeltaValue([]byte{7, 8, 9})}, false)
	trace.Message(messages.StreamMessage{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValue("heard")}, false)
	trace.Message(messages.StreamMessage{
		Type: messages.StreamTypeToolCallDelta, Role: messages.RoleAssistant, ToolCallId: "tool-1",
		Value: messages.NewToolCallDeltaValue(`{"n":`),
	}, false)
	trace.Message(messages.StreamMessage{
		Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ToolCallId: "tool-1",
		Value: messages.NewToolCallEndValue("tool-1", "lookup", `{"n":1}`),
	}, false)
	trace.Message(messages.StreamMessage{
		Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ToolCallId: "tool-2",
		Value: &messages.ToolCallEndValue{Arguments: "true"},
	}, false)
	trace.Message(messages.StreamMessage{Type: messages.StreamTypeToolCallDelta, Role: messages.RoleAssistant, Value: messages.NewToolCallDeltaValue("")}, false)
	trace.Message(messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: (*messages.ToolCallEndValue)(nil)}, false)
	trace.Message(messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "response-1",
		Value: messages.NewMessageEndValue(messages.TokenUsage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18, ReasoningTokens: 3}),
	}, false)
	trace.Message(messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd, Role: messages.RoleTool,
		Value: messages.NewMessageEndValue(messages.TokenUsage{PromptTokens: 500, CompletionTokens: 500, TotalTokens: 1000}),
	}, false)
	trace.Message(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant}, false)
	trace.Message(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("invalid usage")}, false)
	trace.Message(messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{PromptTokens: -1, CompletionTokens: 2, TotalTokens: 1}),
	}, false)
	trace.Message(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant}, true)
	trace.Terminal(0, nil)

	require.NoError(t, trace.Error())
	require.Len(t, observed, 9)
	require.Equal(t, []sessiontrace.SessionRuntimeObservationKind{
		sessiontrace.SessionRuntimeObservationAudioInput,
		sessiontrace.SessionRuntimeObservationInputCommit,
		sessiontrace.SessionRuntimeObservationAudioInput,
		sessiontrace.SessionRuntimeObservationInputCommit,
		sessiontrace.SessionRuntimeObservationResponseCreate,
		sessiontrace.SessionRuntimeObservationAudioOutput,
		sessiontrace.SessionRuntimeObservationTurnCompleted,
		sessiontrace.SessionRuntimeObservationTurnCompleted,
		sessiontrace.SessionRuntimeObservationTerminal,
	}, observationKinds(observed))
	for index, value := range observed {
		require.Equal(t, uint64(index+1), value.Tick)
		require.Equal(t, now, value.Timestamp)
	}
	require.Equal(t, []byte{1, 0, 255, 255}, observed[0].Payload)
	require.Equal(t, "capture-1", observed[0].StreamID)
	require.Equal(t, []byte{1, 0, 255, 255}, observed[1].Payload)
	require.Equal(t, 0, observed[1].InputCommit, "provider-created item must not increment caller commit count")
	require.Equal(t, []byte{2, 0}, observed[3].Payload)
	require.Equal(t, 1, observed[3].InputCommit)
	require.Equal(t, "response-1", observed[4].ResponseID)
	require.Equal(t, "provider-stream", observed[4].StreamID)
	require.Equal(t, 3, observed[4].LoopPassID)
	require.Equal(t, []byte{1, 0, 255, 255}, observed[5].Payload, "observer payload must not alias provider memory")
	require.Equal(t, "assistant-stream", observed[5].StreamID)
	require.Equal(t, 4, observed[5].LoopPassID)
	require.Equal(t, 1, observed[6].TurnsCompleted)
	require.Equal(t, 2, observed[7].TurnsCompleted)
	require.True(t, observed[8].Clean)
	require.Equal(t, 2, observed[8].TurnsCompleted)
	require.NotNil(t, observed[8].FinalAccounting)
	require.Equal(t, uint64(11), observed[8].FinalAccounting.PromptTokens)
	require.Equal(t, uint64(7), observed[8].FinalAccounting.CompletionTokens)
	require.Equal(t, uint64(18), observed[8].FinalAccounting.TotalTokens)
	require.Equal(t, uint64(3), observed[8].FinalAccounting.ReasoningTokens)
	require.Equal(t, sessiontrace.SessionTokenUsageIncremental, observed[8].FinalAccounting.UsageSemantics)
	metricsSnapshot := observed[8].FinalAccounting.Metrics
	require.Equal(t, uint64(6), metricsSnapshot.SeriesFor(metrics.DirectionInput, metrics.ModalityAudio).TotalBytes)
	require.Equal(t, uint64(5), metricsSnapshot.SeriesFor(metrics.DirectionInput, metrics.ModalityText).TotalBytes)
	require.Equal(t, uint64(4), metricsSnapshot.SeriesFor(metrics.DirectionOutput, metrics.ModalityAudio).TotalBytes)
	require.Equal(t, uint64(15), metricsSnapshot.SeriesFor(metrics.DirectionOutput, metrics.ModalityText).TotalBytes)
	require.Equal(t, uint64(3), metricsSnapshot.SeriesFor(metrics.DirectionOutput, metrics.ModalityImage).TotalBytes)
	require.Equal(t, uint64(9), metricsSnapshot.SeriesFor(metrics.DirectionOutput, metrics.ModalityTool).TotalBytes)
}

func TestRuntimeTraceBoundsCommitPayloadAndClassifiesTerminalErrors(t *testing.T) {
	var commits [][]byte
	var terminals []sessiontrace.RuntimeObservation
	var inputEvents int
	trace := NewRuntimeTrace(sessiontrace.RuntimeObserverFunc(func(value sessiontrace.RuntimeObservation) {
		switch value.Kind {
		case sessiontrace.SessionRuntimeObservationAudioInput:
			inputEvents++
		case sessiontrace.SessionRuntimeObservationInputCommit:
			commits = append(commits, value.Payload)
		case sessiontrace.SessionRuntimeObservationTerminal:
			terminals = append(terminals, value)
		default:
			// This test captures only commit retention and terminal classification.
		}
	}), nil, nil)
	trace.CapturedAudio(audio.PCMFrame{Samples: make([]int16, codec.MaxPayloadBytes/2+1)})
	trace.InputCommit(false)
	trace.CapturedAudio(audio.PCMFrame{Samples: []int16{3}})
	trace.InputCommit(false)
	require.Equal(t, 2, inputEvents)
	require.Len(t, commits, 2)
	require.Empty(t, commits[0], "oversized input must not be retained in the commit event")
	require.Equal(t, []byte{3, 0}, commits[1], "a later small commit must remain observable")

	trace.Terminal(4, context.Canceled)
	trace.Terminal(5, context.DeadlineExceeded)
	trace.Terminal(6, errors.New("provider stopped"))
	require.Len(t, terminals, 3)
	require.False(t, terminals[0].Clean)
	require.Empty(t, terminals[0].Error, "caller cancellation is a terminal but not a provider error")
	require.Equal(t, time.Time{}, terminals[0].Timestamp, "a nil clock must remain distinguishable from wall time")
	require.Empty(t, terminals[1].Error, "deadline is a terminal but not a provider error")
	require.Equal(t, "provider stopped", terminals[2].Error)
}

func TestRuntimeTraceWithoutObserverIsInert(t *testing.T) {
	var trace *RuntimeTrace = NewRuntimeTrace(nil, nil, nil)
	require.Nil(t, trace)
	require.NoError(t, trace.Error())
	trace.Message(messages.StreamMessage{}, false)
	trace.CapturedAudio(audio.PCMFrame{})
	trace.AudioOutput(messages.StreamMessage{})
	trace.InputCommit(false)
	trace.ResponseCreate(messages.StreamMessage{})
	trace.TurnCompleted(messages.StreamMessage{}, false)
	trace.Terminal(0, context.Canceled)
}

func observationKinds(values []sessiontrace.SessionRuntimeObservation) []sessiontrace.SessionRuntimeObservationKind {
	kinds := make([]sessiontrace.SessionRuntimeObservationKind, len(values))
	for index, value := range values {
		kinds[index] = value.Kind
	}
	return kinds
}
