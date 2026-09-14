package agentruntime

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimesession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

func (o *sessionProgressObserver) recordLiveMessage(msg messages.StreamMessage, direction runtimesession.LiveRecordDirection) {
	o.recordLiveMessageContext(context.Background(), msg, direction)
}

func (o *sessionProgressObserver) recordLiveMessageContext(ctx context.Context, msg messages.StreamMessage, direction runtimesession.LiveRecordDirection) {
	if o == nil || o.liveRecorder == nil {
		return
	}
	o.recordLiveMessageWire(ctx, msg, direction)
	if direction == runtimesession.LiveRecordAgent {
		o.recordLiveMessageAudioContext(ctx, msg)
	}
	o.recordLiveMessageTerminal(ctx, msg)
}

func (o *sessionProgressObserver) recordLiveMessageWire(ctx context.Context, msg messages.StreamMessage, direction runtimesession.LiveRecordDirection) {
	o.liveRecordingMu.Lock()
	defer o.liveRecordingMu.Unlock()
	err := o.liveRecorder.RecordMessage(ctx, runtimesession.LiveRecord{Direction: direction, Timestamp: o.liveTimestamp(), Message: msg})
	if err != nil && o.liveRecordingErr == nil {
		o.liveRecordingErr = err
	}
}

func (o *sessionProgressObserver) recordLiveMessageTerminal(ctx context.Context, msg messages.StreamMessage) {
	//nolint:exhaustive // only terminal stream types are recorded here.
	switch msg.Type {
	case messages.StreamTypeSessionClose:
		value, ok := msg.Value.(*messages.SessionCloseValue)
		if ok && value != nil {
			o.recordLiveTerminalContext(ctx, value, nil)
		}
	case messages.StreamTypeError:
		value, ok := msg.Value.(*messages.ErrorValue)
		if ok && value != nil && value.IsTerminal() {
			o.recordLiveTerminalContext(ctx, terminalValueFromError(value), value.Err)
		}
	}
}

func terminalValueFromError(value *messages.ErrorValue) *messages.SessionCloseValue {
	terminalReason := value.TerminalReason
	if terminalReason == "" {
		terminalReason = messages.TerminalReasonTerminalFailure
	}
	classification := value.Classification
	if classification == "" {
		classification = string(terminalReason)
	}
	provenance := value.TerminalProvenance
	if provenance == "" {
		provenance = messages.TerminalProvenanceProvider
	}
	outputState := value.OutputState
	if outputState == "" {
		outputState = messages.TerminalOutputNone
	}
	return messages.NewSessionCloseValueWithTerminal("", value.Message, classification, terminalReason, provenance, messages.TerminalOutputState(outputState))
}

func (o *sessionProgressObserver) recordLiveAudio(record runtimesession.LiveAudioRecord) {
	o.recordLiveAudioContext(context.Background(), record)
}

func (o *sessionProgressObserver) recordLiveAudioContext(ctx context.Context, record runtimesession.LiveAudioRecord) {
	if o == nil || o.liveRecorder == nil {
		return
	}
	if record.Timestamp.IsZero() {
		record.Timestamp = o.liveTimestamp()
	}
	o.liveRecordingMu.Lock()
	err := o.liveRecorder.RecordAudio(ctx, record)
	if err != nil && o.liveRecordingErr == nil {
		o.liveRecordingErr = err
	}
	o.liveRecordingMu.Unlock()
}

func (o *sessionProgressObserver) recordLiveMessageAudioContext(ctx context.Context, msg messages.StreamMessage) {
	if o == nil || o.liveRecorder == nil || o.outputAudioRate <= 0 || !isLiveAudioMessage(msg) {
		return
	}
	frame, ok := liveAudioFrame(msg, o.outputAudioRate)
	if !ok {
		return
	}
	o.recordLiveAudioContext(ctx, runtimesession.LiveAudioRecord{Direction: runtimesession.LiveRecordAgent, Admission: runtimesession.LiveAudioMessageObserved, Timestamp: o.liveTimestamp(), Frame: frame})
}

func isLiveAudioMessage(msg messages.StreamMessage) bool {
	return msg.Type == messages.StreamTypeAudioDelta || msg.Type == messages.StreamTypeAudioEnd
}

