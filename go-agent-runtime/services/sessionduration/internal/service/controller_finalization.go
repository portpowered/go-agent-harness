package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func (c *controller) Finalize(ctx context.Context, request sessionduration.FinalizeRequest) (result sessionduration.Result, err error) {
	if c == nil {
		return sessionduration.Result{}, request.Primary
	}
	c.finalizeOnce.Do(func() {
		c.mu.Lock()
		c.livenessStopped = true
		maxTimer := c.maxTimer
		liveTimer := c.livenessTimer
		c.maxTimer, c.livenessTimer = nil, nil
		c.mu.Unlock()
		if maxTimer != nil {
			maxTimer.Stop()
		}
		if liveTimer != nil {
			liveTimer.Stop()
		}
		c.cancel()
		failures := c.cleanup(ctx, request)
		c.mu.Lock()
		c.closed = true
		c.finalizeResult = sessionduration.Result{OutputState: c.outputState, TerminalWritten: c.terminalWritten, Expired: c.expired}
		c.finalizeErr = errors.Join(request.Primary, errors.Join(failures...))
		c.mu.Unlock()
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.finalizeResult, c.finalizeErr
}

//nolint:contextcheck // Finalization must outlive caller cancellation and accepts nil contexts.
func (c *controller) cleanup(ctx context.Context, request sessionduration.FinalizeRequest) []error {
	if ctx == nil {
		ctx = c.ctx
		if ctx == nil {
			ctx = context.Background()
		}
	}
	ctx = context.WithoutCancel(ctx)
	var failures []error
	appendFailure := func(label string, cleanup func() error) {
		if cleanup == nil {
			return
		}
		if cleanupErr := invokeCleanup(cleanup); cleanupErr != nil {
			failures = append(failures, fmt.Errorf("%s: %w", label, cleanupErr))
		}
	}
	if request.DrainLoop != nil {
		appendFailure("drain session", func() error { return c.drainLoop(ctx, request.DrainLoop, request.DrainPolicy) })
	}
	if request.Drain != nil {
		appendFailure("drain session resources", func() error {
			_, wallSafety := drainDurations(request.DrainPolicy)
			drainCtx, cancel := context.WithTimeout(ctx, wallSafety)
			defer cancel()
			return request.Drain(drainCtx)
		})
	}
	appendFailure("close session", request.Close)
	appendFailure("publish max duration", c.publishExpiredTerminal)
	appendFailure("close device binding", request.Binding)
	artifacts := request.Artifacts
	if artifacts == nil {
		artifacts = c.options.Artifacts
	}
	if artifacts != nil {
		appendFailure("flush artifacts", artifacts.Flush)
		appendFailure("close artifacts", artifacts.Close)
	}
	return failures
}

func (c *controller) publishExpiredTerminal() error {
	c.mu.Lock()
	if !c.expired || c.terminalWritten {
		c.mu.Unlock()
		return nil
	}
	output := c.outputState
	providerMessage, providerSeen := messages.StreamMessage{}, false
	if c.options.Terminal.Message != nil {
		providerMessage, providerSeen = c.options.Terminal.Message()
	}
	c.mu.Unlock()
	if providerSeen {
		if err := publish(c.options.Publication, providerMessage); err != nil {
			return err
		}
	} else if err := publishMaxDuration(c.options.Publication, output); err != nil {
		return err
	}
	c.mu.Lock()
	c.terminalWritten = true
	c.mu.Unlock()
	return nil
}

func invokeCleanup(cleanup func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %v", sessionduration.ErrFinalizationPanic, recovered)
		}
	}()
	return cleanup()
}

func (s *Service) NewFinalizer(ports sessionduration.FinalizationPorts) sessionduration.Finalizer {
	return &finalizer{ports: ports}
}

type finalizer struct {
	ports   sessionduration.FinalizationPorts
	binding func() error
	once    sync.Once
	mu      sync.Mutex
	err     error
}

func (f *finalizer) SetDeviceBinding(binding func() error) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.binding = binding
	f.mu.Unlock()
}

func (f *finalizer) Finish(ctx context.Context, out io.Writer, primary error) error {
	if f == nil {
		return primary
	}
	f.once.Do(func() {
		cleanupErr := f.cleanup(ctx, out)
		f.mu.Lock()
		f.err = cleanupErr
		f.mu.Unlock()
	})
	f.mu.Lock()
	cleanupErr := f.err
	f.mu.Unlock()
	return errors.Join(primary, cleanupErr)
}

func (f *finalizer) cleanup(ctx context.Context, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	if ctx == nil {
		ctx = context.Background() //nolint:contextcheck // standalone finalization has no caller context to inherit.
	}
	var failures []error
	appendFailure := func(label string, cleanup func() error) {
		if err := invokeFinalizer(cleanup); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", label, err))
		}
	}

	appendFailure("close session capabilities", f.ports.CloseCapabilities)
	appendFailure("close WebRTC provider session", f.ports.CloseSession)
	f.mu.Lock()
	binding := f.binding
	if binding == nil {
		binding = f.ports.CloseBinding
	}
	f.mu.Unlock()
	appendFailure("close RTC device binding", binding)
	appendFailure("close WebRTC runtime", f.ports.CloseRuntime)
	appendFailure("flush capture", f.ports.FlushCapture)
	if f.ports.Finalize != nil {
		appendFailure("finalize session", func() error { return f.ports.Finalize(ctx, out) })
	}
	appendFailure("release capture", f.ports.ReleaseCapture)
	return errors.Join(failures...)
}

func invokeFinalizer(cleanup func() error) (err error) {
	if cleanup == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %v", sessionduration.ErrFinalizationPanic, recovered)
		}
	}()
	return cleanup()
}

var _ sessionduration.Finalizer = (*finalizer)(nil)

func (c *controller) report(err error) {
	if err == nil || c == nil {
		return
	}
	select {
	case c.errors <- err:
	default:
	}
	if c.options.FirstCause != nil {
		c.firstCauseOnce.Do(func() { c.options.FirstCause(err) })
	}
}

func (c *controller) reportLiveness(err error) {
	c.report(err)
	c.mu.Lock()
	c.livenessReported = true
	c.mu.Unlock()
}

func (c *controller) Expire() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed || c.expired {
		c.mu.Unlock()
		return sessionduration.ErrMaxDurationExceeded
	}
	c.expired = true
	c.mu.Unlock()
	c.mu.Lock()
	if !c.durationReported {
		c.durationReported = true
		c.mu.Unlock()
		c.report(sessionduration.ErrMaxDurationExceeded)
		return sessionduration.ErrMaxDurationExceeded
	}
	c.mu.Unlock()
	return sessionduration.ErrMaxDurationExceeded
}

func (c *controller) Retry(request sessionduration.RetryRequest) sessionduration.RetryDecision {
	if c == nil || request.Terminal == nil {
		return sessionduration.RetryDecision{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.expired || !c.options.Retry.Enabled {
		return sessionduration.RetryDecision{}
	}
	decision := EvaluateRetry(c.options.Retry, request.Terminal)
	if !decision.Eligible {
		return decision
	}
	maxRetries := c.options.Retry.MaxRetries
	if maxRetries == 0 {
		maxRetries = 1
	}
	if c.retriesUsed >= maxRetries {
		return sessionduration.RetryDecision{Exhausted: true}
	}
	c.retriesUsed++
	return decision
}

func (c *controller) OutputState() messages.TerminalOutputState {
	if c == nil {
		return messages.TerminalOutputNone
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.outputState
}

func (c *controller) TerminalWritten() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.terminalWritten
}
