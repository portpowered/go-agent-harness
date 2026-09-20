package internal

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const (
	sideLoopBufferCapacity = 128
	bridgeReadBufferSize   = 64 * 1024
)

type sideResult struct {
	index   int
	err     error
	started bool
}

func (s *Service) runSide(ctx, callerCtx context.Context, index int, role selfplay.SideRole, inferencer messages.SessionInferencer, ready chan<- *agentloop.AgentLoop, output *pcmBridge, stop *stopState, evidence *evidence, source platformclock.TimerSource) sideResult {
	loop, err := newSideLoop(inferencer)
	if err != nil {
		return failSide(stop, index, role, fmt.Errorf("create %s agent loop: %w", role, err), false)
	}
	select {
	case ready <- loop:
	case <-ctx.Done():
		return sideResult{index: index, err: ctx.Err()}
	}
	loopCtx, cancelLoop := context.WithCancel(ctx)
	defer cancelLoop()
	runResult := startSideLoop(loop, loopCtx, cancelLoop)
	started, runErr, failureErr := s.readSideDeltas(loopCtx, loop, index, role, output, stop, evidence, runResult)
	if runErr == nil {
		runErr = waitSideLoop(source, runResult)
	}
	return finishSide(stop, ctx, callerCtx, index, role, started, runErr, failureErr)
}

func newSideLoop(inferencer messages.SessionInferencer) (*agentloop.AgentLoop, error) {
	return agentloop.New(
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(inferencer),
		agentloop.WithToolExecutionDisabled(),
		agentloop.WithBufferCapacity(sideLoopBufferCapacity),
	)
}

func failSide(stop *stopState, index int, role selfplay.SideRole, err error, started bool) sideResult {
	if stop.fail(err) {
		return sideResult{index: index, err: err, started: started}
	}
	return sideResult{index: index, started: started}
}

func startSideLoop(loop *agentloop.AgentLoop, ctx context.Context, cancel context.CancelFunc) chan error {
	result := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		result <- loop.Run(ctx)
		close(finished)
	}()
	go func() {
		select {
		case <-finished:
			cancel()
		case <-ctx.Done():
		}
	}()
	return result
}

func (s *Service) readSideDeltas(ctx context.Context, loop *agentloop.AgentLoop, index int, role selfplay.SideRole, output *pcmBridge, stop *stopState, evidence *evidence, runResult chan error) (bool, error, error) {
	started := false
	openingSent := false
	for {
		message, err := loop.Deltas().ReadContext(ctx)
		if err != nil {
			return started, completedRunError(runResult, err), nil
		}
		started = started || message.Type == messages.StreamTypeSessionOpen || message.Type == messages.StreamTypeError
		if stop.stopped() {
			return started, nil, nil
		}
		if err := s.handleSideMessage(ctx, loop, index, role, message, &openingSent, output, stop, evidence); err != nil {
			failure := failSide(stop, index, role, err, started).err
			return started, err, failure
		}
	}
}

func completedRunError(result chan error, readErr error) error {
	select {
	case err := <-result:
		result <- err
		return err
	default:
		return readErr
	}
}

func (s *Service) handleSideMessage(ctx context.Context, loop *agentloop.AgentLoop, index int, role selfplay.SideRole, message messages.StreamMessage, openingSent *bool, output *pcmBridge, stop *stopState, evidence *evidence) error {
	if err := evidence.observe(index, message); err != nil {
		return err
	}
	if err := sendCustomerOpening(ctx, loop, index, message, openingSent); err != nil {
		return err
	}
	if err := s.handleSideAudio(ctx, index, role, message, output, stop, evidence); err != nil {
		return err
	}
	if err := terminalStreamError(message); err != nil {
		return fmt.Errorf("%s provider stream: %w", role, err)
	}
	return recordCompletedTurn(index, message, stop, evidence)
}

func sendCustomerOpening(ctx context.Context, loop *agentloop.AgentLoop, index int, message messages.StreamMessage, sent *bool) error {
	if index != 0 || message.Type != messages.StreamTypeSessionOpen || *sent {
		return nil
	}
	*sent = true
	return loop.Send(ctx, []messages.Message{messages.NewTextMessage(messages.RoleUser, selfplay.SelfPlayOpeningSeed)})
}

func (s *Service) handleSideAudio(ctx context.Context, index int, role selfplay.SideRole, message messages.StreamMessage, output *pcmBridge, stop *stopState, evidence *evidence) error {
	if message.Type != messages.StreamTypeAudioDelta || !assistantAudio(message) {
		return nil
	}
	value, ok := message.Value.(*messages.AudioDeltaValue)
	if !ok {
		return fmt.Errorf("%s emitted an audio delta with value %T", role, message.Value)
	}
	if len(value.Content)%2 != 0 {
		return fmt.Errorf("%s emitted odd-length PCM16 audio", role)
	}
	if err := evidence.observeAudio(ctx, index, value.Content); err != nil {
		return err
	}
	if err := output.write(value.Content); err != nil && !stop.stopped() {
		return fmt.Errorf("%s PCM bridge write: %w", role, err)
	}
	return nil
}

