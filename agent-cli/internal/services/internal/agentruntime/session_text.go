package agentruntime

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/textseed"
	textseedwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/textseed/wire"
)

// SessionTextSeed is a compatibility alias for textseed.Seed; the adapter symbols below are deprecated.
type SessionTextSeed = textseed.Seed

// RunSessionWithTextSeed runs a session using the explicit text seed when it
// is present, otherwise preserving the existing positional Prompt behavior.
func RunSessionWithTextSeed(ctx context.Context, out io.Writer, opts SessionRunOptions, seed SessionTextSeed) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()

	if !seed.Present {
		return RunSession(ctx, out, opts)
	}

	opts.Prompt = seed.Value
	opts.PromptProvided = true
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	plan, err := planSessionRuntime(opts)
	if err != nil {
		return err
	}

	service := textseedwire.NewDefaultService()
	wirePrompt := service.Allocate()
	plan.loop.Prompt = wirePrompt
	output := newSessionTextOutput(out, service)
	if plan.inferencer != nil {
		plan.inferencer = &sessionTextSeedInferencer{
			inner:      plan.inferencer,
			wirePrompt: wirePrompt,
			value:      seed.Value,
			service:    service,
		}
	}
	return errors.Join(plan.run(ctx, output), output.errorValue())
}

// Deprecated: use textseed.Service.WrapInferencer.
type sessionTextSeedInferencer struct {
	inner      messages.SessionInferencer
	wirePrompt string
	value      string
	service    textseed.Service
}

func (i *sessionTextSeedInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	service := i.service
	if service == nil {
		service = textseedwire.NewDefaultService()
	}
	wrapped := service.WrapSession(ctx, session, i.wirePrompt, textseed.Seed{Value: i.value, Present: true})
	return &sessionTextSeedSession{Session: wrapped, inner: session}, nil
}

// sessionTextSeedSession is the CLI-only media/terminal compatibility adapter.
// Text-seed behavior itself is supplied by the public runtime Session.
type sessionTextSeedSession struct {
	textseed.Session
	inner messages.Session
}

func (s *sessionTextSeedSession) rtcMedia() (RTCMediaEndpoints, bool) {
	return rtcMediaFromSession(s.inner)
}

func (s *sessionTextSeedSession) TerminalError() error {
	return terminalSessionError(s.inner)
}

// Deprecated: use textseed.Service.Allocate.
func nextSessionTextWirePrompt() string { return textseedwire.NewDefaultService().Allocate() }

// Deprecated: use textseed.Service.NewOutput.
type sessionTextOutput struct {
	writer  io.Writer
	service textseed.Service

	once   sync.Once
	output textseed.Output
}

func newSessionTextOutput(writer io.Writer, service textseed.Service) *sessionTextOutput {
	return &sessionTextOutput{writer: writer, service: service}
}

func (o *sessionTextOutput) ensure() textseed.Output {
	o.once.Do(func() {
		if o.service == nil {
			o.service = textseedwire.NewDefaultService()
		}
		o.output = o.service.NewOutput(o.writer)
	})
	return o.output
}

func (o *sessionTextOutput) Write(data []byte) (int, error) {
	return o.ensure().Write(data)
}

func (o *sessionTextOutput) errorValue() error {
	return o.ensure().Err()
}
