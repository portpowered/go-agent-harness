package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
)

type sideResult struct {
	name string
	err  error
}

type terminalSnapshot struct {
	result selfplay.Result
	err    error
}

type stopState struct {
	done         chan struct{}
	bridgeCancel context.CancelFunc
	once         sync.Once
	mu           sync.Mutex
	turns        [2]int
	terminal     *terminalSnapshot
}

func newStopState(bridgeCancel context.CancelFunc) *stopState {
	return &stopState{done: make(chan struct{}), bridgeCancel: bridgeCancel}
}

func (s *stopState) stop(reason selfplay.StopReason, err error) bool {
	if reason == "" {
		reason = selfplay.StopFailure
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal != nil {
		return false
	}
	s.terminal = &terminalSnapshot{result: selfplay.Result{StopReason: reason, CustomerTurns: s.turns[0], AssistantTurns: s.turns[1]}, err: err}
	s.once.Do(func() {
		close(s.done)
		if s.bridgeCancel != nil {
			s.bridgeCancel()
		}
	})
	return true
}

func (s *stopState) fail(err error) bool {
	if err == nil {
		err = errors.New("self-play stopped because an agent failed")
	}
	return s.stop(selfplay.StopFailure, err)
}

func (s *stopState) doneErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal == nil {
		return nil
	}
	return s.terminal.err
}

func (s *stopState) stopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminal != nil
}

func (s *stopState) recordTurn(side, target int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if side < 0 || side >= len(s.turns) || target <= 0 || s.terminal != nil || s.turns[side] >= target {
		return false
	}
	s.turns[side]++
	if s.turns[0] == target && s.turns[1] == target {
		s.stopLocked(selfplay.StopTurnTarget, nil)
	}
	return true
}

func (s *stopState) stopLocked(reason selfplay.StopReason, err error) {
	if s.terminal != nil {
		return
	}
	s.terminal = &terminalSnapshot{result: selfplay.Result{StopReason: reason, CustomerTurns: s.turns[0], AssistantTurns: s.turns[1]}, err: err}
	s.once.Do(func() {
		close(s.done)
		if s.bridgeCancel != nil {
			s.bridgeCancel()
		}
	})
}

func (s *stopState) snapshot() (selfplay.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal != nil {
		return s.terminal.result, s.terminal.err
	}
	return selfplay.Result{CustomerTurns: s.turns[0], AssistantTurns: s.turns[1]}, nil
}

type pcmBridge struct {
	reader *io.PipeReader
	writer *io.PipeWriter
	once   sync.Once
	mu     sync.Mutex
	closed bool
}

func newPCMBridge(ctx context.Context) *pcmBridge {
	reader, writer := io.Pipe()
	bridge := &pcmBridge{reader: reader, writer: writer}
	go func() {
		<-ctx.Done()
		bridge.close()
	}()
	return bridge
}

func (b *pcmBridge) write(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	n, err := b.writer.Write(pcm)
	if err != nil {
		return err
	}
	if n != len(pcm) {
		return io.ErrShortWrite
	}
	return nil
}

func (b *pcmBridge) pump(ctx context.Context, ready <-chan selfplay.AudioInput, fail func(error), name string, observe func([]byte)) {
	input, ok := waitForAudioInput(ctx, ready)
	if !ok {
		return
	}
	buffer := make([]byte, 64*1024)
	for {
		count, err := b.reader.Read(buffer)
		if count > 0 && !b.forward(input, ctx, buffer[:count], fail, name, observe) {
			return
		}
		if err != nil {
			b.handleReadError(err, fail, name)
			return
		}
	}
}

func waitForAudioInput(ctx context.Context, ready <-chan selfplay.AudioInput) (selfplay.AudioInput, bool) {
	select {
	case input, ok := <-ready:
		return input, ok && input != nil
	case <-ctx.Done():
		return nil, false
	}
}

func (b *pcmBridge) forward(input selfplay.AudioInput, ctx context.Context, raw []byte, fail func(error), name string, observe func([]byte)) bool {
	pcm := append([]byte(nil), raw...)
	if sendErr := input.SendAudioInput(ctx, pcm); sendErr != nil {
		if !isCancellation(sendErr) {
			fail(fmt.Errorf("%s PCM bridge send: %w", name, sendErr))
		}
		return false
	}
	if observe != nil {
		observe(pcm)
	}
	return true
}