func liveAudioFrame(msg messages.StreamMessage, rate int) (audio.PCMFrame, bool) {
	frame := audio.PCMFrame{Format: audio.PCM16DeviceFormat(rate), PlaybackResponse: audio.PlaybackResponse{ResponseID: msg.ResponseID}}
	if msg.Type == messages.StreamTypeAudioEnd {
		frame.EndOfResponse = true
		return frame, true
	}
	value, ok := msg.Value.(*messages.AudioDeltaValue)
	if !ok || value == nil || len(value.Content) == 0 {
		return audio.PCMFrame{}, false
	}
	samples, err := codec.DecodePCM16(value.Content)
	if err != nil {
		return audio.PCMFrame{}, false
	}
	frame.Samples = samples
	return frame, true
}

func (o *sessionProgressObserver) recordLiveTerminal(value *messages.SessionCloseValue, runErr error) {
	o.recordLiveTerminalContext(context.Background(), value, runErr)
}

func (o *sessionProgressObserver) recordLiveTerminalContext(ctx context.Context, value *messages.SessionCloseValue, runErr error) {
	if o == nil || o.liveRecorder == nil || value == nil {
		return
	}
	o.liveRecordingMu.Lock()
	defer o.liveRecordingMu.Unlock()
	if o.liveTerminalSeen {
		return
	}
	o.liveTerminalSeen = true
	err := o.liveRecorder.RecordEvent(ctx, runtimesession.LiveEvent{
		Timestamp: o.liveTimestamp(), Kind: string(runtimesession.LiveEventTerminal), SessionID: o.sessionID,
		Terminal: value, Error: runErr, Critical: true,
	})
	if err != nil && o.liveRecordingErr == nil {
		o.liveRecordingErr = err
	}
}

func (o *sessionProgressObserver) recordLiveTerminalForRun(runErr error) {
	if o == nil || o.liveRecorder == nil || o.liveTerminalRecorded() {
		return
	}
	o.liveRecordingMu.Lock()
	durationExpired := o.liveDurationExpired
	o.liveRecordingMu.Unlock()
	if durationExpired {
		o.recordLiveTerminalContext(context.Background(), messages.NewSessionCloseValueWithTerminal("", "max_duration", "max_duration", messages.TerminalReason("max_duration"), messages.TerminalProvenanceLoop, messages.TerminalOutputPartial), runErr)
		return
	}
	reason, provenance := sessionTerminalClassification(o, runErr)
	outputState := deriveOutputState(o.sawSessionOpen, o.turnsCompleted)
	o.recordLiveTerminal(messages.NewSessionCloseValueWithTerminal("", "", string(reason), reason, provenance, messages.TerminalOutputState(outputState)), runErr)
}

func (o *sessionProgressObserver) markLiveDurationExceeded() {
	if o == nil {
		return
	}
	o.liveRecordingMu.Lock()
	o.liveDurationExpired = true
	o.liveRecordingMu.Unlock()
}

func markLiveDurationFinish(planned *bool, value bool, observer *sessionProgressObserver) {
	*planned = value
	if value {
		observer.markLiveDurationExceeded()
	}
}

func (o *sessionProgressObserver) liveTerminalRecorded() bool {
	o.liveRecordingMu.Lock()
	defer o.liveRecordingMu.Unlock()
	return o.liveTerminalSeen
}

func sessionTerminalClassification(o *sessionProgressObserver, runErr error) (messages.TerminalReason, messages.TerminalProvenance) {
	if o.userCancelled || roomCancellationOnly(runErr) {
		return messages.TerminalReasonCancellation, messages.TerminalProvenanceCLI
	}
	if runErr != nil && !roomCancellationOnly(runErr) {
		return messages.TerminalReasonTerminalFailure, messages.TerminalProvenanceSession
	}
	return messages.TerminalReasonLoopSynthesizedCompletion, messages.TerminalProvenanceLoop
}

func (o *sessionProgressObserver) liveRecordingFailure() error {
	if o == nil {
		return nil
	}
	o.liveRecordingMu.Lock()
	defer o.liveRecordingMu.Unlock()
	return o.liveRecordingErr
}

func (o *sessionProgressObserver) lockProviderBoundary() func() {
	if o == nil {
		return func() {}
	}
	o.providerBoundaryMu.Lock()
	return o.providerBoundaryMu.Unlock
}

func (o *sessionProgressObserver) scheduleAudioInputs(inputs []ScheduledAudioInput) {
	if o == nil {
		return
	}
	o.lifecycleProjectionMu.Lock()
	defer o.lifecycleProjectionMu.Unlock()
	o.pendingInputs = append(o.pendingInputs, inputs...)
	o.scheduledInputs += len(inputs)
}
