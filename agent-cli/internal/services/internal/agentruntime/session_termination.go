package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	terminalwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
)

// sessionStragglerDrainQuietPeriod is the bounded quiet period used before a
// session is stopped. Provider output and terminal signals travel through
// independent paths, so the terminal signal can overtake output the provider
// has already accepted.
const sessionStragglerDrainQuietPeriod = 25 * time.Millisecond

// sessionStragglerDrainWallSafety bounds teardown when the canonical clock
// is deterministic and has stopped advancing. It is deliberately longer than
// the normal quiet period so a provider delta that arrives shortly after the
// terminal signal still gets drained before the safety path wins.
const sessionStragglerDrainWallSafety = 250 * time.Millisecond

// sessionStragglerDrainPolicy is required by the waiting drain operation so a
// caller cannot obtain a buffered-only terminal path by omitting a mode. The
// default policy is deliberately bounded.
type sessionStragglerDrainPolicy struct {
	quietPeriod time.Duration
}

// Invariant: a terminal signal must never skip the straggler drain.

// defaultSessionStragglerDrainPolicy is the only policy selected by the shared
// termination boundary. A positive quiet period is mandatory because the
// provider output path can lag the terminal signal by one scheduling turn.
var defaultSessionStragglerDrainPolicy = sessionStragglerDrainPolicy{
	quietPeriod: sessionStragglerDrainQuietPeriod,
}

var errInvalidSessionStragglerDrainPolicy = errors.New("session straggler drain policy requires a positive quiet period")

var errMissingSessionStragglerDrain = errors.New("session termination boundary requires a straggler drain")

// sessionTerminationBoundary is the one terminal shutdown boundary shared by
// the live and duration session loops. Its callbacks are loop-owned adapters:
// they retain the live renderer or duration artifact/terminal state while this
// boundary owns the ordering of terminal cleanup.
//
// Every terminal signal follows this order, even when the initiating path has
// an error: quiesce external producers, wait for stragglers during the bounded
// quiet period, cancel and stop owned resources, then flush messages already
// buffered after the stop. Quiescing is separate from stopping the session so
// room-owned mixer producers cannot create new outbound transport events while
// the session's provider output is being drained.
//
// ctx is the caller-owned run context. The owning select loop can observe
// ctx.Done() and a different, unrelated terminal channel (provider close, a
// scheduled timer, a tool lifecycle transition, ...) as ready at the same
// time; which case the runtime picks is not deterministic. Every terminal
// path funnels through terminate, so it — not each of the loop's dozen call
// sites — is the one place responsible for preserving a caller cancellation
// that a differently-caused clean result would otherwise race out of the
// returned error. See the trunk-flake and select-race postmortems this
// boundary exists to prevent from recurring piecemeal.
type sessionTerminationBoundary struct {
	ctx                context.Context
	quiesceUpstream    func() error
	waitForStragglers  func(sessionStragglerDrainPolicy) error
	stopOwnedResources func() error
	flushBuffered      func() error
	once               sync.Once
	result             error
}

// terminate applies the shared terminal-drain contract and joins cleanup
// failures with the initiating error without masking either one. It also
// unconditionally joins the boundary's run-context error: when ctx was never
// cancelled that is a no-op (errors.Join drops nil arguments), and when ctx
// was cancelled or its deadline expired, the caller's cancellation survives
// in the returned error regardless of which terminal channel the owning
// select loop happened to observe first.
func (b *sessionTerminationBoundary) terminate(primary error) error {
	b.once.Do(func() {
		var quiesceErr, waitErr, stopErr, flushErr error
		if b.quiesceUpstream != nil {
			quiesceErr = b.quiesceUpstream()
		}
		if b.waitForStragglers == nil {
			// A missing wait callback is a configuration error, never an implicit
			// buffered-only exception. Keep the remaining cleanup phases running
			// so a malformed boundary still releases owned resources.
			waitErr = errMissingSessionStragglerDrain
		} else {
			waitErr = b.waitForStragglers(defaultSessionStragglerDrainPolicy)
		}
		if b.stopOwnedResources != nil {
			stopErr = b.stopOwnedResources()
		}
		if b.flushBuffered != nil {
			flushErr = b.flushBuffered()
		}
		var ctxErr error
		if b.ctx != nil {
			ctxErr = b.ctx.Err()
		}
		b.result = errors.Join(primary, quiesceErr, waitErr, stopErr, flushErr, ctxErr)
	})
	return b.result
}

func isTerminalErrorMessage(msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeError {
		return false
	}
	value, ok := msg.Value.(*messages.ErrorValue)
	return !ok || value.IsTerminal()
}

func flushBufferedSessionLoopMessages(out io.Writer, loop *agentloop.AgentLoop, obs *sessionProgressObserver) error {
	for {
		msg, ok := loop.Deltas().Read()
		if !ok {
			return nil
		}
		if obs != nil {
			obs.observe(msg)
		}
		if err := terminalwire.NewService().WriteTranscriptMessage(out, msg); err != nil {
			return err
		}
	}
}

func sendSessionClose(ctx context.Context, loop *agentloop.AgentLoop) error {
	msg := messages.Message{
		Role: messages.RoleUser,
		ContentParts: []messages.ContentPart{
			messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypeSessionClose},
		},
	}
	if err := loop.Send(ctx, []messages.Message{msg}); err != nil {
		return fmt.Errorf("close session loop: %w", err)
	}
	return nil
}

func wrapSessionPhaseError(phase string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", phase, err)
}

func shouldStopSessionLoop(msg messages.StreamMessage, opts sessionLoopOptions) bool {
	if isAuthoritativeSessionStop(msg) || hasSessionTerminalFailure(msg, opts.observer) {
		return true
	}
	if opts.CloseAfterOpen || opts.WaitForClose {
		return false
	}
	return ordinarySessionStop(msg, opts)
}

func isAuthoritativeSessionStop(msg messages.StreamMessage) bool {
	return msg.Type == messages.StreamTypeSessionClose || msg.Type == messages.StreamTypeLoopEnd || isTerminalErrorMessage(msg)
}

func hasSessionTerminalFailure(msg messages.StreamMessage, observer *sessionProgressObserver) bool {
	return msg.Type == messages.StreamTypeMessageEnd && observer != nil &&
		(observer.hasTerminalToolContinuationFailure() || observer.hasTerminalScheduledResponseFailure())
}

func ordinarySessionStop(msg messages.StreamMessage, opts sessionLoopOptions) bool {
	//nolint:exhaustive // only terminal boundaries determine ordinary session stop.
	switch msg.Type {
	case messages.StreamTypeMessageEnd:
		return messageEndStopsSession(opts)
	case messages.StreamTypeTextEnd:
		return opts.observer == nil || !opts.observer.hasToolLifecycleObligation()
	default:
		return false
	}
}

func messageEndStopsSession(opts sessionLoopOptions) bool {
	if opts.observer != nil && (!opts.observer.lastMessageEndAdmitted() || opts.observer.hasToolLifecycleObligation()) {
		return false
	}
	if opts.CloseAfterScheduledAudio && opts.observer != nil && !opts.observer.scheduledAudioComplete() {
		return false
	}
	return true
}
