package services

import (
	"context"
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
func RunSessionWithTextSeed(ctx context.Context, out io.Writer, opts SessionRunOptions, seed SessionTextSeed) error {
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
	if plan.inferencer != nil {
		plan.inferencer = &sessionTextSeedInferencer{
			inner:      plan.inferencer,
			wirePrompt: wirePrompt,
			value:      seed.Value,
		}
	}
	return plan.run(ctx, out)
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
	return &sessionTextSeedSession{
		Session:    session,
		wirePrompt: i.wirePrompt,
		value:      i.value,
	}, nil
}

type sessionTextSeedSession struct {
	messages.Session
	wirePrompt string
	value      string

	mu       sync.Mutex
	seedSent bool
}

var _ messages.Session = (*sessionTextSeedSession)(nil)

func (s *sessionTextSeedSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if s.replaceSeed(msg) {
		msg.Value = messages.NewTextDeltaValue(s.value)
	}
	return s.Session.Send(ctx, msg)
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
