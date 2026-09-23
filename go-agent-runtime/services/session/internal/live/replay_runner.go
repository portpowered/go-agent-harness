package live

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audiosubsystem "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems/audio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func (h *handle) runReplay(ctx context.Context) {
	defer h.runWG.Done()
	plan := h.request.ReplayPlan
	if plan == nil || len(plan.AudioTurns) == 0 {
		return
	}
	if err := h.prepareReplay(ctx); err != nil {
		h.cancelReplayOnError(ctx, "prepare replay", err)
		return
	}
	for turnIndex, turn := range plan.AudioTurns {
		if err := h.sendReplayTurn(ctx, turnIndex, turn); err != nil {
			h.cancelReplayOnError(ctx, "replay audio", err)
			return
		}
		// Keep each replay append behind the preceding response terminal so
		// provider admission preserves source-session causal ordering.
		if turnIndex+1 < len(plan.AudioTurns) {
			if err := h.waitReplayResponse(ctx, turnIndex+1); err != nil {
				h.cancelReplayOnError(ctx, fmt.Sprintf("wait for replay response %d", turnIndex+1), err)
				return
			}
		}
	}
	// Mark admission after every captured turn crosses bounded ingress.
	h.markCaptureComplete()
}

func (h *handle) prepareReplay(ctx context.Context) error {
	if err := h.waitReplayReady(ctx); err != nil {
		return fmt.Errorf("wait for replay provider readiness: %w", err)
	}
	if err := h.media.WaitReady(ctx); err != nil {
		return fmt.Errorf("wait for replay media: %w", err)
	}
	return nil
}

func (h *handle) sendReplayTurn(ctx context.Context, turnIndex int, turn session.LiveReplayAudioTurn) error {
	for chunkIndex, samples := range turn.Chunks {
		if len(samples) == 0 {
			continue
		}
		if err := h.media.Endpoints().Outbound.WriteFrame(ctx, sharedaudio.PCMFrame{Samples: samples}); err != nil {
			return fmt.Errorf("send replay audio turn %d chunk %d: %w", turnIndex+1, chunkIndex+1, err)
		}
	}
	if err := h.Send(ctx, session.LiveControl{Kind: session.LiveControlAudioCommit}); err != nil {
		return fmt.Errorf("commit replay audio turn %d: %w", turnIndex+1, err)
	}
	return nil
}

func (h *handle) cancelReplayOnError(ctx context.Context, operation string, err error) {
	if err == nil || ctx == nil || ctx.Err() != nil {
		return
	}
	h.Cancel(fmt.Errorf("%s: %w", operation, err))
}

func (h *handle) waitReplayReady(ctx context.Context) error {
	if h == nil || h.request.ReplayPlan == nil || !h.request.ReplayPlan.WaitForSessionUpdated {
		return nil
	}
	if ctx == nil {
		return errors.New("replay readiness context is required")
	}
	select {
	case <-h.replayReady:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *handle) waitReplayResponse(ctx context.Context, target int) error {
	return h.waitForResponse(ctx, target)
}

// waitForResponse waits for the requested number of non-tool response
// terminals. It is shared by replay and finite multi-turn capture workers so
// a later source cannot overtake the response that closes the preceding turn.
func (h *handle) waitForResponse(ctx context.Context, target int) error {
	if h == nil {
		return context.Canceled
	}
	if ctx == nil {
		return errors.New("response wait context is required")
	}
	for {
		h.mu.Lock()
		if h.replayResponses >= target {
			h.mu.Unlock()
			return nil
		}
		wake := h.replayResponseWake
		h.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// waitForResponseBoundary includes partial assistant terminals produced by
// barge-in cancellation.
func (h *handle) waitForResponseBoundary(ctx context.Context, target int) error {
	if h == nil {
		return context.Canceled
	}
	if ctx == nil {
		return errors.New("response boundary context is required")
	}
	for {
		h.mu.Lock()
		ready := h.observedResponseTerminals >= target && !h.responseActive && !h.responsePending
		terminalWake, responseWake := h.responseTerminalWake, h.replayResponseWake
		h.mu.Unlock()
		if ready {
			return nil
		}
		select {
		case <-terminalWake:
		case <-responseWake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// DuplexLoopFactory builds the session loop from values admitted by the
// session-duration contract; providers and hosts cannot supply raw options.
type DuplexLoopFactory struct{}

func NewDuplexLoopFactory() *DuplexLoopFactory { return &DuplexLoopFactory{} }

func (*DuplexLoopFactory) Build(ctx context.Context, inferencer messages.SessionInferencer, config sessionduration.DuplexLoopOptions) (sessionduration.Loop, error) {
	if err := validateDuplexBuild(ctx, inferencer); err != nil {
		return nil, err
	}
	return agentloop.New(duplexBuildOptions(inferencer, config)...)
}

func validateDuplexBuild(ctx context.Context, inferencer messages.SessionInferencer) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if inferencer == nil {
		return errors.New("session execution inferencer is required")
	}
	return nil
}

func duplexBuildOptions(inferencer messages.SessionInferencer, config sessionduration.DuplexLoopOptions) []agentloop.Option {
	options := []agentloop.Option{agentloop.WithMode(engine.DuplexSession), agentloop.WithSessionInferencer(inferencer)}
	options = appendAudioPortOption(options, config.AudioPorts)
	if config.ToolExecutor == nil {
		return append(options, agentloop.WithToolExecutionDisabled())
	}
	options = appendToolDefinitionOptions(options, config)
	options = append(options, agentloop.WithToolExecutor(config.ToolExecutor))
	return appendToolAcknowledgementOption(options, config.ToolAcknowledgementPolicy)
}

func appendAudioPortOption(options []agentloop.Option, ports *audiosubsystem.Ports) []agentloop.Option {
	if ports != nil && (ports.Capture != nil || ports.Playback != nil || ports.Commands != nil) {
		return append(options, agentloop.WithAudioSubsystem(audiosubsystem.New(*ports)))
	}
	return options
}

func appendToolDefinitionOptions(options []agentloop.Option, config sessionduration.DuplexLoopOptions) []agentloop.Option {
	definitions := append([]messages.ToolDefinition(nil), config.ToolDefinitions...)
	if len(definitions) == 0 {
		return options
	}
	options = append(options, agentloop.WithTools(definitions))
	if config.AdvertiseToolDefinitions {
		options = append(options, agentloop.WithSessionConfig(messages.SessionUpdateConfig{Tools: definitions}))
	}
	return options
}

func appendToolAcknowledgementOption(options []agentloop.Option, policy *sessionduration.DuplexToolAcknowledgementPolicy) []agentloop.Option {
	if policy == nil {
		return options
	}
	longRunning := make(map[string]struct{}, len(policy.LongRunningToolNames))
	for _, name := range policy.LongRunningToolNames {
		if name != "" {
			longRunning[name] = struct{}{}
		}
	}
	return append(options, agentloop.WithToolAcknowledgementPolicy(agentloop.ToolAcknowledgementPolicy{
		Threshold: policy.Threshold,
		IsLongRunning: func(name string) bool {
			_, ok := longRunning[name]
			return ok
		},
	}))
}

var _ sessionduration.DuplexLoopFactory = (*DuplexLoopFactory)(nil)
