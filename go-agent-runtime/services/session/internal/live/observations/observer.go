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
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessiontrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

// RuntimeTrace emits bounded lifecycle observations at the live service boundary.
type RuntimeTrace struct {
	observer      sessiontrace.RuntimeObserver
	clock         session.LiveClock
	tick          func() uint64
	sequence      atomic.Uint64
	inputMu       sync.Mutex
	input         []byte
	inputOverflow bool
	commits       int
	turns         atomic.Int64
	accountingMu  sync.Mutex
	accounting    *metrics.InMemorySink
	usage         messages.TokenUsage
	outputInTurn  bool
	toolDeltas    map[string]struct{}
}

// NewRuntimeTrace constructs an inert observer for one live invocation.
func NewRuntimeTrace(observer sessiontrace.RuntimeObserver, clock session.LiveClock, tick func() uint64) *RuntimeTrace {
	if observer == nil {
		return nil
	}
	accounting, err := metrics.NewInMemorySink()
	if err != nil {
		panic(err)
	}
	return &RuntimeTrace{observer: observer, clock: clock, tick: tick, accounting: accounting, toolDeltas: make(map[string]struct{})}
}

func (r *RuntimeTrace) observe(kind sessiontrace.SessionRuntimeObservationKind, payload []byte, turns, commit int, response messages.StreamMessage, clean bool, runErr error) {
	r.observeFinal(kind, payload, turns, commit, response, clean, runErr, nil)
}

func (r *RuntimeTrace) observeFinal(kind sessiontrace.SessionRuntimeObservationKind, payload []byte, turns, commit int, response messages.StreamMessage, clean bool, runErr error, finalAccounting *sessiontrace.SessionFinalAccounting) {
	if r == nil || r.observer == nil {
		return
	}
	tick := r.sequence.Add(1)
	if r.tick != nil {
		tick = r.tick()
	}
	r.observer.ObserveSessionRuntime(sessiontrace.SessionRuntimeObservation{
		Kind: kind, Tick: tick, Timestamp: r.now(), Payload: append([]byte(nil), payload...),
		TurnsCompleted: turns, InputCommit: commit, ResponseID: response.ResponseID,
		ResponsePurpose: response.ResponsePurpose, StreamID: response.ActorStreamID,
		LoopPassID: response.LoopPassID, Clean: clean, Error: runtimeTraceError(runErr), FinalAccounting: finalAccounting,
	})
}

// Message accounts each normalized stream observation before publishing its
// related trace events. Usage is accumulated only for completed responses
// that emitted output, matching the live stream boundary rather than a
// rendered transcript.
func (r *RuntimeTrace) Message(msg messages.StreamMessage, interrupted bool) {
	if r == nil {
		return
	}
	r.accountMessage(msg)
	r.AudioOutput(msg)
	r.TurnCompleted(msg, interrupted)
	if msg.Type == messages.StreamTypeInputItemAdded {
		r.InputCommit(true)
	}
}

func (r *RuntimeTrace) accountMessage(msg messages.StreamMessage) {
	if r == nil || r.accounting == nil {
		return
	}
	r.accountingMu.Lock()
	defer r.accountingMu.Unlock()
	if msg.Type == messages.StreamTypeMessageStart {
		r.outputInTurn = false
	}
	switch value := msg.Value.(type) {
	case *messages.AudioDeltaValue:
		direction := streamDirection(msg.Role)
		r.recordMetric(direction, metrics.ModalityAudio, len(value.Content))
		r.outputInTurn = r.outputInTurn || direction == metrics.DirectionOutput && len(value.Content) > 0
	case *messages.TextDeltaValue:
		direction := streamDirection(msg.Role)
		r.recordMetric(direction, metrics.ModalityText, len(value.Content))
		r.outputInTurn = r.outputInTurn || direction == metrics.DirectionOutput && len(value.Content) > 0
	case *messages.TranscriptDeltaValue:
		direction := streamDirection(msg.Role)
		r.recordMetric(direction, metrics.ModalityText, len(value.Text))
		r.outputInTurn = r.outputInTurn || direction == metrics.DirectionOutput && len(value.Text) > 0
	case *messages.ImageDeltaValue:
		direction := streamDirection(msg.Role)
		r.recordMetric(direction, metrics.ModalityImage, len(value.Content))
		r.outputInTurn = r.outputInTurn || direction == metrics.DirectionOutput && len(value.Content) > 0
	case *messages.ToolCallDeltaValue:
		r.recordMetric(metrics.DirectionOutput, metrics.ModalityTool, len(value.PartialJSON))
		if len(value.PartialJSON) > 0 {
			r.outputInTurn = true
			r.toolDeltas[msg.ToolCallId] = struct{}{}
		}
	case *messages.ToolCallEndValue:
		id := msg.ToolCallId
		if value.ToolCallID != "" {
			id = value.ToolCallID
		}
		if _, streamed := r.toolDeltas[id]; !streamed {
			r.recordMetric(metrics.DirectionOutput, metrics.ModalityTool, len(value.Arguments))
		}
		delete(r.toolDeltas, id)
	case *messages.MessageEndValue:
		usage := value.Usage
		if r.outputInTurn && usage.PromptTokens >= 0 && usage.CompletionTokens >= 0 && usage.TotalTokens >= 0 && usage.ReasoningTokens >= 0 {
			r.usage.PromptTokens += usage.PromptTokens
			r.usage.CompletionTokens += usage.CompletionTokens
			r.usage.TotalTokens += usage.TotalTokens
			r.usage.ReasoningTokens += usage.ReasoningTokens
		}
		r.outputInTurn = false
	}
}

