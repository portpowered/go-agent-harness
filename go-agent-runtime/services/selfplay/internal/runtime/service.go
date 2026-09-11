package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// Service owns self-play policy and lifecycle. It does not know which host
// supplies the two provider sessions or how a host executes one session.
type Service struct {
	deps selfplay.Dependencies
}

func New(deps selfplay.Dependencies) *Service { return &Service{deps: deps} }

func (s *Service) Run(ctx context.Context, out io.Writer, options selfplay.RunOptions) error {
	_, err := s.RunWithResult(ctx, out, options)
	return err
}

func (s *Service) RunWithResult(ctx context.Context, out io.Writer, options selfplay.RunOptions) (selfplay.Result, error) {
	if s == nil {
		return failureResult(), errors.New("self-play service is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if out == nil {
		out = io.Discard
	}
	normalized, err := normalize(options, s.deps)
	if err != nil {
		return failureResult(), err
	}
	if s.deps.Sessions == nil {
		return failureResult(), errors.New("self-play session factory is required")
	}
	if s.deps.Runner == nil {
		return failureResult(), errors.New("self-play session runner is required")
	}

	base := selfplay.SessionRequest{
		APIKey: normalized.APIKey, Provider: normalized.Provider, Model: normalized.Model,
		BaseURL: normalized.BaseURL, ConfigDir: normalized.ConfigDir, Clock: s.deps.Clock,
	}
	customerRequest := base
	customerRequest.Prompt = selfplay.SelfPlayOpeningSeed
	customerRequest.Persona = selfplay.SelfPlayCustomerPersona
	customer, err := s.deps.Sessions.NewSession(ctx, customerRequest)
	if err != nil {
		return failureResult(), closeAfterConstruction(customer, fmt.Errorf("construct customer live session: %w", err))
	}
	if customer == nil {
		return failureResult(), errors.New("self-play session factory returned a nil customer inferencer")
	}
	assistantRequest := base
	assistantRequest.Persona = selfplay.SelfPlayAssistantPersona
	assistant, err := s.deps.Sessions.NewSession(ctx, assistantRequest)
	if err != nil {
		return failureResult(), closeAfterConstructionPair(customer, assistant, fmt.Errorf("construct assistant live session: %w", err))
	}
	if assistant == nil {
		return failureResult(), closeAfterConstruction(customer, errors.New("self-play session factory returned a nil assistant inferencer"))
	}

	if err := os.MkdirAll(normalized.OutputDir, 0o700); err != nil {
		return failureResult(), closeAfterConstructionPair(customer, assistant, fmt.Errorf("create self-play output directory %q: %w", normalized.OutputDir, err))
	}
	startedAt := now(s.deps.Clock)
	evidence, err := newEvidence(normalized.OutputDir, normalized, startedAt)
	if err != nil {
		return failureResult(), closeAfterConstructionPair(customer, assistant, err)
	}
	result, runErr := s.runConversation(ctx, normalized, customer, assistant, evidence, func() error {
		return errors.Join(closeSession(customer, "customer"), closeSession(assistant, "assistant"))
	})
	if _, writeErr := fmt.Fprintf(out, "self-play stopped: reason=%s customer_turns=%d assistant_turns=%d\n", result.StopReason, result.CustomerTurns, result.AssistantTurns); writeErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("write self-play result: %w", writeErr))
	}
	return result, runErr
}

func failureResult() selfplay.Result { return selfplay.Result{StopReason: selfplay.StopFailure} }

func normalize(options selfplay.RunOptions, deps selfplay.Dependencies) (selfplay.RunOptions, error) {
	options.Provider = strings.ToLower(strings.TrimSpace(options.Provider))
	if options.Provider == "" {
		options.Provider = selfplay.SelfPlayDefaultProvider
	}
	if options.Provider != selfplay.SelfPlayDefaultProvider {
		return selfplay.RunOptions{}, fmt.Errorf("self-play supports provider %q only; got %q", selfplay.SelfPlayDefaultProvider, options.Provider)
	}
	options.Model = strings.TrimSpace(options.Model)
	if options.Model == "" {
		options.Model = selfplay.SelfPlayDefaultModel
	}
	if deps.ModelAdmission == nil {
		return selfplay.RunOptions{}, errors.New("self-play model admission is required")
	}
	if err := deps.ModelAdmission.ValidateSessionModel(options.Provider, options.Model); err != nil {
		return selfplay.RunOptions{}, fmt.Errorf("self-play model admission: %w", err)
	}
	if options.MaxDuration <= 0 {
		return selfplay.RunOptions{}, fmt.Errorf("self-play max duration must be positive, got %s", options.MaxDuration)
	}
	if options.MaxTurns <= 0 {
		return selfplay.RunOptions{}, fmt.Errorf("self-play max turns must be positive, got %d", options.MaxTurns)
	}
	options.OutputDir = strings.TrimSpace(options.OutputDir)
	if options.OutputDir == "" {
		return selfplay.RunOptions{}, errors.New("self-play output directory is required")
	}
	options.OutputDir = filepath.Clean(options.OutputDir)
	if err := validateOutputTarget(options.OutputDir); err != nil {
		return selfplay.RunOptions{}, err
	}
	if _, err := clock.RequireTimerSource(deps.Clock); err != nil {
		return selfplay.RunOptions{}, fmt.Errorf("self-play clock: %w", err)
	}
	return options, nil
}

