// Package session owns the runtime recording façade. It is intentionally
// independent of CLI configuration, flags, and browser implementations.
package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimesession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// Service is the invocation factory. The existing semantic evidence service is
// injected explicitly so this package adds lifecycle composition without
// creating a second transcript/audio writer.
type Service struct {
	capture recording.Service
	clock   clock.Source
}

func New(capture recording.Service, source clock.Source) *Service {
	return &Service{capture: capture, clock: source}
}

func (s *Service) OpenSession(options recording.SessionOptions) (recording.SessionRecorder, error) {
	if s == nil || s.capture == nil {
		return nil, errors.New("recording session requires a capture service")
	}
	if s.clock == nil {
		return nil, errors.New("recording session requires a clock")
	}
	if options.Destination == "" {
		return nil, errors.New("recording session requires a destination")
	}
	base := s.clock.Now()
	if options.ClockBase.IsZero() {
		options.ClockBase = base
	}
	if options.WallClockStart.IsZero() {
		options.WallClockStart = base
	}
	options.Credentials = append([]string(nil), options.Credentials...)
	options.AdditionalArtifacts = cloneArtifacts(options.AdditionalArtifacts)
	live, err := s.capture.OpenLiveEvidence(recording.LiveEvidenceOptions{
		Destination:         options.Destination,
		SessionID:           options.SessionID,
		ParticipantID:       options.ParticipantID,
		Provider:            options.Provider,
		Model:               options.Model,
		ClockBase:           options.ClockBase,
		WallClockStart:      options.WallClockStart,
		Credentials:         options.Credentials,
		ProviderCapturePath: options.ProviderCapturePath,
		Limits:              options.Limits,
	})
	if err != nil {
		return nil, err
	}
	r := &recorder{
		live:       live,
		clock:      s.clock,
		options:    options,
		images:     make(map[string]imageEvidence),
		terminalCh: make(chan struct{}),
	}
	if options.Browser.Enabled {
		r.browser = newBrowserRecorder(options.Browser, options.Credentials, options.ClockBase)
		r.browser.start()
	}
	return r, nil
}

func cloneArtifacts(input []transcript.RecordingArtifact) []transcript.RecordingArtifact {
	if len(input) == 0 {
		return nil
	}
	output := make([]transcript.RecordingArtifact, len(input))
	for i, artifact := range input {
		output[i] = artifact
		output[i].Data = append([]byte(nil), artifact.Data...)
	}
	return output
}

type recorder struct {
	live    runtimesession.LiveRecorder
	clock   clock.Source
	options recording.SessionOptions
	browser *browserRecorder

	mu           sync.Mutex
	observeMu    sync.Mutex
	closed       bool
	images       map[string]imageEvidence
	imageOrder   []string
	inputAudio   [][]byte
	outputAudio  [][]byte
	audio        audioProjection
	toolOrder    []toolObservation
	terminal     *transcript.RecordingTerminalSummary
	terminalCh   chan struct{}
	firstErr     error
	finalizeOnce sync.Once
	finalizeErr  error
}

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
	r.observeMu.Lock()
	defer r.observeMu.Unlock()
	return r.observeMessageLocked(ctx, message, direction)
}

