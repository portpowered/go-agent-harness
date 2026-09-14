package service

import (
	"strings"

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
	classification := "silent_provider_empty_response"
	cause := sessionduration.ErrProviderEmptyResponse
	if timeout {
		classification = "silent_provider_timeout"
		cause = sessionduration.ErrProviderLivenessTimeout
	}
	err := &sessionduration.LivenessError{
		Classification:     classification,
		ResponseID:         strings.TrimSpace(msg.ResponseID),
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