func streamDirection(role messages.Role) metrics.Direction {
	if role == messages.RoleUser {
		return metrics.DirectionInput
	}
	return metrics.DirectionOutput
}

func (r *RuntimeTrace) recordMetric(direction metrics.Direction, modality metrics.Modality, byteCount int) {
	if byteCount > 0 {
		_ = r.accounting.Record(direction, modality, int64(byteCount))
	}
}

func (r *RuntimeTrace) finalAccounting() *sessiontrace.SessionFinalAccounting {
	if r == nil || r.accounting == nil {
		return nil
	}
	r.accountingMu.Lock()
	usage := r.usage
	r.accountingMu.Unlock()
	return &sessiontrace.SessionFinalAccounting{
		PromptTokens: uint64(usage.PromptTokens), CompletionTokens: uint64(usage.CompletionTokens),
		TotalTokens: uint64(usage.TotalTokens), ReasoningTokens: uint64(usage.ReasoningTokens),
		UsageSemantics: sessiontrace.SessionTokenUsageIncremental, Metrics: r.accounting.Snapshot(),
	}
}

func (r *RuntimeTrace) now() time.Time {
	if r.clock != nil {
		return r.clock()
	}
	return time.Time{}
}

// AudioOutput copies each normalized response-audio delta into the observer.
func (r *RuntimeTrace) AudioOutput(msg messages.StreamMessage) {
	value, ok := msg.Value.(*messages.AudioDeltaValue)
	if ok && value != nil {
		r.observe(sessiontrace.SessionRuntimeObservationAudioOutput, value.Content, 0, 0, msg, false, nil)
	}
}

// CapturedAudio records the accepted input frame and retains a bounded commit payload.
func (r *RuntimeTrace) CapturedAudio(frame audio.PCMFrame) {
	if r == nil {
		return
	}
	payload := codec.EncodePCM16(frame.Samples)
	r.accountingMu.Lock()
	r.recordMetric(metrics.DirectionInput, metrics.ModalityAudio, len(payload))
	r.accountingMu.Unlock()
	r.inputMu.Lock()
	if !r.inputOverflow && len(payload) <= codec.MaxPayloadBytes-len(r.input) {
		r.input = append(r.input, payload...)
	} else {
		r.inputOverflow = true
	}
	r.inputMu.Unlock()
	r.observe(sessiontrace.SessionRuntimeObservationAudioInput, payload, 0, 0, messages.StreamMessage{ActorStreamID: frame.StreamID}, false, nil)
}

// InputCommit emits one provider or caller-owned audio commit boundary.
func (r *RuntimeTrace) InputCommit(providerCreated bool) {
	if r == nil {
		return
	}
	r.inputMu.Lock()
	payload := append([]byte(nil), r.input...)
	r.input = nil
	if r.inputOverflow {
		payload = nil
		r.inputOverflow = false
	}
	commit := 0
	if !providerCreated {
		r.commits++
		commit = r.commits
	}
	r.inputMu.Unlock()
	r.observe(sessiontrace.SessionRuntimeObservationInputCommit, payload, 0, commit, messages.StreamMessage{}, true, nil)
}

// ResponseCreate records a response-request control accepted by the provider.
func (r *RuntimeTrace) ResponseCreate(msg messages.StreamMessage) {
	r.observe(sessiontrace.SessionRuntimeObservationResponseCreate, nil, 0, 0, msg, true, nil)
}

// TurnCompleted records assistant completion while excluding tool responses.
func (r *RuntimeTrace) TurnCompleted(msg messages.StreamMessage, interrupted bool) {
	if r == nil || msg.Type != messages.StreamTypeMessageEnd || msg.Role == messages.RoleTool || interrupted {
		return
	}
	turns := int(r.turns.Add(1))
	r.observe(sessiontrace.SessionRuntimeObservationTurnCompleted, nil, turns, 0, msg, true, nil)
}

// Terminal emits the bounded service result after its media workers join.
func (r *RuntimeTrace) Terminal(turns int, err error) {
	if r == nil {
		return
	}
	if turns == 0 {
		turns = int(r.turns.Load())
	}
	r.observeFinal(sessiontrace.SessionRuntimeObservationTerminal, nil, turns, 0, messages.StreamMessage{}, err == nil, err, r.finalAccounting())
}

func runtimeTraceError(err error) string {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ""
	}
	return err.Error()
}

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
	o.Audio(session.LiveAudioRecord{Direction: direction, Admission: session.LiveAudioMediaBridged, Frame: frame})
}

// QueueFrame records local capture after the bounded loop input queue accepts
// it. This is deliberately separate from Frame: queue admission is not a
// provider transport acknowledgement.
func (o *Observer) QueueFrame(frame audio.PCMFrame) {
	if o == nil {
		return
	}
	if frame.Format == (audio.DeviceFormat{}) && o.inputRate > 0 {
		frame.Format = audio.PCM16DeviceFormat(o.inputRate)
	}
	o.Audio(session.LiveAudioRecord{Direction: session.LiveRecordClient, Admission: session.LiveAudioQueueAdmitted, Frame: frame})
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
	o.Audio(session.LiveAudioRecord{Direction: session.LiveRecordAgent, Admission: session.LiveAudioMessageObserved, Timestamp: record.Timestamp, Frame: frame})
}
