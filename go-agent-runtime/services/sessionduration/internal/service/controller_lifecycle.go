package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func (c *controller) isEmptyResponseLocked(msg messages.StreamMessage) bool {
	value, ok := msg.Value.(*messages.MessageEndValue)
	if !ok || value == nil || c.responseOutput || c.toolObligation || msg.ResponsePurpose == messages.ResponsePurposeToolAcknowledgement {
		return false
	}
	if value.TerminalReason != messages.TerminalReasonPartialOutput || value.OutputState != messages.TerminalOutputNone || value.Usage.CompletionTokens != 0 {
		return false
	}
	if value.TerminalReason == messages.TerminalReasonCancellation || value.TerminalProvenance == messages.TerminalProvenanceLoop || strings.EqualFold(strings.TrimSpace(value.Status), "cancelled") {
		return false
	}
	return true
}

func (c *controller) makeLivenessErrorLocked(msg messages.StreamMessage, timeout bool) error {
	classification := sessionduration.LivenessClassificationEmptyResponse
	cause := sessionduration.ErrProviderEmptyResponse
	if timeout {
		classification = sessionduration.LivenessClassificationTimeout
		cause = sessionduration.ErrProviderLivenessTimeout
	}
	err := &sessionduration.LivenessError{
		Classification:     classification,
		ResponseID:         strings.TrimSpace(msg.ResponseID),
		FailingEvent:       msg.Type,
		TerminalReason:     messages.TerminalReasonTerminalFailure,
		TerminalProvenance: messages.TerminalProvenanceSession,
		OutputState:        messages.TerminalOutputNone,
		Cause:              cause,
	}
	if value, ok := msg.Value.(*messages.MessageEndValue); ok && value != nil {
		err.Usage = value.Usage
	}
	return err
}

func isProviderOutput(msg messages.StreamMessage) bool {
	if msg.Role == messages.RoleUser || msg.Role == messages.RoleTool {
		return false
	}
	//nolint:exhaustive // only provider output boundaries affect liveness.
	switch msg.Type {
	case messages.StreamTypeTextDelta, messages.StreamTypeAudioDelta, messages.StreamTypeImageDelta, messages.StreamTypeVideoDelta, messages.StreamTypeFileDelta, messages.StreamTypeEmbeddingDelta, messages.StreamTypeReasoningDelta, messages.StreamTypeTranscriptDelta, messages.StreamTypeTextEnd, messages.StreamTypeAudioEnd, messages.StreamTypeImageEnd, messages.StreamTypeVideoEnd, messages.StreamTypeFileEnd, messages.StreamTypeEmbeddingEnd, messages.StreamTypeReasoningEnd, messages.StreamTypeTranscriptEnd:
		return true
	default:
		return false
	}
}

func (c *controller) armLiveness(onlyIfArmed bool) {
	if c == nil || !c.options.Liveness.Enabled || c.options.LivenessClock == nil {
		return
	}
	c.armMu.Lock()
	defer c.armMu.Unlock()
	timeout := c.options.Liveness.Timeout
	if timeout <= 0 {
		timeout = defaultLivenessTimeout
	}
	c.mu.Lock()
	if !c.canArmLivenessLocked(onlyIfArmed) {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	timer := c.options.LivenessClock.NewTimer(timeout)
	if timer == nil {
		c.report(sessionduration.ErrSchedulerUnavailable)
		return
	}
	c.mu.Lock()
	if !c.canArmLivenessLocked(onlyIfArmed) {
		c.mu.Unlock()
		timer.Stop()
		return
	}
	old, wake := c.installLivenessLocked(timer)
	c.mu.Unlock()
	if old != nil {
		old.Stop()
	}
	select {
	case wake <- struct{}{}:
	default:
	}
	go c.watchLiveness()
}

func (c *controller) canArmLivenessLocked(onlyIfArmed bool) bool {
	return !c.closed && !c.livenessStopped && !c.localToolActive &&
		(onlyIfArmed && c.livenessArmed || !onlyIfArmed && !c.livenessArmed)
}

func (c *controller) installLivenessLocked(timer sessionTimer) (sessionTimer, chan struct{}) {
	old := c.livenessTimer
	c.livenessTimer = timer
	c.livenessArmed = true
	c.livenessGeneration++
	return old, c.livenessWake
}

func (c *controller) watchLiveness() {
	for {
		c.mu.Lock()
		if c.closed || c.livenessStopped || !c.livenessArmed || c.livenessTimer == nil {
			c.mu.Unlock()
			return
		}
		generation := c.livenessGeneration
		timer := c.livenessTimer
		wake := c.livenessWake
		ctx := c.ctx
		c.mu.Unlock()
		select {
		case <-timer.C():
			c.expireLiveness(generation)
			return
		case <-wake:
		case <-ctx.Done():
			return
		}
	}
}

func (c *controller) expireLiveness(generation uint64) {
	c.mu.Lock()
	if c.closed || c.livenessStopped || c.localToolActive || !c.livenessArmed || c.livenessGeneration != generation || c.livenessFailure != nil {
		c.mu.Unlock()
		return
	}
	err := c.makeLivenessErrorLocked(messages.StreamMessage{}, true)
	c.livenessFailure = err
	c.livenessArmed = false
	timer := c.livenessTimer
	c.livenessTimer = nil
	c.livenessGeneration++
	c.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	c.reportLiveness(err)
}

func (c *controller) stopLiveness() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.livenessArmed && c.livenessTimer == nil {
		c.mu.Unlock()
		return
	}
	c.livenessArmed = false
	c.livenessGeneration++
	timer := c.livenessTimer
	c.livenessTimer = nil
	wake := c.livenessWake
	c.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (c *controller) Errors() <-chan error {
	if c == nil {
		return nil
	}
	return c.errors
}

func (c *controller) LivenessFailure() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.livenessReported {
		return nil
	}
	return c.livenessFailure
}
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
		ctx = c.ctx
		if ctx == nil {
			ctx = context.Background()
		}
	}
	//nolint:contextcheck // finalization cleanup must outlive caller cancellation.
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
	appendFailure("quiesce session input", request.Quiesce)
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
