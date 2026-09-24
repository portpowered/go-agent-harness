package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const (
	seedValue      = "Say hello in one short sentence."
	reusedSentinel = "\x00agent-cli-session-text-seed:reused"
)

const errShortWrite sessionturn.Error = "writer closed"

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errShortWrite }

// seededPlan records how RunSeeded bound the host plan.
type seededPlan struct {
	prompt     string
	inferencer messages.SessionInferencer
	runs       int
	wrap       func(messages.SessionInferencer) messages.SessionInferencer
	runErr     error
	write      bool
}

func (p *seededPlan) request(seed sessionturn.Seed, bounded bool, out io.Writer) sessionturn.SeededRunRequest {
	return sessionturn.SeededRunRequest{
		Seed: seed, Bounded: bounded, Output: out, Inferencer: p.inferencer,
		SetPrompt:     func(prompt string) { p.prompt = prompt },
		SetInferencer: func(inferencer messages.SessionInferencer) { p.inferencer = inferencer },
		Run: func(out io.Writer, wrap func(messages.SessionInferencer) messages.SessionInferencer) error {
			p.runs++
			p.wrap = wrap
			if p.write {
				//nolint:errcheck // the sticky output error is asserted through RunSeeded.
				out.Write([]byte("rendered"))
			}
			return p.runErr
		},
	}
}

func TestRunSeededRequiresRun(t *testing.T) {
	if err := New(nil, nil, nil).RunSeeded(sessionturn.SeededRunRequest{}); !errors.Is(err, errNoSeededRun) {
		t.Fatalf("RunSeeded without run = %v", err)
	}
}

func TestRunSeededWithoutSeedRunsPlanUnchanged(t *testing.T) {
	plan := &seededPlan{inferencer: nopInferencer{}}
	if err := New(nil, nil, nil).RunSeeded(plan.request(sessionturn.Seed{}, true, io.Discard)); err != nil {
		t.Fatalf("RunSeeded = %v", err)
	}
	if plan.runs != 1 || plan.wrap != nil || plan.prompt != "" || plan.inferencer != (nopInferencer{}) {
		t.Fatalf("unseeded plan = %#v, want one unchanged run", plan)
	}
}

func TestRunSeededBoundedWrapsInsideAdmissionAndJoinsOutputFailure(t *testing.T) {
	plan := &seededPlan{inferencer: nopInferencer{}, write: true}
	err := New(nil, nil, nil).RunSeeded(plan.request(sessionturn.Seed{Value: seedValue, Present: true}, true, failingWriter{}))
	if !errors.Is(err, errShortWrite) {
		t.Fatalf("RunSeeded = %v, want joined output failure", err)
	}
	if !strings.HasPrefix(plan.prompt, sessionturn.TextSeedWirePrefix) || plan.wrap == nil || plan.inferencer != (nopInferencer{}) {
		t.Fatalf("bounded plan = %#v, want sentinel prompt and deferred wrap", plan)
	}
	inner := &capturingInferencer{}
	assertSeedSubstitution(t, plan.wrap(inner), inner, plan.prompt)
}

func TestRunSeededUnboundedWrapsPlanInferencerAndReusesSentinel(t *testing.T) {
	inner := &capturingInferencer{}
	plan := &seededPlan{inferencer: inner}
	request := plan.request(sessionturn.Seed{Value: seedValue, Present: true}, false, io.Discard)
	request.WirePrompt = reusedSentinel
	plan.runErr = errors.New("loop failed")
	if err := New(nil, nil, nil).RunSeeded(request); !errors.Is(err, plan.runErr) {
		t.Fatalf("RunSeeded = %v, want run failure", err)
	}
	if plan.prompt != reusedSentinel || plan.wrap != nil || plan.inferencer == messages.SessionInferencer(inner) {
		t.Fatalf("unbounded plan = %#v, want reused sentinel and wrapped plan inferencer", plan)
	}
	assertSeedSubstitution(t, plan.inferencer, inner, reusedSentinel)
}

type capturingInferencer struct{ session *capturingSession }

func (i *capturingInferencer) ConnectSession(context.Context) (messages.Session, error) {
	i.session = &capturingSession{receive: messages.NewTypedBuffer[messages.StreamMessage](1), done: make(chan struct{})}
	return i.session, nil
}

type capturingSession struct {
	sent    []messages.StreamMessage
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
}

func (s *capturingSession) Send(_ context.Context, msg messages.StreamMessage) bool {
	s.sent = append(s.sent, msg)
	return true
}
func (s *capturingSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }
func (s *capturingSession) Done() <-chan struct{}                                  { return s.done }
func (s *capturingSession) Close() error                                           { return nil }

func assertSeedSubstitution(t *testing.T, wrapped messages.SessionInferencer, inner *capturingInferencer, sentinel string) {
	t.Helper()
	session, err := wrapped.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("connect seeded session: %v", err)
	}
	defer close(inner.session.done)
	if !session.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(sentinel)}) {
		t.Fatal("seeded send rejected")
	}
	sent := inner.session.sent
	if len(sent) != 1 {
		t.Fatalf("provider saw %#v, want one message", sent)
	}
	if value, ok := sent[0].Value.(*messages.TextDeltaValue); !ok || value.Content != seedValue {
		t.Fatalf("provider saw %#v, want the explicit seed", sent[0])
	}
}