func recordCompletedTurn(index int, message messages.StreamMessage, stop *stopState, evidence *evidence) error {
	if message.Type != messages.StreamTypeMessageEnd || !assistantMessage(message) {
		return nil
	}
	turn, accepted := stop.recordTurn(index, message.ResponseID)
	if !accepted {
		return nil
	}
	if err := evidence.recordTurn(index, turn); err != nil {
		return err
	}
	stop.commitTurnTarget()
	return nil
}

func waitSideLoop(source platformclock.TimerSource, runResult <-chan error) error {
	timer := source.NewTimer(shutdownDeadline)
	defer timer.Stop()
	select {
	case err := <-runResult:
		return err
	case <-timer.C():
		return selfplay.ErrShutdownTimeout
	}
}

func finishSide(stop *stopState, runCtx, callerCtx context.Context, index int, role selfplay.SideRole, started bool, runErr, failureErr error) sideResult {
	if failureErr != nil {
		return sideResult{index: index, err: failureErr, started: started}
	}
	if !stop.stopped() {
		failureErr = sideStopFailure(stop, runCtx, callerCtx, role, runErr)
	}
	if failureErr != nil {
		return sideResult{index: index, err: failureErr, started: started}
	}
	if stop.stopped() && contextStopError(runErr) {
		return sideResult{index: index, started: started}
	}
	return sideResult{index: index, err: runErr, started: started}
}

func sideStopFailure(stop *stopState, runCtx, callerCtx context.Context, role selfplay.SideRole, runErr error) error {
	if runCtx.Err() != nil && callerCtx.Err() == nil {
		stop.commit(selfplay.StopMaxDuration, nil)
		return nil
	}
	if callerCtx.Err() != nil {
		err := callerCtx.Err()
		if stop.fail(err) {
			return err
		}
		return nil
	}
	if runErr == nil {
		runErr = fmt.Errorf("%s session closed before reaching the turn target", role)
	}
	if stop.fail(runErr) {
		return runErr
	}
	return nil
}

func contextStopError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func pumpPCM(ctx context.Context, bridge *pcmBridge, target <-chan *agentloop.AgentLoop) error {
	loop, err := awaitTargetLoop(ctx, target)
	if err != nil || loop == nil {
		return err
	}
	return forwardBridgePCM(ctx, loop, bridge.reader)
}

func awaitTargetLoop(ctx context.Context, target <-chan *agentloop.AgentLoop) (*agentloop.AgentLoop, error) {
	select {
	case loop := <-target:
		if loop == nil {
			return nil, errors.New("self-play PCM target loop is unavailable")
		}
		return loop, nil
	case <-ctx.Done():
		return nil, nil
	}
}

func forwardBridgePCM(ctx context.Context, loop *agentloop.AgentLoop, reader io.Reader) error {
	buffer := make([]byte, bridgeReadBufferSize)
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			if sendErr := sendBridgePCM(ctx, loop, buffer[:count]); sendErr != nil {
				return sendErr
			}
		}
		if err != nil {
			return err
		}
	}
}

func sendBridgePCM(ctx context.Context, loop *agentloop.AgentLoop, chunk []byte) error {
	pcm := append([]byte(nil), chunk...)
	return loop.SendAudioInput(ctx, pcm)
}

func assistantAudio(message messages.StreamMessage) bool {
	return message.Role == "" || message.Role == messages.RoleAssistant
}

func assistantMessage(message messages.StreamMessage) bool {
	return message.Role == "" || message.Role == messages.RoleAssistant
}

func terminalStreamError(message messages.StreamMessage) error {
	if message.Type == messages.StreamTypeError {
		return terminalProviderError(message.Value)
	}
	if message.Type == messages.StreamTypeMessageEnd {
		return terminalResponseError(message.Value)
	}
	return nil
}

func terminalProviderError(raw any) error {
	value, ok := raw.(*messages.ErrorValue)
	if !ok || value == nil {
		return errors.New("provider emitted an invalid error event")
	}
	if value.IsNonTerminal() {
		return nil
	}
	if value.Err != nil {
		return value.Err
	}
	if value.Message != "" {
		return errors.New(value.Message)
	}
	return errors.New("provider emitted a terminal error event")
}

func terminalResponseError(raw any) error {
	value, ok := raw.(*messages.MessageEndValue)
	if !ok || value == nil || (value.Status != "failed" && value.Status != "incomplete") {
		return nil
	}
	if value.ProviderErrorMessage != "" {
		return errors.New(value.ProviderErrorMessage)
	}
	return fmt.Errorf("provider response ended with status %q", value.Status)
}