func validateOutputTarget(destination string) error {
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("prepare self-play output parent %q: %w", destination, err)
	}
	info, err := os.Lstat(destination)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("self-play output target %q must be a non-symlink directory", destination)
		}
		entries, readErr := os.ReadDir(destination)
		if readErr != nil {
			return fmt.Errorf("inspect self-play output directory %q: %w", destination, readErr)
		}
		if len(entries) != 0 {
			return fmt.Errorf("self-play output directory %q is not safe: it must be empty", destination)
		}
		parent = destination
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect self-play output target %q: %w", destination, err)
	}
	probe, err := os.CreateTemp(parent, ".self-play-probe-")
	if err != nil {
		return fmt.Errorf("probe self-play output target %q: %w", destination, err)
	}
	probePath := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(probePath)
	if closeErr != nil {
		return fmt.Errorf("close self-play output probe %q: %w", destination, closeErr)
	}
	if removeErr != nil {
		return fmt.Errorf("remove self-play output probe %q: %w", destination, removeErr)
	}
	return nil
}

func now(source clock.Source) time.Time { return source.Now().UTC() }

func newTimer(source clock.Source, duration time.Duration) (clock.Timer, error) {
	timerSource, err := clock.RequireTimerSource(source)
	if err != nil {
		return nil, err
	}
	timer := timerSource.NewTimer(duration)
	if timer == nil {
		return nil, errors.New("self-play clock returned a nil timer")
	}
	return timer, nil
}

func closeAfterConstruction(session messages.SessionInferencer, err error) error {
	if closer, ok := session.(interface{ Close() error }); ok {
		return errors.Join(err, closer.Close())
	}
	return err
}

func closeAfterConstructionPair(customer, assistant messages.SessionInferencer, err error) error {
	err = closeAfterConstruction(customer, err)
	return closeAfterConstruction(assistant, err)
}

