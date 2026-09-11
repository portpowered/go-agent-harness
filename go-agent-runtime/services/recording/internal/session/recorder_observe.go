package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimesession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func (r *recorder) Wrap(inner messages.SessionInferencer) messages.SessionInferencer {
	if r == nil || inner == nil {
		return inner
	}
	return &inferencer{inner: inner, recording: r}
}

func (r *recorder) ObserveMessage(ctx context.Context, message messages.StreamMessage, direction recording.SessionMessageDirection) error {
	if r == nil {
		return errors.New("nil recording session")
	}
	if ctx == nil {
		return r.latch(errors.New("recording message context is nil"))
	}
	r.observeMu.Lock()
	defer r.observeMu.Unlock()
	return r.observeMessageLocked(ctx, message, direction)
}

func (r *recorder) observeMessageLocked(ctx context.Context, message messages.StreamMessage, direction recording.SessionMessageDirection) error {
	if !validMessageDirection(direction) {
		return r.latch(errors.New("recording message direction is invalid"))
	}
	if r.isClosed() {
		return r.latch(recording.ErrLiveEvidenceClosed)
	}
	err := r.live.RecordMessage(ctx, runtimesession.LiveRecord{
		Direction: runtimesession.LiveRecordDirection(direction),
		Timestamp: r.observationTime(),
		Message:   cloneStreamMessage(message),
	})
	if err == nil {
		r.observeAudio(message, direction)
	}
	r.captureAudio(message, direction)
	return r.latch(err)
}

func validMessageDirection(direction recording.SessionMessageDirection) bool {
	return direction == recording.SessionMessageFromClient || direction == recording.SessionMessageFromAgent
}

func (r *recorder) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

func (r *recorder) observeAudio(message messages.StreamMessage, direction recording.SessionMessageDirection) {
	projectionDirection := recordingDirectionAgent
	if direction == recording.SessionMessageFromClient {
		projectionDirection = recordingDirectionClient
	}
	r.audio.observe(message, projectionDirection)
}

func (r *recorder) captureAudio(message messages.StreamMessage, direction recording.SessionMessageDirection) {
	audio, ok := message.Value.(*messages.AudioDeltaValue)
	if !ok || audio == nil || len(audio.Content) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	content := append([]byte(nil), audio.Content...)
	if direction == recording.SessionMessageFromClient {
		r.inputAudio = append(r.inputAudio, content)
		return
	}
	r.outputAudio = append(r.outputAudio, content)
}

func (r *recorder) ObserveToolCall(ctx context.Context, call messages.ToolCall) error {
	if r == nil {
		return errors.New("nil recording session")
	}
	if ctx == nil {
		return r.latch(errors.New("recording tool-call context is nil"))
	}
	r.observeMu.Lock()
	defer r.observeMu.Unlock()
	if r.isClosed() {
		return r.latch(recording.ErrLiveEvidenceClosed)
	}
	r.mu.Lock()
	r.toolOrder = append(r.toolOrder, toolObservation{call: call})
	r.mu.Unlock()
	return r.observeMessageLocked(ctx, toolCallMessage(call), recording.SessionMessageFromAgent)
}

func toolCallMessage(call messages.ToolCall) messages.StreamMessage {
	return messages.StreamMessage{
		Type:       messages.StreamTypeToolCallEnd,
		Role:       messages.RoleAssistant,
		ToolCallId: call.ID,
		Value:      messages.NewToolCallEndValue(call.ID, call.Name, call.Arguments),
	}
}

func (r *recorder) ObserveToolResult(ctx context.Context, call messages.ToolCall, response messages.ToolCallResponse, failed bool) error {
	if r == nil {
		return errors.New("nil recording session")
	}
	if ctx == nil {
		return r.latch(errors.New("recording tool-result context is nil"))
	}
	r.observeMu.Lock()
	defer r.observeMu.Unlock()
	if r.isClosed() {
		return r.latch(recording.ErrLiveEvidenceClosed)
	}
	r.updateToolObservation(call, response, failed)
	var imageErr error
	if !failed {
		imageErr = r.captureImage(call, response)
	}
	return errors.Join(imageErr, r.observeMessageLocked(ctx, toolResultMessage(call, response), recording.SessionMessageFromAgent))
}

