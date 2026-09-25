package sessionwrap

import (
	"context"
	"io"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

// SeedReceiveCapacity is the relay buffer of a text-seed session.
const SeedReceiveCapacity = 256

// WirePrompts allocates unique loop prompts that carry an explicit text seed
// through a loop that triggers on a non-empty prompt.
type WirePrompts struct {
	sequence atomic.Uint64
}

// Next returns a new sentinel prompt.
func (w *WirePrompts) Next() string {
	return sessionturn.TextSeedWirePrefix + strconv.FormatUint(w.sequence.Add(1), 10)
}

// SeedInferencer translates the sentinel back to the explicit seed at the
// provider boundary, so an explicitly empty seed is delivered as supplied.
type SeedInferencer struct {
	inner      messages.SessionInferencer
	wirePrompt string
	value      string
}

var _ messages.SessionInferencer = (*SeedInferencer)(nil)

// NewSeedInferencer wraps inner with one seed substitution.
func NewSeedInferencer(inner messages.SessionInferencer, wirePrompt, value string) *SeedInferencer {
	return &SeedInferencer{inner: inner, wirePrompt: wirePrompt, value: value}
}

// ConnectSession connects the provider and starts the receive relay.
func (i *SeedInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	wrapped := NewSeedSession(session, i.wirePrompt, i.value)
	// The relay outlives the connect call; it ends with the provider session.
	go wrapped.ForwardIncoming(context.WithoutCancel(ctx))
	return wrapped, nil
}

// SeedSession substitutes the first matching sentinel text delta.
type SeedSession struct {
	forwarder
	wirePrompt string
	value      string
	receive    *messages.TypedBuffer[messages.StreamMessage]

	mu       sync.Mutex
	seedSent bool
}

var _ messages.Session = (*SeedSession)(nil)

// NewSeedSession wraps a connected session. Callers start ForwardIncoming.
func NewSeedSession(inner messages.Session, wirePrompt, value string) *SeedSession {
	return &SeedSession{
		forwarder:  forwarder{inner: inner},
		wirePrompt: wirePrompt,
		value:      value,
		receive:    messages.NewTypedBuffer[messages.StreamMessage](SeedReceiveCapacity),
	}
}

func (s *SeedSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if s.replaceSeed(msg) {
		msg.Value = messages.NewTextDeltaValue(s.value)
	}
	return s.inner.Send(ctx, msg)
}

func (s *SeedSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }

func (s *SeedSession) Done() <-chan struct{} { return s.inner.Done() }

func (s *SeedSession) Close() error { return s.inner.Close() }

// ForwardIncoming relays provider messages until the provider is done. A
// relay write failure closes the provider session.
func (s *SeedSession) ForwardIncoming(ctx context.Context) {
	for {
		msg, ok := s.inner.Receive().ReadBlocking(s.inner.Done())
		if !ok {
			return
		}
		if !s.receive.Write(ctx, msg) {
			_ = s.inner.Close() //nolint:errcheck // the relay has no caller to report a close failure to
			return
		}
	}
}

func (s *SeedSession) replaceSeed(msg messages.StreamMessage) bool {
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

// Output retains the first write failure, including a short write.
type Output struct {
	writer   io.Writer
	mu       sync.Mutex
	writeErr error
}

var _ sessionturn.Output = (*Output)(nil)

// NewOutput wraps writer.
func NewOutput(writer io.Writer) *Output { return &Output{writer: writer} }

func (o *Output) Write(data []byte) (int, error) {
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

// Err returns the first write failure.
func (o *Output) Err() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.writeErr
}
