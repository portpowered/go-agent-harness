package service

import (
	"context"
	"errors"
	"fmt"

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

func (c *controller) cleanup(ctx context.Context, request sessionduration.FinalizeRequest) []error {
	if ctx == nil {
		//nolint:contextcheck // finalization cleanup must outlive caller cancellation.
		ctx = context.WithoutCancel(c.ctx)
	}
	var failures []error
	appendFailure := func(label string, cleanup func() error) {
		if cleanup == nil {
			return
		}
		if cleanupErr := invokeCleanup(cleanup); cleanupErr != nil {
			failures = append(failures, fmt.Errorf("%s: %w", label, cleanupErr))
		}
	}
	if request.Drain != nil {
		appendFailure("drain session", func() error { return request.Drain(ctx) })
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
			err = fmt.Errorf("session finalization panicked: %v", recovered)
		}
	}()
	return cleanup()
}
