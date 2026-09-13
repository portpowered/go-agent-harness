package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func (s *service) acceptAndHandle(ctx context.Context, req RunRequest, handler MessageHandler, state *terminalState, msg messages.StreamMessage) (MessageResult, error) {
	if req.Artifacts != nil {
		if err := req.Artifacts.Accept(msg); err != nil {
			return MessageResult{}, fmt.Errorf("accept duration artifact: %w", err)
		}
	}
	return handler(ctx, msg, state.snapshot())
}

func (s *service) finish(ctx context.Context, req RunRequest, admission *admission, handle Handle, state *terminalState, planned bool, preferred error) error {
	errs := s.finishDrain(ctx, req, admission, handle, state, planned)
	stopCtx, cancel := context.WithTimeout(ctx, cleanupTimeout(req.CleanupTimeout))
	defer cancel()
	errs = append(errs, named("stop duration runner", handle.Stop(stopCtx)))
	errs = append(errs, s.flushBuffered(ctx, req, admission, handle, state, planned))
	admission.Wait()
	errs = append(errs, s.finishTerminal(ctx, req, admission, state, planned)...)
	if req.OnFinish != nil {
		req.OnFinish(planned, state.outputState())
	}
	errs = append(errs, s.finishErrors(req, admission, preferred)...)
	return errors.Join(append(errs, s.finishArtifacts(req))...)
}

func (s *service) finishDrain(ctx context.Context, req RunRequest, admission *admission, handle Handle, state *terminalState, planned bool) []error {
	var errs []error
	if req.Quiesce != nil {
		errs = append(errs, named("quiesce upstream", req.Quiesce()))
	}
	if req.MaxDuration > 0 {
		errs = append(errs, s.drain(ctx, req, admission, handle, state, planned))
	}
	return errs
}

func (s *service) finishTerminal(ctx context.Context, req RunRequest, admission *admission, state *terminalState, planned bool) []error {
	if state.terminalWritten {
		return nil
	}
	if planned {
		return s.finishPlannedTerminal(ctx, req, admission, state)
	}
	if ctx != nil && ctx.Err() == nil {
		return s.finishProviderTerminal(ctx, req, state)
	}
	return nil
}

func (s *service) finishPlannedTerminal(ctx context.Context, req RunRequest, admission *admission, state *terminalState) []error {
	if msg, ok := admission.ProviderTerminal(); ok {
		state.observe(msg)
		if _, err := s.acceptAndHandle(ctx, req, req.Handle, state, msg); err != nil {
			return []error{named("handle provider terminal", err)}
		}
		state.terminalWritten = true
		return nil
	}
	msg := messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValueWithTerminal(
		"", string(MaxDurationReason), string(MaxDurationReason), MaxDurationReason,
		messages.TerminalProvenanceLoop, state.outputState(),
	)}
	errs := []error{named("accept duration terminal", acceptArtifact(req.Artifacts, msg))}
	if _, err := req.Handle(ctx, msg, state.snapshot()); err != nil {
		errs = append(errs, named("handle duration terminal", err))
	}
	state.terminalWritten = true
	return errs
}

func (s *service) finishProviderTerminal(ctx context.Context, req RunRequest, state *terminalState) []error {
	msg := messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValueWithTerminal(
		"", "provider_close", "provider_close", messages.TerminalReasonProviderClose,
		messages.TerminalProvenanceProvider, state.outputState(),
	)}
	errs := []error{named("accept provider terminal", acceptArtifact(req.Artifacts, msg))}
	if _, err := req.Handle(ctx, msg, state.snapshot()); err != nil {
		errs = append(errs, named("handle provider terminal", err))
	}
	state.terminalWritten = true
	return errs
}

func acceptArtifact(artifacts ArtifactLifecycle, msg messages.StreamMessage) error {
	if artifacts == nil {
		return nil
	}
	return artifacts.Accept(msg)
}

func (s *service) finishErrors(req RunRequest, admission *admission, preferred error) []error {
	var errs []error
	if preferred != nil && !errors.Is(preferred, context.Canceled) {
		errs = append(errs, preferred)
	}
	if err := admission.RuntimeError(); err != nil {
		errs = append(errs, named("session runtime", err))
	}
	if err := admission.CloseError(); err != nil {
		errs = append(errs, named("close session", err))
	}
	return errs
}

func (s *service) finishArtifacts(req RunRequest) error {
	if req.Artifacts == nil {
		return nil
	}
	artifactErr := errors.Join(named("flush duration artifacts", req.Artifacts.Flush()), named("close duration artifacts", req.Artifacts.Close()))
	if req.OnArtifactsFinalized != nil {
		req.OnArtifactsFinalized(artifactErr)
	}
	return artifactErr
}

func (s *service) drain(ctx context.Context, req RunRequest, admission *admission, handle Handle, state *terminalState, planned bool) error {
	period := req.DrainPeriod
	if period <= 0 {
		period = DefaultDrainPeriod
	}
	timer := req.Clock.NewTimer(period)
	if timer == nil {
		return errors.New("session duration clock returned a nil drain timer")
	}
	defer timer.Stop()
	for {
		select {
		case msg, ok := <-handle.Deltas().Chan():
			if !ok {
				return nil
			}
			if shouldSkipDrainedMessage(admission, state, planned, msg) {
				continue
			}
			if err := s.acceptDrainedMessage(ctx, req, state, msg); err != nil {
				return err
			}
		case <-timer.C():
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func shouldSkipDrainedMessage(admission *admission, state *terminalState, planned bool, msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeSessionClose {
		return false
	}
	return (planned && !admission.IsProviderTerminal(msg)) || state.terminalWritten
}

func (s *service) acceptDrainedMessage(ctx context.Context, req RunRequest, state *terminalState, msg messages.StreamMessage) error {
	state.observe(msg)
	if _, err := s.acceptAndHandle(ctx, req, req.Handle, state, msg); err != nil {
		return fmt.Errorf("drain duration message: %w", err)
	}
	if msg.Type == messages.StreamTypeSessionClose {
		state.terminalWritten = true
	}
	return nil
}

func (s *service) flushBuffered(ctx context.Context, req RunRequest, admission *admission, handle Handle, state *terminalState, planned bool) error {
	for {
		msg, ok := handle.Deltas().Read()
		if !ok {
			return nil
		}
		if shouldSkipDrainedMessage(admission, state, planned, msg) {
			continue
		}
		if err := s.acceptDrainedMessage(ctx, req, state, msg); err != nil {
			return fmt.Errorf("flush buffered duration message: %w", err)
		}
	}
}

func timerReady(ch <-chan time.Time) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func cleanupTimeout(value time.Duration) time.Duration {
	if value <= 0 {
		return DefaultCleanupTimeout
	}
	return value
}

func named(label string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", label, err)
}

func normalizeResultError(ctx context.Context, err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("session error: %w", err)
}