func closeSession(session messages.SessionInferencer, name string) error {
	closer, ok := session.(interface{ Close() error })
	if !ok {
		return nil
	}
	if err := closer.Close(); err != nil {
		return fmt.Errorf("close %s live session: %w", name, err)
	}
	return nil
}

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
	var input selfplay.AudioInput
	select {
	case input = <-ready:
	case <-ctx.Done():
		return
	}
	if input == nil {
		return
	}
	buffer := make([]byte, 64*1024)
	for {
		count, err := b.reader.Read(buffer)
		if count > 0 {
			pcm := append([]byte(nil), buffer[:count]...)
			if sendErr := input.SendAudioInput(ctx, pcm); sendErr != nil {
				if !isCancellation(sendErr) {
					fail(fmt.Errorf("%s PCM bridge send: %w", name, sendErr))
				}
				return
			}
			if observe != nil {
				observe(pcm)
			}
		}
		if err != nil {
			b.mu.Lock()
			closed := b.closed
			b.mu.Unlock()
			if !closed && !isCancellation(err) {
				fail(fmt.Errorf("%s PCM bridge read: %w", name, err))
			}
			return
		}
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

func (s *Service) runConversation(ctx context.Context, options selfplay.RunOptions, customer, assistant messages.SessionInferencer, evidence *evidence, closeSessions func() error) (selfplay.Result, error) {
	close := func() error {
		if closeSessions == nil {
			return nil
		}
		return closeSessions()
	}
	timer, err := newTimer(s.deps.Clock, options.MaxDuration)
	if err != nil {
		return failureResult(), errors.Join(fmt.Errorf("self-play clock: %w", err), close())
	}
	defer timer.Stop()

	bridgeCtx, bridgeCancel := context.WithCancel(context.Background())
	defer bridgeCancel()
	stop := newStopState(bridgeCancel)
	customerReady := make(chan selfplay.AudioInput, 1)
	assistantReady := make(chan selfplay.AudioInput, 1)
	customerToAssistant := newPCMBridge(bridgeCtx)
	assistantToCustomer := newPCMBridge(bridgeCtx)

	var bridgeWG sync.WaitGroup
	bridgeWG.Add(2)
	go func() {
		defer bridgeWG.Done()
		customerToAssistant.pump(bridgeCtx, assistantReady, func(err error) { stop.fail(err) }, "customer-to-assistant", func(pcm []byte) { evidence.observeInput(1, pcm) })
	}()
	go func() {
		defer bridgeWG.Done()
		assistantToCustomer.pump(bridgeCtx, customerReady, func(err error) { stop.fail(err) }, "assistant-to-customer", func(pcm []byte) { evidence.observeInput(0, pcm) })
	}()

	results := make(chan sideResult, 2)
	runSide := func(name string, side int, session messages.SessionInferencer, prompt string, output *pcmBridge, ready chan<- selfplay.AudioInput) {
		err := s.deps.Runner.Run(ctx, session, selfplay.SessionRunOptions{
			Prompt: prompt, Provider: options.Provider, Model: options.Model, Clock: s.deps.Clock,
			Done: stop.done, DoneErr: stop.doneErr, Ready: ready,
			AdmitTurn: func(messages.StreamMessage) bool { return stop.recordTurn(side, options.MaxTurns) },
			ObserveDiagnostic: func(diagnostic selfplay.Diagnostic) {
				if recordErr := evidence.observeDiagnostic(side, diagnostic); recordErr != nil {
					evidence.fail(fmt.Errorf("%s diagnostic evidence: %w", name, recordErr))
					stop.fail(fmt.Errorf("%s diagnostic evidence: %w", name, recordErr))
					return
				}
				if diagnostic.Event == "session_failure" {
					failure := strings.TrimSpace(diagnostic.Fields["provider_error_code"])
					if failure == "" {
						failure = "provider session failure"
					}
					stop.fail(fmt.Errorf("%s: %s", name, failure))
				}
			},
			ObserveStream: func(msg messages.StreamMessage) {
				if streamErr := evidence.observeStream(side, msg); streamErr != nil {
					evidence.fail(fmt.Errorf("%s stream delta evidence: %w", name, streamErr))
					stop.fail(fmt.Errorf("%s stream delta evidence: %w", name, streamErr))
					return
				}
				if msg.Type != messages.StreamTypeAudioDelta || !assistantAudioDelta(msg) {
					return
				}
				value, ok := msg.Value.(*messages.AudioDeltaValue)
				if !ok || value == nil {
					stop.fail(fmt.Errorf("%s emitted AUDIO.DELTA with unexpected value %T", name, msg.Value))
					return
				}
				if len(value.Content)%2 != 0 {
					err := fmt.Errorf("%s emitted odd PCM16 AUDIO.DELTA length %d", name, len(value.Content))
					evidence.fail(err)
					stop.fail(err)
					return
				}
				if audioErr := evidence.observeAudio(side, ctx, value.Content); audioErr != nil {
					err := fmt.Errorf("%s WAV evidence: %w", name, audioErr)
					evidence.fail(err)
					stop.fail(err)
					return
				}
				if bridgeErr := output.write(value.Content); bridgeErr != nil && !isCancellation(bridgeErr) && !stop.stopped() {
					stop.fail(fmt.Errorf("%s PCM bridge write: %w", name, bridgeErr))
				}
			},
		})
		results <- sideResult{name: name, err: err}
	}

	go runSide("customer", 0, customer, selfplay.SelfPlayOpeningSeed, customerToAssistant, customerReady)
	go runSide("assistant", 1, assistant, "", assistantToCustomer, assistantReady)

	timerCh := timer.C()
	var stopCh <-chan struct{} = stop.done
	ctxDone := ctx.Done()
	remaining := 2
	for remaining > 0 {
		select {
		case <-timerCh:
			timerCh = nil
			stop.stop(selfplay.StopMaxDuration, nil)
		case <-stopCh:
			stopCh = nil
		case <-ctxDone:
			ctxDone = nil
			stop.fail(ctx.Err())
		case side := <-results:
			remaining--
			if side.err != nil && !stop.stopped() {
				stop.fail(fmt.Errorf("%s session: %w", side.name, side.err))
			} else if side.err == nil && !stop.stopped() {
				stop.fail(fmt.Errorf("%s session ended before a self-play bound", side.name))
			}
		}
	}

	customerToAssistant.close()
	assistantToCustomer.close()
	bridgeWG.Wait()
	result, runErr := stop.snapshot()
	if result.StopReason == "" {
		stop.fail(errors.New("self-play ended without a stop reason"))
		result, runErr = stop.snapshot()
	}
	if evidenceErr := evidence.err(); evidenceErr != nil {
		runErr = errors.Join(runErr, evidenceErr)
	}
	runErr = errors.Join(runErr, close())
	finalizeErr := evidence.finalize(result, runErr, now(s.deps.Clock))
	return result, errors.Join(runErr, finalizeErr)
}

func assistantAudioDelta(msg messages.StreamMessage) bool {
	return msg.Role == "" || msg.Role == messages.RoleAssistant
}

func turnIndex(fields map[string]string) int {
	index, _ := strconv.Atoi(fields["turn_index"])
	return index
}
