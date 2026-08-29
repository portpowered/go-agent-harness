package services

import (
	"context"
	"errors"
	"io"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// SessionTextSeed carries the value and Cobra presence of --prompt separately.
// A separate presence bit keeps an explicitly supplied empty prompt distinct
// from an omitted flag.
type SessionTextSeed struct {
	Value   string
	Present bool
}

// RunSessionWithTextSeed runs a session using the explicit text seed when it
// is present, otherwise preserving the existing positional Prompt behavior.
func RunSessionWithTextSeed(ctx context.Context, out io.Writer, opts SessionRunOptions, seed SessionTextSeed) (runErr error) {
	var coordinator *SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if !seed.Present {
		return RunSession(ctx, out, opts)
	}

	opts.Prompt = seed.Value
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	plan, err := planSessionRuntime(opts)
	if err != nil {
		return err
	}

	// The existing session loop uses a non-empty Prompt as its trigger. Carry a
	// unique non-empty wire value through that unchanged loop, then translate it
	// back at the owned session boundary so an explicitly empty value is still
	// delivered exactly as supplied.
	wirePrompt := nextSessionTextWirePrompt()
	plan.loop.Prompt = wirePrompt
	output := &sessionTextOutput{writer: out}
	if plan.inferencer != nil {
		plan.inferencer = &sessionTextSeedInferencer{
			inner:      plan.inferencer,
			wirePrompt: wirePrompt,
			value:      seed.Value,
		}
	}
	return errors.Join(plan.run(ctx, output), output.errorValue())
}

var sessionTextWireSequence uint64

const sessionTextWirePrefix = "\x00agent-cli-session-text-seed:"

func nextSessionTextWirePrompt() string {
	sequence := atomic.AddUint64(&sessionTextWireSequence, 1)
	return sessionTextWirePrefix + strconv.FormatUint(sequence, 10)
}

type sessionTextSeedInferencer struct {
	inner      messages.SessionInferencer
	wirePrompt string
	value      string
}

var _ messages.SessionInferencer = (*sessionTextSeedInferencer)(nil)

func (i *sessionTextSeedInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	wrapped := &sessionTextSeedSession{
		inner:      session,
		wirePrompt: i.wirePrompt,
		value:      i.value,
		receive:    messages.NewTypedBuffer[messages.StreamMessage](256),
	}
	go wrapped.forwardIncoming()
	return wrapped, nil
}

type sessionTextSeedSession struct {
	inner      messages.Session
	wirePrompt string
	value      string
	receive    *messages.TypedBuffer[messages.StreamMessage]

	mu       sync.Mutex
	seedSent bool
}

var _ messages.Session = (*sessionTextSeedSession)(nil)

func (s *sessionTextSeedSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if s.replaceSeed(msg) {
		msg.Value = messages.NewTextDeltaValue(s.value)
	}
	return s.inner.Send(ctx, msg)
}

// RequestResponse forwards the optional explicit response capability while
// leaving the text-seed replacement limited to the initial user turn.
func (s *sessionTextSeedSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, s.inner)
}

func (s *sessionTextSeedSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.inner)
}

func (s *sessionTextSeedSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(SessionImageMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

func (s *sessionTextSeedSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(SessionImageMessageSenderWithoutResponse)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *sessionTextSeedSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.inner)
	return complete
}

func (s *sessionTextSeedSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.inner)
	return withoutResponse
}

func (s *sessionTextSeedSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *sessionTextSeedSession) Done() <-chan struct{} {
	return s.inner.Done()
}

func (s *sessionTextSeedSession) rtcMedia() (RTCMediaEndpoints, bool) {
	return rtcMediaFromSession(s.inner)
}

func (s *sessionTextSeedSession) TerminalError() error {
	return terminalSessionError(s.inner)
}

func (s *sessionTextSeedSession) Close() error {
	return s.inner.Close()
}

func (s *sessionTextSeedSession) forwardIncoming() {
	for {
		msg, ok := s.inner.Receive().ReadBlocking(s.inner.Done())
		if !ok {
			return
		}
		if !s.receive.Write(context.Background(), msg) {
			_ = s.inner.Close()
			return
		}
	}
}

func (s *sessionTextSeedSession) replaceSeed(msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeTextDelta {
		return false
	}
	value, ok := msg.Value.(*messages.TextDeltaValue)
	if !ok || value.Content != s.wirePrompt {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seedSent {
		return false
	}
	s.seedSent = true
	return true
}

type sessionTextOutput struct {
	writer io.Writer

	mu       sync.Mutex
	writeErr error
}

func (o *sessionTextOutput) Write(data []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.writeErr != nil {
		return 0, o.writeErr
	}

	n, err := o.writer.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		o.writeErr = err
	}
	return n, err
}

func (o *sessionTextOutput) errorValue() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.writeErr
}