func (r *recorder) updateToolObservation(call messages.ToolCall, response messages.ToolCallResponse, failed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.toolOrder {
		if r.toolOrder[index].call.ID != call.ID {
			continue
		}
		r.toolOrder[index].result = response
		r.toolOrder[index].hasResult = true
		r.toolOrder[index].failed = failed
		return
	}
}

func (r *recorder) captureImage(call messages.ToolCall, response messages.ToolCallResponse) error {
	evidence, artifact, err := decodeImageCapture(call, response, r.nextImagePath())
	if err != nil {
		imageErr := wrapRecordingError(transcript.ErrRecordingWrite, "validate captured image", r.options.Destination, err, r.options.Credentials)
		r.recordError(imageErr)
		return imageErr
	}
	if evidence == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.images[call.ID]; exists {
		return nil
	}
	r.images[call.ID] = *evidence
	r.imageOrder = append(r.imageOrder, call.ID)
	r.options.AdditionalArtifacts = append(r.options.AdditionalArtifacts, *artifact)
	return nil
}

func (r *recorder) nextImagePath() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return fmt.Sprintf("screenshots/%06d", len(r.imageOrder)+1)
}

func toolResultMessage(call messages.ToolCall, response messages.ToolCallResponse) messages.StreamMessage {
	return messages.StreamMessage{
		Type:       messages.StreamTypeTextDelta,
		Role:       messages.RoleTool,
		ToolCallId: call.ID,
		Value:      messages.NewTextDeltaValue(responseText(response)),
	}
}

func responseText(response messages.ToolCallResponse) string {
	if response.Content != "" {
		return response.Content
	}
	if len(response.ContentParts) == 0 {
		return ""
	}
	return fmt.Sprintf("%d content parts", len(response.ContentParts))
}

func (r *recorder) RecordTerminalSummary(summary transcript.RecordingTerminalSummary) error {
	if r == nil {
		return errors.New("nil recording session")
	}
	if err := summary.Validate(); err != nil {
		return r.latch(err)
	}
	r.observeMu.Lock()
	defer r.observeMu.Unlock()
	if r.isClosed() {
		return r.latch(recording.ErrLiveEvidenceClosed)
	}
	if err := r.setTerminal(summary); err != nil {
		return r.latch(err)
	}
	err := r.live.RecordEvent(context.Background(), runtimesession.LiveEvent{
		Timestamp: r.observationTime(),
		Kind:      string(runtimesession.LiveEventTerminal),
		Terminal:  terminalValue(r.options.SessionID, summary),
		Critical:  true,
	})
	return r.latch(err)
}

func (r *recorder) setTerminal(summary transcript.RecordingTerminalSummary) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.terminal != nil {
		if *r.terminal == summary {
			return nil
		}
		return wrapRecordingError(transcript.ErrRecordingWrite, "capture terminal summary", r.options.Destination, errors.New("conflicting terminal summary"), r.options.Credentials)
	}
	copy := summary
	r.terminal = &copy
	return nil
}

func terminalValue(sessionID string, summary transcript.RecordingTerminalSummary) *messages.SessionCloseValue {
	return messages.NewSessionCloseValueWithTerminal(
		sessionID, summary.Reason, summary.Classification, summary.TerminalReason,
		summary.TerminalProvenance, summary.OutputState,
	)
}

// observationTime keeps deterministic hosts byte-comparable while preserving
// the historical one-tick-per-observation ordering. Real clocks do not expose
// Advance and therefore continue to use their wall-clock reading directly.
func (r *recorder) observationTime() time.Time {
	if advancing, ok := r.clock.(interface{ Advance() uint64 }); ok {
		advancing.Advance()
	}
	return r.clock.Now()
}