func (r *recorder) observeMessageLocked(ctx context.Context, message messages.StreamMessage, direction recording.SessionMessageDirection) error {
	if direction != recording.SessionMessageFromClient && direction != recording.SessionMessageFromAgent {
		return r.latch(errors.New("recording message direction is invalid"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return r.latch(recording.ErrLiveEvidenceClosed)
	}
	err := r.live.RecordMessage(ctx, runtimesession.LiveRecord{
		Direction: runtimesession.LiveRecordDirection(direction),
		Timestamp: r.observationTime(),
		Message:   cloneStreamMessage(message),
	})
	if err == nil {
		projectionDirection := recordingDirectionAgent
		if direction == recording.SessionMessageFromClient {
			projectionDirection = recordingDirectionClient
		}
		r.audio.observe(message, projectionDirection)
	}
	var audioContent []byte
	switch audio := message.Value.(type) {
	case *messages.AudioDeltaValue:
		if audio != nil {
			audioContent = audio.Content
		}
	}
	if len(audioContent) > 0 {
		r.mu.Lock()
		if direction == recording.SessionMessageFromClient {
			r.inputAudio = append(r.inputAudio, append([]byte(nil), audioContent...))
		} else {
			r.outputAudio = append(r.outputAudio, append([]byte(nil), audioContent...))
		}
		r.mu.Unlock()
	}
	return r.latch(err)
}

func (r *recorder) ObserveToolCall(ctx context.Context, call messages.ToolCall) error {
	if r == nil {
		return errors.New("nil recording session")
	}
	r.observeMu.Lock()
	defer r.observeMu.Unlock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return r.latch(recording.ErrLiveEvidenceClosed)
	}
	r.toolOrder = append(r.toolOrder, toolObservation{call: call})
	r.mu.Unlock()
	message := messages.StreamMessage{
		Type:       messages.StreamTypeToolCallEnd,
		Role:       messages.RoleAssistant,
		ToolCallId: call.ID,
		Value:      messages.NewToolCallEndValue(call.ID, call.Name, call.Arguments),
	}
	return r.observeMessageLocked(ctx, message, recording.SessionMessageFromAgent)
}

func (r *recorder) ObserveToolResult(ctx context.Context, call messages.ToolCall, response messages.ToolCallResponse, failed bool) error {
	if r == nil {
		return errors.New("nil recording session")
	}
	r.observeMu.Lock()
	defer r.observeMu.Unlock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return r.latch(recording.ErrLiveEvidenceClosed)
	}
	for index := range r.toolOrder {
		if r.toolOrder[index].call.ID == call.ID {
			r.toolOrder[index].result = response
			r.toolOrder[index].hasResult = true
			r.toolOrder[index].failed = failed
			break
		}
	}
	r.mu.Unlock()
	var imageErr error
	if !failed {
		if evidence, artifact, err := decodeImageCapture(call, response, r.nextImagePath(call, response)); err != nil {
			imageErr = wrapRecordingError(transcript.ErrRecordingWrite, "validate captured image", r.options.Destination, err, r.options.Credentials)
			_ = r.latch(imageErr)
		} else if evidence != nil {
			r.mu.Lock()
			if _, exists := r.images[call.ID]; !exists {
				r.images[call.ID] = *evidence
				r.imageOrder = append(r.imageOrder, call.ID)
				r.options.AdditionalArtifacts = append(r.options.AdditionalArtifacts, *artifact)
			}
			r.mu.Unlock()
		}
	}
	message := messages.StreamMessage{
		Type:       messages.StreamTypeTextDelta,
		Role:       messages.RoleTool,
		ToolCallId: call.ID,
		Value:      messages.NewTextDeltaValue(responseText(response)),
	}
	return errors.Join(imageErr, r.observeMessageLocked(ctx, message, recording.SessionMessageFromAgent))
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

func (r *recorder) nextImagePath(call messages.ToolCall, response messages.ToolCallResponse) string {
	// The final filename is filled after validating the digest. This placeholder
	// keeps the helper pure and avoids using the call name as a path component.
	r.mu.Lock()
	count := len(r.imageOrder) + 1
	r.mu.Unlock()
	return fmt.Sprintf("screenshots/%06d", count)
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
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return r.latch(recording.ErrLiveEvidenceClosed)
	}
	if r.terminal != nil {
		if *r.terminal == summary {
			r.mu.Unlock()
			return nil
		}
		err := fmt.Errorf("conflicting terminal summary")
		r.mu.Unlock()
		return r.latch(wrapRecordingError(transcript.ErrRecordingWrite, "capture terminal summary", r.options.Destination, err, r.options.Credentials))
	}
	copy := summary
	r.terminal = &copy
	r.mu.Unlock()
	terminal := messages.NewSessionCloseValueWithTerminal(
		r.options.SessionID,
		summary.Reason,
		summary.Classification,
		summary.TerminalReason,
		summary.TerminalProvenance,
		summary.OutputState,
	)
	err := r.live.RecordEvent(context.Background(), runtimesession.LiveEvent{
		Timestamp: r.observationTime(),
		Kind:      string(runtimesession.LiveEventTerminal),
		Terminal:  terminal,
		Critical:  true,
	})
	return r.latch(err)
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

func (r *recorder) Finalize(ctx context.Context, runErr error) error {
	if r == nil {
		return nil
	}
	r.finalizeOnce.Do(func() {
		if r.browser != nil {
			r.browser.stop()
		}
		r.observeMu.Lock()
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		r.appendAudioArtifactsLocked()
		options := cloneSessionOptions(r.options)
		liveErr := r.live.Finalize(ctx, runErr)
		r.observeMu.Unlock()
		augmentErr := augmentBundle(options, r.browser, r.imageSnapshot(), r.toolSnapshot(), r.audioSnapshot())
		r.finalizeErr = errors.Join(r.latched(), liveErr, augmentErr)
		close(r.terminalCh)
	})
	return r.finalizeErr
}

func cloneSessionOptions(options recording.SessionOptions) recording.SessionOptions {
	options.Credentials = append([]string(nil), options.Credentials...)
	options.AdditionalArtifacts = cloneArtifacts(options.AdditionalArtifacts)
	options.Metadata.Configuration = cloneStringMap(options.Metadata.Configuration)
	return options
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	copyValues := make(map[string]string, len(values))
	for key, value := range values {
		copyValues[key] = value
	}
	return copyValues
}

func (r *recorder) appendAudioArtifactsLocked() {
	for index, data := range r.inputAudio {
		r.options.AdditionalArtifacts = append(r.options.AdditionalArtifacts, transcript.RecordingArtifact{
			Path: fmt.Sprintf("audio/in-%03d.pcm", index), Data: append([]byte(nil), data...),
		})
	}
	for index, data := range r.outputAudio {
		r.options.AdditionalArtifacts = append(r.options.AdditionalArtifacts, transcript.RecordingArtifact{
			Path: fmt.Sprintf("audio/out-%03d.pcm", index), Data: append([]byte(nil), data...),
		})
	}
}

func (r *recorder) imageSnapshot() []imageEvidence {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]imageEvidence, 0, len(r.imageOrder))
	for _, id := range r.imageOrder {
		if image, ok := r.images[id]; ok {
			result = append(result, image)
		}
	}
	return result
}