func (b *pcmBridge) handleReadError(err error, fail func(error), name string) {
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if !closed && !isCancellation(err) {
		fail(fmt.Errorf("%s PCM bridge read: %w", name, err))
	}
}

func (b *pcmBridge) close() {
	b.once.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
		_ = b.writer.Close()
		_ = b.reader.Close()
	})
}

func isCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrClosedPipe)
}

type conversationRuntime struct {
	service             *Service
	ctx                 context.Context
	options             selfplay.RunOptions
	customer            messages.SessionInferencer
	assistant           messages.SessionInferencer
	evidence            *evidence
	closeSessions       func() error
	bridgeCtx           context.Context
	bridgeCancel        context.CancelFunc
	stop                *stopState
	customerReady       chan selfplay.AudioInput
	assistantReady      chan selfplay.AudioInput
	customerToAssistant *pcmBridge
	assistantToCustomer *pcmBridge
	results             chan sideResult
	bridgeWG            sync.WaitGroup
}

func newConversationRuntime(service *Service, ctx context.Context, options selfplay.RunOptions, customer, assistant messages.SessionInferencer, evidence *evidence, closeSessions func() error) *conversationRuntime {
	bridgeCtx, bridgeCancel := context.WithCancel(context.Background())
	return &conversationRuntime{
		service: service, ctx: ctx, options: options, customer: customer, assistant: assistant,
		evidence: evidence, closeSessions: closeSessions, bridgeCtx: bridgeCtx, bridgeCancel: bridgeCancel,
		stop: newStopState(bridgeCancel), customerReady: make(chan selfplay.AudioInput, 1),
		assistantReady: make(chan selfplay.AudioInput, 1), customerToAssistant: newPCMBridge(bridgeCtx),
		assistantToCustomer: newPCMBridge(bridgeCtx), results: make(chan sideResult, 2),
	}
}

func (c *conversationRuntime) startBridges() {
	c.bridgeWG.Add(2)
	go func() {
		defer c.bridgeWG.Done()
		c.customerToAssistant.pump(c.bridgeCtx, c.assistantReady, func(err error) { c.stop.fail(err) }, "customer-to-assistant", func(pcm []byte) { c.evidence.observeInput(1, pcm) })
	}()
	go func() {
		defer c.bridgeWG.Done()
		c.assistantToCustomer.pump(c.bridgeCtx, c.customerReady, func(err error) { c.stop.fail(err) }, "assistant-to-customer", func(pcm []byte) { c.evidence.observeInput(0, pcm) })
	}()
}

func (c *conversationRuntime) startSides() {
	go c.runSide("customer", 0, c.customer, selfplay.SelfPlayOpeningSeed, c.customerToAssistant, c.customerReady)
	go c.runSide("assistant", 1, c.assistant, "", c.assistantToCustomer, c.assistantReady)
}

func (c *conversationRuntime) runSide(name string, side int, session messages.SessionInferencer, prompt string, output *pcmBridge, ready chan<- selfplay.AudioInput) {
	err := c.service.deps.Runner.Run(c.ctx, session, selfplay.SessionRunOptions{
		Prompt: prompt, Provider: c.options.Provider, Model: c.options.Model, Clock: c.service.deps.Clock,
		Done: c.stop.done, DoneErr: c.stop.doneErr, Ready: ready,
		AdmitTurn:         func(messages.StreamMessage) bool { return c.stop.recordTurn(side, c.options.MaxTurns) },
		ObserveDiagnostic: func(diagnostic selfplay.Diagnostic) { c.observeDiagnostic(name, side, diagnostic) },
		ObserveStream:     func(msg messages.StreamMessage) { c.observeStream(name, side, output, msg) },
	})
	c.results <- sideResult{name: name, err: err}
}

func (c *conversationRuntime) observeDiagnostic(name string, side int, diagnostic selfplay.Diagnostic) {
	if err := c.evidence.observeDiagnostic(side, diagnostic); err != nil {
		c.failEvidence(name, "diagnostic", err)
		return
	}
	if diagnostic.Event != "session_failure" {
		return
	}
	failure := strings.TrimSpace(diagnostic.Fields["provider_error_code"])
	if failure == "" {
		failure = "provider session failure"
	}
	c.stop.fail(fmt.Errorf("%s: %s", name, failure))
}

