package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func runSessionAudioOutPlan(ctx context.Context, out io.Writer, plan sessionRuntimePlan, path string, seed SessionTextSeed, voice string, maxDuration time.Duration) (runErr error) {
	turnRuntime, err := prepareSessionTurnSeed(&plan, seed)
	if err != nil {
		return err
	}
	audioOut, err := newSessionAudioOutputForPlan(&plan, path, out, audio.NewLoudnessNormalizer(audio.LoudnessNormalizerConfig{GainDB: VoiceLoudnessGainDB(voice)}))
	if err != nil {
		return fmt.Errorf("--audio-out %q: %w", path, err)
	}
	defer func() {
		if closeErr := audioOut.close(); closeErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", path, closeErr))
		}
	}()
	sessionOut := audioSessionWriter(out, path)
	if plan.inferencer == nil {
		return runSessionAudioPlan(ctx, sessionOut, plan, maxDuration)
	}
	wirePrompt := ""
	if turnRuntime != nil {
		wirePrompt = turnRuntime.WirePrompt()
	}
	wrapped := newSessionAudioOutputInferencer(plan.inferencer, audioOut, wirePrompt, seed.Value)
	plan.inferencer = wrapped
	runErr = runSessionAudioPlan(ctx, sessionOut, plan, maxDuration)
	wrapped.wait()
	if outputErr := wrapped.err(); outputErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", path, outputErr))
	}
	return runErr
}

func audioSessionWriter(out io.Writer, path string) io.Writer {
	if path == "-" {
		return io.Discard
	}
	return out
}

func runSessionAudioPlan(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration) error {
	if maxDuration == 0 {
		return plan.run(ctx, out)
	}
	durationCtx, err := prepareSessionDurationArtifacts(ctx)
	if err != nil {
		return err
	}
	return runSessionDurationPlan(durationCtx, out, plan, maxDuration, realSessionDurationClock{})
}

func (o *sessionAudioOutput) close() error {
	o.closeOnce.Do(func() {
		o.mu.Lock()
		o.closed = true
		var sinkErr error
		if o.sink != nil {
			sinkErr = o.sink.Close()
		}
		o.mu.Unlock()
		o.mu.Lock()
		o.closeErr = sinkErr
		o.mu.Unlock()
	})
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.closeErr
}

type sessionAudioOutputInferencer struct {
	inner      messages.SessionInferencer
	output     *sessionAudioOutput
	wirePrompt string
	seedValue  string
	mu         sync.Mutex
	lastErr    error
	connected  *sessionAudioOutputSession
}

func newSessionAudioOutputInferencer(inner messages.SessionInferencer, output *sessionAudioOutput, wirePrompt string, seedValue string) *sessionAudioOutputInferencer {
	return &sessionAudioOutputInferencer{
		inner:      inner,
		output:     output,
		wirePrompt: wirePrompt,
		seedValue:  seedValue,
	}
}

func (i *sessionAudioOutputInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(context.WithoutCancel(ctx))
	if err != nil {
		return nil, err
	}
	wrapped := newSessionAudioOutputSession(ctx, session, i.output, i.recordErr, i.wirePrompt, i.seedValue)
	i.mu.Lock()
	i.connected = wrapped
	i.mu.Unlock()
	return wrapped, nil
}

func (i *sessionAudioOutputInferencer) wait() {
	i.mu.Lock()
	connected := i.connected
	i.mu.Unlock()
	if connected != nil {
		// Close completes retained draining and captures provider shutdown errors.
		i.recordErr(connected.Close())
	}
}

// recordErr joins stream and provider-close failures from the same session.
func (i *sessionAudioOutputInferencer) recordErr(err error) {
	if err == nil {
		return
	}
	i.mu.Lock()
	i.lastErr = errors.Join(i.lastErr, err)
	i.mu.Unlock()
}

func (i *sessionAudioOutputInferencer) err() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.lastErr
}

type sessionAudioOutputSession struct {
	messages.Session
	ctx            context.Context
	output         *sessionAudioOutput
	record         func(error)
	wirePrompt     string
	seedValue      string
	receive        *messages.TypedBuffer[messages.StreamMessage]
	done           chan struct{}
	drainStarted   chan struct{}
	closeRequested chan struct{}
	closeOnce      sync.Once
	innerCloseOnce sync.Once
	innerCloseDone chan struct{}
	closeErr       error
	seedMu         sync.Mutex
	seedSent       bool
}

func newSessionAudioOutputSession(ctx context.Context, inner messages.Session, output *sessionAudioOutput, record func(error), wirePrompt string, seedValue string) *sessionAudioOutputSession {
	s := &sessionAudioOutputSession{
		Session:        inner,
		ctx:            ctx,
		output:         output,
		record:         record,
		wirePrompt:     wirePrompt,
		seedValue:      seedValue,
		receive:        messages.NewTypedBuffer[messages.StreamMessage](sessionAudioOutputBufferSize),
		done:           make(chan struct{}),
		drainStarted:   make(chan struct{}),
		closeRequested: make(chan struct{}),
		innerCloseDone: make(chan struct{}),
	}
	go s.forward()
	return s
}

func (s *sessionAudioOutputSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if s.replaceSeed(msg) {
		msg.Value = messages.NewTextDeltaValue(s.seedValue)
	}
	return s.Session.Send(ctx, msg)
}

