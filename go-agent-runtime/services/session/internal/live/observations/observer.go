// Package observations adapts invocation observations to the public recorder
// port. It does not own archive formats, storage, provider I/O or device I/O.
package observations

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

// Observer is invocation-owned. Recorder admission must remain nonblocking;
// failures are retained for the lifecycle join without interrupting audio.
type Observer struct {
	recorder              session.LiveRecorder
	context               func() context.Context
	clock                 session.LiveClock
	inputRate, outputRate int
	mediaAttached         atomic.Bool
	mu                    sync.Mutex
	err                   error
}

// New receives the invocation's cleanup context and canonical clock. The
// context supplier preserves Start's context values even if it differs from
// the earlier OpenLive admission context. Construction performs no effects.
func New(recorder session.LiveRecorder, source func() context.Context, clock session.LiveClock, inputRate, outputRate int) *Observer {
	return &Observer{recorder: recorder, context: source, clock: clock, inputRate: inputRate, outputRate: outputRate}
}

func (o *Observer) SetMediaAttached(attached bool) {
	if o != nil {
		o.mediaAttached.Store(attached)
	}
}

func (o *Observer) Error() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.err
}

func (o *Observer) latch(operation string, err error) {
	if err == nil {
		return
	}
	o.mu.Lock()
	if o.err == nil {
		o.err = fmt.Errorf("%s: %w", operation, err)
	}
	o.mu.Unlock()
}

func (o *Observer) timestamp(value time.Time) time.Time {
	if value.IsZero() && o.clock != nil {
		return o.clock()
	}
	return value
}

func (o *Observer) Message(record session.LiveRecord) {
	if o == nil || o.recorder == nil {
		return
	}
	record.Timestamp = o.timestamp(record.Timestamp)
	o.latch("record live message", o.recorder.RecordMessage(o.context(), record))
	if !o.mediaAttached.Load() {
		o.messageAudio(record)
	}
}

// Frame records the media bridge boundary. An omitted format may use an
// explicitly negotiated rate; a partially specified invalid format is kept
// intact so recording cannot conceal the source's invalid metadata.
func (o *Observer) Frame(outbound bool, frame audio.PCMFrame) {
	if o == nil {
		return
	}
	direction, rate := session.LiveRecordAgent, o.outputRate
	if outbound {
		direction, rate = session.LiveRecordClient, o.inputRate
	}
	if frame.Format == (audio.DeviceFormat{}) && rate > 0 {
		frame.Format = audio.PCM16DeviceFormat(rate)
	}
	o.Audio(session.LiveAudioRecord{Direction: direction, Frame: frame})
}

func (o *Observer) Audio(record session.LiveAudioRecord) {
	if o == nil || o.recorder == nil {
		return
	}
	record.Timestamp = o.timestamp(record.Timestamp)
	o.latch("observe live audio format", record.Frame.Format.Validate())
	o.latch("record live audio", o.recorder.RecordAudio(o.context(), record))
}

func (o *Observer) Event(event session.LiveEvent) {
	if o == nil || o.recorder == nil {
		return
	}
	event.Timestamp = o.timestamp(event.Timestamp)
	o.latch("record live event", o.recorder.RecordEvent(o.context(), event))
}

// Some inferencers expose normalized PCM messages without a media endpoint.
// Adapt only that boundary; providers with media endpoints are observed there
// so a response is never recorded twice. PCM decoding stays in go-audio.
func (o *Observer) messageAudio(record session.LiveRecord) {
	if record.Direction != session.LiveRecordAgent {
		return
	}
	if record.Message.Type != messages.StreamTypeAudioDelta && record.Message.Type != messages.StreamTypeAudioEnd {
		return
	}
	if o.outputRate <= 0 {
		o.latch("record fallback audio", errors.New("provider audio sample rate is unavailable"))
		return
	}
	frame := audio.PCMFrame{Format: audio.PCM16DeviceFormat(o.outputRate), PlaybackResponse: audio.PlaybackResponse{ResponseID: record.Message.ResponseID}}
	if record.Message.Type == messages.StreamTypeAudioDelta {
		value, ok := record.Message.Value.(*messages.AudioDeltaValue)
		if !ok || value == nil {
			o.latch("record fallback audio", errors.New("audio delta has no PCM value"))
			return
		}
		if len(value.Content) == 0 {
			return
		}
		decoded, err := codec.DecodePCM16(value.Content)
		if err != nil {
			o.latch("decode provider audio evidence", err)
			return
		}
		frame.Samples = decoded
	} else {
		frame.EndOfResponse = true
	}
	o.Audio(session.LiveAudioRecord{Direction: session.LiveRecordAgent, Timestamp: record.Timestamp, Frame: frame})
}