type toolObservation struct {
	call      messages.ToolCall
	result    messages.ToolCallResponse
	hasResult bool
	failed    bool
}

func (r *recorder) toolSnapshot() []toolObservation {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]toolObservation, len(r.toolOrder))
	copy(result, r.toolOrder)
	return result
}

func (r *recorder) audioSnapshot() []audioTurn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.audio.snapshot()
}

func (r *recorder) latch(err error) error {
	if err == nil {
		return nil
	}
	r.mu.Lock()
	if r.firstErr == nil {
		r.firstErr = err
	}
	r.mu.Unlock()
	return err
}

func (r *recorder) latched() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.firstErr
}

type inferencer struct {
	inner     messages.SessionInferencer
	recording *recorder
}

func (i *inferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if i == nil || i.inner == nil {
		return nil, errors.New("recording inferencer is unavailable")
	}
	inner, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	if inner == nil {
		return nil, errors.New("recording inferencer returned nil session")
	}
	return newRecordedSession(ctx, inner, i.recording), nil
}

type recordedSession struct {
	inner     messages.Session
	recording *recorder
	ctx       context.Context
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func newRecordedSession(ctx context.Context, inner messages.Session, recording *recorder) *recordedSession {
	if ctx == nil {
		ctx = context.Background()
	}
	capacity := 1024
	if source := inner.Receive(); source != nil && source.Cap() > capacity {
		capacity = source.Cap()
	}
	s := &recordedSession{
		inner: inner, recording: recording, ctx: ctx,
		receive: messages.NewTypedBuffer[messages.StreamMessage](capacity), done: make(chan struct{}),
	}
	go s.relay()
	return s
}

func (s *recordedSession) Send(ctx context.Context, message messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, message).OK()
}

func (s *recordedSession) SendWithOutcome(ctx context.Context, message messages.StreamMessage) messages.SessionSendOutcome {
	outcome := messages.SendSessionWithOutcome(ctx, s.inner, message)
	if outcome.OK() {
		_ = s.recording.ObserveMessage(context.Background(), message, recording.SessionMessageFromClient)
	}
	return outcome
}