func (s *sessionAudioOutputSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, s.Session)
}

func (s *sessionAudioOutputSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.Session)
}

func (s *sessionAudioOutputSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(sessionturn.CompleteMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

func (s *sessionAudioOutputSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(sessionturn.CompleteMessageWithoutResponseSender)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *sessionAudioOutputSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.Session)
	return complete
}

func (s *sessionAudioOutputSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.Session)
	return withoutResponse
}

func (s *sessionAudioOutputSession) replaceSeed(msg messages.StreamMessage) bool {
	if s.wirePrompt == "" || msg.Type != messages.StreamTypeTextDelta {
		return false
	}
	value, ok := msg.Value.(*messages.TextDeltaValue)
	if !ok || value.Content != s.wirePrompt {
		return false
	}

	s.seedMu.Lock()
	defer s.seedMu.Unlock()
	if s.seedSent {
		return false
	}
	s.seedSent = true
	return true
}

func (s *sessionAudioOutputSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *sessionAudioOutputSession) Done() <-chan struct{} { return s.done }

func (s *sessionAudioOutputSession) rtcMedia() (RTCMediaEndpoints, bool) {
	return rtcMediaFromSession(s.Session)
}

func (s *sessionAudioOutputSession) TerminalError() error {
	return terminalSessionError(s.Session)
}

func (s *sessionAudioOutputSession) forward() {
	defer func() {
		s.closeInner()
		close(s.done)
	}()
	input := s.Session.Receive()
	for {
		if s.ctx.Err() != nil {
			s.drainAfterCancellation(input)
			return
		}
		if s.forwardNext(input) {
			continue
		}
		if s.ctx.Err() != nil {
			s.drainAfterCancellation(input)
		} else {
			select {
			case <-s.closeRequested:
				s.drainAfterCancellation(input)
			default:
			}
		}
		return
	}
}

func (s *sessionAudioOutputSession) forwardNext(input *messages.TypedBuffer[messages.StreamMessage]) bool {
	select {
	case msg := <-input.Chan():
		retainingCtx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), sessionStragglerDrainWallSafety)
		defer cancel()
		return s.forwardMessageWithContext(retainingCtx, msg, s.ctx.Err() != nil)
	case <-s.Session.Done():
		if s.ctx.Err() == nil {
			s.drain(input, s.ctx, false)
		}
	case <-s.closeRequested:
	case <-s.ctx.Done():
	}
	return false
}

func (s *sessionAudioOutputSession) drain(input *messages.TypedBuffer[messages.StreamMessage], ctx context.Context, retaining bool) bool {
	for {
		msg, ok := input.Read()
		if !ok {
			return true
		}
		if !s.forwardMessageWithContext(ctx, msg, retaining) {
			return false
		}
	}
}

func (s *sessionAudioOutputSession) drainAfterCancellation(input *messages.TypedBuffer[messages.StreamMessage]) {
	close(s.drainStarted)
	retainCtx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), sessionStragglerDrainWallSafety)
	defer cancel()
	terminal := time.NewTimer(sessionStragglerDrainWallSafety)
	defer terminal.Stop()
	for {
		select {
		case msg := <-input.Chan():
			if !s.forwardMessageWithContext(retainCtx, msg, true) {
				return
			}
		case <-s.Session.Done():
			s.closeInner()
			s.drainAfterInnerClose(input, retainCtx)
			return
		case <-terminal.C:
			s.closeInner()
			s.drainAfterInnerClose(input, retainCtx)
			return
		}
	}
}

func (s *sessionAudioOutputSession) drainAfterInnerClose(input *messages.TypedBuffer[messages.StreamMessage], ctx context.Context) {
	select {
	case <-s.innerCloseDone:
	case <-ctx.Done():
		return
	}
	for {
		select {
		case msg := <-input.Chan():
			if !s.forwardMessageWithContext(ctx, msg, true) {
				return
			}
		case <-time.After(sessionStragglerDrainQuietPeriod):
			return
		case <-ctx.Done():
			return
		}
	}
}

func (s *sessionAudioOutputSession) closeInner() {
	s.innerCloseOnce.Do(func() { go s.finishInnerClose() })
}

func (s *sessionAudioOutputSession) finishInnerClose() {
	s.closeErr = s.Session.Close()
	close(s.innerCloseDone)
}

func (s *sessionAudioOutputSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.closeRequested)
		<-s.done
		<-s.innerCloseDone
	})
	return s.closeErr
}

func (s *sessionAudioOutputSession) forwardMessageWithContext(ctx context.Context, msg messages.StreamMessage, retaining bool) bool {
	if msg.Type == messages.StreamTypeAudioDelta && assistantAudioDelta(msg) {
		value, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok {
			s.record(fmt.Errorf("AUDIO.DELTA has unexpected value %T", msg.Value))
			s.closeInner()
			return false
		}
		if err := s.output.writeDelta(ctx, value.Content, msg); err != nil {
			s.record(err)
			s.closeInner()
			return false
		}
	}

	for {
		outcome := s.receive.WriteContext(ctx, msg)
		if outcome.OK() {
			return true
		}
		if outcome.Err != nil {
			return false
		}
		if retaining {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-s.closeRequested:
			return false
		case <-time.After(time.Millisecond):
		}
	}
}

func assistantAudioDelta(msg messages.StreamMessage) bool {
	return msg.Role == "" || msg.Role == messages.RoleAssistant
}
