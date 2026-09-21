package internal

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type sideWorkers struct {
	ready   [2]chan *agentloop.AgentLoop
	results chan sideResult
	wait    *sync.WaitGroup
}

type sideCompletion struct {
	received int
	errors   [2]error
	started  [2]bool
}

func (s *Service) runConversation(runCtx, callerCtx context.Context, cancel context.CancelFunc, source platformclock.TimerSource, request selfplay.Request, customer, assistant messages.SessionInferencer, evidence *evidence) (selfplay.Result, error) {
	bridges := [2]*pcmBridge{newPCMBridge(), newPCMBridge()}
	stop := newStopState(request.MaxTurns, func() {
		bridges[0].close()
		bridges[1].close()
		cancel()
	})
	roles := [2]selfplay.SideRole{selfplay.RoleCustomer, selfplay.RoleAssistant}
	workers := s.startSideWorkers(runCtx, callerCtx, source, [2]messages.SessionInferencer{customer, assistant}, bridges, roles, stop, evidence)
	var pumpWait sync.WaitGroup
	s.startPCMPumps(runCtx, bridges, workers.ready, stop, &pumpWait)
	completion := awaitTerminal(runCtx, callerCtx, roles, workers.results, stop)
	terminal := stop.snapshot()
	shutdownErr := joinWorkers(source, workers.wait, &pumpWait, workers.results, &completion)
	recordSideTerminals(request.APIKey, roles, terminal, shutdownErr, completion, evidence)
	return makeConversationResult(terminal, roles, evidence), errors.Join(terminal.err, shutdownErr, sideShutdownError(completion))
}

func sideShutdownError(completion sideCompletion) error {
	for _, err := range completion.errors {
		if errors.Is(err, selfplay.ErrShutdownTimeout) {
			return err
		}
	}
	return nil
}

func (s *Service) startSideWorkers(runCtx, callerCtx context.Context, source platformclock.TimerSource, inferencers [2]messages.SessionInferencer, bridges [2]*pcmBridge, roles [2]selfplay.SideRole, stop *stopState, evidence *evidence) sideWorkers {
	workers := sideWorkers{ready: [2]chan *agentloop.AgentLoop{make(chan *agentloop.AgentLoop, 1), make(chan *agentloop.AgentLoop, 1)}, results: make(chan sideResult, 2), wait: &sync.WaitGroup{}}
	outputs := [2]*pcmBridge{bridges[0], bridges[1]}
	for side := range inferencers {
		workers.wait.Add(1)
		go func(side int) {
			defer workers.wait.Done()
			workers.results <- s.runSide(runCtx, callerCtx, side, roles[side], inferencers[side], workers.ready[side], outputs[side], stop, evidence, source)
		}(side)
	}
	return workers
}

func (s *Service) startPCMPumps(ctx context.Context, bridges [2]*pcmBridge, ready [2]chan *agentloop.AgentLoop, stop *stopState, wait *sync.WaitGroup) {
	startPCMPump(ctx, bridges[0], ready[1], "customer-to-assistant", stop, wait)
	startPCMPump(ctx, bridges[1], ready[0], "assistant-to-customer", stop, wait)
}

func startPCMPump(ctx context.Context, bridge *pcmBridge, ready <-chan *agentloop.AgentLoop, direction string, stop *stopState, wait *sync.WaitGroup) {
	wait.Add(1)
	go func() {
		defer wait.Done()
		if err := pumpPCM(ctx, bridge, ready); err != nil && !stop.stopped() {
			stop.fail(fmt.Errorf("%s PCM bridge: %w", direction, err))
		}
	}()
}

func awaitTerminal(runCtx, callerCtx context.Context, roles [2]selfplay.SideRole, results <-chan sideResult, stop *stopState) sideCompletion {
	var completion sideCompletion
	for {
		select {
		case side := <-results:
			completion.add(side)
			if !stop.stopped() {
				stop.fail(sideEndError(roles[side.index], side.err))
			}
		case <-stop.done:
			return completion
		case <-runCtx.Done():
			stopOnContext(callerCtx, stop)
		}
	}
}

func sideEndError(role selfplay.SideRole, err error) error {
	if err != nil {
		return fmt.Errorf("%s session: %w", role, err)
	}
	return fmt.Errorf("%s session ended before the self-play bound", role)
}

func stopOnContext(callerCtx context.Context, stop *stopState) {
	if callerCtx.Err() != nil {
		stop.fail(callerCtx.Err())
		return
	}
	stop.commit(selfplay.StopMaxDuration, nil)
}

func (c *sideCompletion) add(side sideResult) {
	c.received++
	c.errors[side.index] = side.err
	c.started[side.index] = side.started
}

func joinWorkers(source platformclock.TimerSource, sides, pumps *sync.WaitGroup, results <-chan sideResult, completion *sideCompletion) error {
	finished := make(chan struct{})
	go func() {
		sides.Wait()
		pumps.Wait()
		close(finished)
	}()
	timer := source.NewTimer(shutdownDeadline)
	defer timer.Stop()
	select {
	case <-finished:
	case <-timer.C():
		return selfplay.ErrShutdownTimeout
	}
	for completion.received < 2 {
		completion.add(<-results)
	}
	return nil
}

func recordSideTerminals(secret string, roles [2]selfplay.SideRole, terminal terminalSnapshot, shutdownErr error, completion sideCompletion, evidence *evidence) {
	for side := range roles {
		state := selfplay.SideNotStarted
		if completion.started[side] {
			state = selfplay.SideStopped
		}
		if errors.Is(shutdownErr, selfplay.ErrShutdownTimeout) || isFailedSide(terminal, completion.errors[side]) {
			state = selfplay.SideFailed
		}
		evidence.setTerminal(side, state, completion.errors[side], secret)
	}
}

func isFailedSide(terminal terminalSnapshot, sideErr error) bool {
	return terminal.reason == selfplay.StopFailure && sideErr != nil &&
		!errors.Is(sideErr, context.Canceled) && !errors.Is(sideErr, context.DeadlineExceeded)
}

func makeConversationResult(terminal terminalSnapshot, roles [2]selfplay.SideRole, evidence *evidence) selfplay.Result {
	return selfplay.Result{
		StopReason: terminal.reason,
		Customer:   selfplay.SideResult{Role: roles[0], CompletedTurns: terminal.turns[0], Terminal: evidence.sides[0].terminal, TerminalError: evidence.sides[0].terminalErr},
		Assistant:  selfplay.SideResult{Role: roles[1], CompletedTurns: terminal.turns[1], Terminal: evidence.sides[1].terminal, TerminalError: evidence.sides[1].terminalErr},
	}
}