func (s *recordedSession) SendMessage(ctx context.Context, message messages.Message) bool {
	sender, ok := s.inner.(interface {
		SendMessage(context.Context, messages.Message) bool
	})
	return ok && sender.SendMessage(ctx, message)
}

func (s *recordedSession) SendMessageWithoutResponse(ctx context.Context, message messages.Message) bool {
	sender, ok := s.inner.(interface {
		SendMessageWithoutResponse(context.Context, messages.Message) bool
	})
	return ok && sender.SendMessageWithoutResponse(ctx, message)
}

func (s *recordedSession) SupportsCompleteMessages() bool {
	capability, ok := s.inner.(interface{ SupportsCompleteMessages() bool })
	return ok && capability.SupportsCompleteMessages()
}

func (s *recordedSession) SupportsCompleteMessagesWithoutResponse() bool {
	capability, ok := s.inner.(interface{ SupportsCompleteMessagesWithoutResponse() bool })
	return ok && capability.SupportsCompleteMessagesWithoutResponse()
}

func (s *recordedSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	if !messages.SupportsSessionResponseRequests(s.inner) {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	return s.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewResponseCreateValue(),
	})
}

func (s *recordedSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.inner)
}

func (s *recordedSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }

func (s *recordedSession) Done() <-chan struct{} { return s.inner.Done() }

func (s *recordedSession) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.inner.Close()
		select {
		case <-s.done:
			s.drain(s.inner.Receive())
		case <-time.After(time.Second):
			s.closeErr = errors.Join(s.closeErr, errors.New("recording session relay did not stop"))
		}
	})
	return s.closeErr
}

func (s *recordedSession) RTCMedia() sharedaudio.MediaEndpoints {
	if media, ok := s.inner.(sharedaudio.MediaSession); ok {
		return media.RTCMedia()
	}
	return sharedaudio.MediaEndpoints{}
}

func (s *recordedSession) TerminalError() error {
	if terminal, ok := s.inner.(interface{ TerminalError() error }); ok {
		return terminal.TerminalError()
	}
	return nil
}

func (s *recordedSession) relay() {
	defer close(s.done)
	source := s.inner.Receive()
	if source == nil {
		return
	}
	for {
		select {
		case message, ok := <-source.Chan():
			if !ok {
				return
			}
			_ = s.recording.ObserveMessage(context.Background(), message, recording.SessionMessageFromAgent)
			if !s.forward(message) {
				return
			}
		case <-s.inner.Done():
			s.drain(source)
			return
		case <-s.ctx.Done():
			s.drain(source)
			return
		}
	}
}

func (s *recordedSession) drain(source *messages.TypedBuffer[messages.StreamMessage]) {
	for {
		select {
		case message, ok := <-source.Chan():
			if !ok {
				return
			}
			_ = s.recording.ObserveMessage(context.Background(), message, recording.SessionMessageFromAgent)
			if !s.forward(message) {
				return
			}
		default:
			return
		}
	}
}

func (s *recordedSession) forward(message messages.StreamMessage) bool {
	for {
		if s.receive.Write(context.Background(), message) {
			return true
		}
		select {
		case <-s.ctx.Done():
			return false
		case <-s.inner.Done():
			return false
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func cloneStreamMessage(message messages.StreamMessage) messages.StreamMessage {
	clone := message
	switch value := message.Value.(type) {
	case *messages.AudioDeltaValue:
		if value != nil {
			copy := *value
			copy.Content = append([]byte(nil), value.Content...)
			clone.Value = &copy
		}
	case *messages.TextDeltaValue:
		if value != nil {
			copy := *value
			clone.Value = &copy
		}
	}
	return clone
}

func wrapRecordingError(kind error, operation, path string, cause error, secrets []string) error {
	_ = secrets
	return &transcript.RecordingError{Kind: kind, Operation: operation, Path: path, Cause: cause}
}

var _ recording.SessionService = (*Service)(nil)
var _ recording.SessionRecorder = (*recorder)(nil)
var _ messages.Session = (*recordedSession)(nil)
var _ messages.SessionSendOutcomeSender = (*recordedSession)(nil)
var _ messages.SessionResponseRequester = (*recordedSession)(nil)
var _ messages.SessionResponseCapability = (*recordedSession)(nil)
var _ sharedaudio.MediaSession = (*recordedSession)(nil)