func (c *conversationRuntime) failEvidence(name, kind string, err error) {
	failure := fmt.Errorf("%s %s evidence: %w", name, kind, err)
	c.evidence.fail(failure)
	c.stop.fail(failure)
}

func (c *conversationRuntime) observeStream(name string, side int, output *pcmBridge, msg messages.StreamMessage) {
	if err := c.evidence.observeStream(side, msg); err != nil {
		c.failEvidence(name, "stream delta", err)
		return
	}
	if msg.Type != messages.StreamTypeAudioDelta || !assistantAudioDelta(msg) {
		return
	}
	value, ok := msg.Value.(*messages.AudioDeltaValue)
	if !ok || value == nil {
		c.stop.fail(fmt.Errorf("%s emitted AUDIO.DELTA with unexpected value %T", name, msg.Value))
		return
	}
	if len(value.Content)%2 != 0 {
		err := fmt.Errorf("%s emitted odd PCM16 AUDIO.DELTA length %d", name, len(value.Content))
		c.evidence.fail(err)
		c.stop.fail(err)
		return
	}
	if err := c.evidence.observeAudio(side, c.ctx, value.Content); err != nil {
		err = fmt.Errorf("%s WAV evidence: %w", name, err)
		c.evidence.fail(err)
		c.stop.fail(err)
		return
	}
	if err := output.write(value.Content); err != nil && !isCancellation(err) && !c.stop.stopped() {
		c.stop.fail(fmt.Errorf("%s PCM bridge write: %w", name, err))
	}
}

func (c *conversationRuntime) await(timerCh <-chan time.Time) {
	stopCh := c.stop.done
	ctxDone := c.ctx.Done()
	remaining := 2
	for remaining > 0 {
		select {
		case <-timerCh:
			timerCh = nil
			c.stop.stop(selfplay.StopMaxDuration, nil)
		case <-stopCh:
			stopCh = nil
		case <-ctxDone:
			ctxDone = nil
			c.stop.fail(c.ctx.Err())
		case side := <-c.results:
			remaining--
			c.handleSideResult(side)
		}
	}
}

func (c *conversationRuntime) handleSideResult(side sideResult) {
	if c.stop.stopped() {
		return
	}
	if side.err != nil {
		c.stop.fail(fmt.Errorf("%s session: %w", side.name, side.err))
		return
	}
	c.stop.fail(fmt.Errorf("%s session ended before a self-play bound", side.name))
}

func (c *conversationRuntime) finish() (selfplay.Result, error) {
	c.customerToAssistant.close()
	c.assistantToCustomer.close()
	c.bridgeWG.Wait()
	result, runErr := c.stop.snapshot()
	if result.StopReason == "" {
		c.stop.fail(errors.New("self-play ended without a stop reason"))
		result, runErr = c.stop.snapshot()
	}
	if err := c.evidence.err(); err != nil {
		runErr = errors.Join(runErr, err)
	}
	runErr = errors.Join(runErr, closeSessionsSafely(c.closeSessions))
	finalizeErr := c.evidence.finalize(result, runErr, now(c.service.deps.Clock))
	return result, errors.Join(runErr, finalizeErr)
}

func (s *Service) runConversation(ctx context.Context, options selfplay.RunOptions, customer, assistant messages.SessionInferencer, evidence *evidence, closeSessions func() error) (selfplay.Result, error) {
	timer, err := newTimer(s.deps.Clock, options.MaxDuration)
	if err != nil {
		return failureResult(), errors.Join(fmt.Errorf("self-play clock: %w", err), closeSessionsSafely(closeSessions))
	}
	defer timer.Stop()
	conversation := newConversationRuntime(s, ctx, options, customer, assistant, evidence, closeSessions)
	defer conversation.bridgeCancel()
	conversation.startBridges()
	conversation.startSides()
	conversation.await(timer.C())
	return conversation.finish()
}

func closeSessionsSafely(closeSessions func() error) error {
	if closeSessions == nil {
		return nil
	}
	return closeSessions()
}

func assistantAudioDelta(msg messages.StreamMessage) bool {
	return msg.Role == "" || msg.Role == messages.RoleAssistant
}

func turnIndex(fields map[string]string) int {
	index, _ := strconv.Atoi(fields["turn_index"])
	return index
}
