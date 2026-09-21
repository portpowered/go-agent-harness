package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	gwproviders "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
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

func waitForSessionLoopStragglers(out io.Writer, loop *agentloop.AgentLoop, policy sessionStragglerDrainPolicy, obs *sessionProgressObserver) error {
	return waitForSessionLoopStragglersWithContext(context.Background(), out, loop, policy, obs, platformclock.Real{}, audioiowire.NewService())
}

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

func writeSessionReplayMessageUnscoped(out io.Writer, msg messages.StreamMessage) error {
	switch v := msg.Value.(type) {
	case *messages.TextDeltaValue:
		_, err := fmt.Fprint(out, v.Content)
		return err
	case *messages.TranscriptDeltaValue:
		if v == nil || v.Text == "" || strings.TrimSpace(v.Text) == "" {
			return nil
		}
		_, err := fmt.Fprintf(out, "%s: %s\n", sessionReplayTranscriptLabel(msg.Role), v.Text)
		return err
	case *messages.TranscriptEndValue:
		if v == nil || v.FullText == "" || strings.TrimSpace(v.FullText) == "" {
			return nil
		}
		_, err := fmt.Fprintf(out, "%s: %s\n", sessionReplayTranscriptLabel(msg.Role), v.FullText)
		return err
	case *messages.SessionCloseValue:
		return writeSessionReplayClose(out, v, true)
	case *messages.ErrorValue:
		return writeSessionReplayError(out, v)
	}
	return nil
}

func writeSessionReplayError(out io.Writer, value *messages.ErrorValue) error {
	if value == nil || value.IsNonTerminal() {
		return nil
	}
	fields := sessionErrorFields(value)
	wrapCause := func(message string) error {
		if value.Err == nil {
			return errors.New(message)
		}
		return fmt.Errorf("%s: %w", message, value.Err)
	}
	if value.Message != "" {
		if fields != "" {
			return wrapCause(fmt.Sprintf("session error: %s [%s]", value.Message, fields))
		}
		return wrapCause(fmt.Sprintf("session error: %s", value.Message))
	}
	if fields != "" {
		return wrapCause(fmt.Sprintf("session error [%s]", fields))
	}
	return wrapCause("session error")
}

func writeSessionReplayClose(out io.Writer, value *messages.SessionCloseValue, leadingNewline bool) error {
	if value == nil {
		return nil
	}
	if value.Reason != "" {
		prefix := ""
		if leadingNewline {
			prefix = "\n"
		}
		if _, err := fmt.Fprintf(out, "%s[session closed: %s]\n", prefix, value.Reason); err != nil {
			return err
		}
	}
	if fields := sessionTerminalFields(value.Classification, value.TerminalReason, value.TerminalProvenance, value.OutputState); fields != "" {
		_, err := fmt.Fprintf(out, "[session terminal: %s]\n", fields)
		return err
	}
	return nil
}

func isTerminalErrorMessage(msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeError {
		return false
	}
	value, ok := msg.Value.(*messages.ErrorValue)
	return !ok || value.IsTerminal()
}

func sessionErrorFields(value *messages.ErrorValue) string {
	if value == nil {
		return ""
	}
	classification := value.Classification
	if classification == "" && (value.ErrorType != "" || value.Code != "" || value.Message != "") {
		classification = gwproviders.SessionErrorClassification(value.ErrorType, value.Code, value.Message)
	}
	fields := sessionTerminalFields(classification, value.TerminalReason, value.TerminalProvenance, value.OutputState)
	providerFields := make([]string, 0, 2)
	if value.ErrorType != "" {
		providerFields = append(providerFields, "error_type="+value.ErrorType)
	}
	if value.Code != "" {
		providerFields = append(providerFields, "code="+value.Code)
	}
	if fields != "" {
		providerFields = append([]string{fields}, providerFields...)
	}
	return strings.Join(providerFields, " ")
}

// waitForSessionLoopStragglersWithContext drains provider output until the
// required quiet period elapses. The audio service creates the canonical
// timers so injected clocks retain the same behavior as the live loop.
func waitForSessionLoopStragglersWithContext(ctx context.Context, out io.Writer, loop *agentloop.AgentLoop, policy sessionStragglerDrainPolicy, obs *sessionProgressObserver, source platformclock.Source, services ...audioio.Service) error {
	if policy.quietPeriod <= 0 {
		return errInvalidSessionStragglerDrainPolicy
	}
	var audioService audioio.Service
	if len(services) > 0 {
		audioService = services[0]
	}
	if audioService == nil {
		return errors.New("session straggler drain requires an audio service")
	}
	idle, err := audioService.NewTimer(source, policy.quietPeriod)
	if err != nil {
		return err
	}
	var wallSafety platformclock.Timer
	var wallSafetyC <-chan time.Time
	if source != nil {
		wallSafety, err = audioService.NewTimer(platformclock.Real{}, sessionStragglerDrainWallSafety)
		if err != nil {
			idle.Stop()
			return err
		}
		wallSafetyC = wallSafety.C()
	}
	defer func() {
		idle.Stop()
		if wallSafety != nil {
			wallSafety.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-wallSafetyC:
			return nil
		case msg, ok := <-loop.Deltas().Chan():
			if !ok {
				return nil
			}
			if obs != nil {
				obs.observe(msg)
			}
			if err := writeSessionReplayMessage(out, msg); err != nil {
				return err
			}
			if !idle.Stop() {
				select {
				case <-idle.C():
				default:
				}
			}
			idle, err = audioService.NewTimer(source, policy.quietPeriod)
			if err != nil {
				return err
			}
		case <-idle.C():
			return nil
		}
	}
}

// sessionLivenessErrorChannel bridges the observer's stable wake channel to
// the session loop's error channel and exits promptly when its run is cancelled.
func sessionLivenessErrorChannel(ctx context.Context, observer *sessionProgressObserver) <-chan error {
	if observer == nil {
		return nil
	}
	_ = ctx // the merged error-channel owner already cancels its read on teardown.
	return observer.livenessErrors
}

func sessionTerminalFields(classification string, reason messages.TerminalReason, provenance messages.TerminalProvenance, outputState messages.TerminalOutputState) string {
	var fields []string
	if classification != "" {
		fields = append(fields, "classification="+classification)
	}
	if reason != "" {
		fields = append(fields, "terminal_reason="+string(reason))
	}
	if provenance != "" {
		fields = append(fields, "terminal_provenance="+string(provenance))
	}
	if outputState != "" {
		fields = append(fields, "output_state="+string(outputState))
	}
	return strings.Join(fields, " ")
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
		if err := writeSessionReplayMessage(out, msg); err != nil {
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
