package observations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
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
