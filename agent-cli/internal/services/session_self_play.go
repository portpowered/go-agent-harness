package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	// SelfPlayDefaultProvider is the only provider enabled by the Phase 1
	// self-play command. The provider field remains configurable so a future
	// phase can add another live session implementation without changing the
	// command contract.
	SelfPlayDefaultProvider = "openai"
	// SelfPlayDefaultModel keeps the Phase 1 smoke path on the explicitly
	// requested OpenAI Realtime model, independent of the general session
	// command's configured model default.
	SelfPlayDefaultModel = "gpt-realtime"
	// SelfPlayDefaultMaxDuration bounds an invocation that omits the flag.
	SelfPlayDefaultMaxDuration = 2 * time.Minute
	// SelfPlayDefaultTurnTarget gives the manual smoke path the three-turn
	// target required by the Phase 1 proof.
	SelfPlayDefaultTurnTarget = 3

	// SelfPlayCustomerPersona and SelfPlayAssistantPersona are intentionally
	// fixed in Phase 1. Keeping them in one service-owned place makes the
	// command help, live instructions, and later manifest evidence agree.
	SelfPlayCustomerPersona  = "You are the customer. Speak naturally, briefly, and only as part of a spoken conversation. Ask one practical follow-up at a time. Do not call tools."
	SelfPlayAssistantPersona = "You are the helpful assistant. Speak naturally, briefly, and only as part of a spoken conversation. Answer the customer's latest request and ask one concise follow-up when useful. Do not call tools."
	// SelfPlayOpeningSeed is sent as the sole initial user text turn to the
	// customer-side session. All later turns travel as raw PCM16 audio.
	SelfPlayOpeningSeed = "Hi, I need help planning a simple weekend trip."
)

// SelfPlayStopReason is the stable reason a bounded self-play run stopped.
type SelfPlayStopReason string

const (
	SelfPlayStopMaxDuration SelfPlayStopReason = "max_duration"
	SelfPlayStopTurnTarget  SelfPlayStopReason = "turn_target"
	SelfPlayStopFailure     SelfPlayStopReason = "failure"
)

// SelfPlaySessionInferencerFactory constructs one production or hermetic live
// session. The options are copied per side; the customer option carries the
// opening seed while the assistant option does not.
type SelfPlaySessionInferencerFactory func(SessionRunOptions, string) (messages.SessionInferencer, error)

// SelfPlayRunOptions is the bounded Phase 1 self-play command configuration.
// CustomerInferencer and AssistantInferencer are deterministic test seams. In
// production both are nil and the service composes the existing live session
// constructor twice.
type SelfPlayRunOptions struct {
	APIKey      string
	OutputDir   string
	Provider    string
	Model       string
	BaseURL     string
	ConfigDir   string
	MaxDuration time.Duration
	MaxTurns    int

	WebSocketDialer transport.Dialer
	SessionFactory  SelfPlaySessionInferencerFactory

	CustomerInferencer  messages.SessionInferencer
	AssistantInferencer messages.SessionInferencer
}

// SelfPlayOptions is a concise alias for callers that do not need the Run
// suffix used by the other session option type.
type SelfPlayOptions = SelfPlayRunOptions

// SelfPlayResult contains observable bounded-run facts useful to callers and
// to the evidence-producing story that builds on this runner.
type SelfPlayResult struct {
	StopReason     SelfPlayStopReason
	CustomerTurns  int
	AssistantTurns int
}

// RunSessionSelfPlay is the descriptive alias for RunSelfPlay.
func RunSessionSelfPlay(ctx context.Context, out io.Writer, opts SelfPlayRunOptions) error {
	_, err := RunSelfPlayWithResult(ctx, out, opts)
	return err
}

// RunSelfPlay validates the complete bounded self-play request before creating
// either session, then runs two continuously open duplex sessions connected by
// raw PCM16 bridges.
func RunSelfPlay(ctx context.Context, out io.Writer, opts SelfPlayRunOptions) error {
	_, err := RunSelfPlayWithResult(ctx, out, opts)
	return err
}

// RunSelfPlayWithResult runs a bounded self-play conversation and returns the
// side turn counts even when the run stops because one side fails. A bounded
// max-duration or turn-target stop is a clean result; uncontrolled session and
// bridge failures are returned.
func RunSelfPlayWithResult(ctx context.Context, out io.Writer, opts SelfPlayRunOptions) (SelfPlayResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if out == nil {
		out = io.Discard
	}

	normalized, err := normalizeSelfPlayRunOptions(opts)
	if err != nil {
		return SelfPlayResult{}, err
	}

	base := selfPlaySessionRunOptions(normalized)
	factory := normalized.SessionFactory
	if factory == nil {
		factory = defaultSelfPlaySessionFactory
	}

	customerInferencer := normalized.CustomerInferencer
	assistantInferencer := normalized.AssistantInferencer
	if (customerInferencer == nil) != (assistantInferencer == nil) {
		return SelfPlayResult{}, errors.New("self-play requires both customer and assistant session inferencers when using injected sessions")
	}
	if customerInferencer == nil {
		// Construction is deliberately after all validation. The production
		// factory creates providers but does not dial; the first network call is
		// still made only by the concurrently started session loops below.
		customerOptions := base
		customerOptions.Prompt = SelfPlayOpeningSeed
		customerInferencer, err = factory(customerOptions, SelfPlayCustomerPersona)
		if err != nil {
			return SelfPlayResult{}, fmt.Errorf("construct customer live session: %w", err)
		}
		assistantOptions := base
		assistantInferencer, err = factory(assistantOptions, SelfPlayAssistantPersona)
		if err != nil {
			return SelfPlayResult{}, fmt.Errorf("construct assistant live session: %w", err)
		}
	}
	if customerInferencer == nil || assistantInferencer == nil {
		return SelfPlayResult{}, errors.New("self-play session factory returned a nil inferencer")
	}

	if err := os.MkdirAll(normalized.OutputDir, 0o700); err != nil {
		return SelfPlayResult{}, fmt.Errorf("create self-play output directory %q: %w", normalized.OutputDir, err)
	}

	result, runErr := runSelfPlayConversation(ctx, normalized, customerInferencer, assistantInferencer)
	if _, writeErr := fmt.Fprintf(out, "self-play stopped: reason=%s customer_turns=%d assistant_turns=%d\n", result.StopReason, result.CustomerTurns, result.AssistantTurns); writeErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("write self-play result: %w", writeErr))
	}
	return result, runErr
}

func normalizeSelfPlayRunOptions(opts SelfPlayRunOptions) (SelfPlayRunOptions, error) {
	opts.Provider = strings.ToLower(strings.TrimSpace(opts.Provider))
	if opts.Provider == "" {
		opts.Provider = SelfPlayDefaultProvider
	}
	if opts.Provider != SelfPlayDefaultProvider {
		return SelfPlayRunOptions{}, fmt.Errorf("self-play Phase 1 supports provider %q only; got %q", SelfPlayDefaultProvider, opts.Provider)
	}

	opts.Model = strings.TrimSpace(opts.Model)
	if opts.Model == "" {
		opts.Model = SelfPlayDefaultModel
	}
	if _, ok := LookupOpenAIRealtimeModel(opts.Model); !ok {
		return SelfPlayRunOptions{}, fmt.Errorf("self-play model %q is not an OpenAI Realtime model; supported models: %s", opts.Model, strings.Join(SupportedOpenAIRealtimeModelIDs(), ", "))
	}
	if opts.MaxDuration <= 0 {
		return SelfPlayRunOptions{}, fmt.Errorf("self-play --max-duration must be positive, got %s", opts.MaxDuration)
	}
	if opts.MaxTurns <= 0 {
		return SelfPlayRunOptions{}, fmt.Errorf("self-play --max-turns must be positive, got %d", opts.MaxTurns)
	}
	opts.OutputDir = strings.TrimSpace(opts.OutputDir)
	if opts.OutputDir == "" {
		return SelfPlayRunOptions{}, errors.New("self-play --output-dir is required")
	}
	opts.OutputDir = filepath.Clean(opts.OutputDir)
	if err := validateSelfPlayOutputTarget(opts.OutputDir); err != nil {
		return SelfPlayRunOptions{}, err
	}

	// An injected pair is the credential-free hermetic seam. The command and
	// the default production factory always take this validation path.
	if opts.CustomerInferencer == nil && opts.AssistantInferencer == nil && opts.SessionFactory == nil {
		if _, err := resolveOpenAIRealtimeSessionConfig(selfPlaySessionRunOptions(opts)); err != nil {
			return SelfPlayRunOptions{}, fmt.Errorf("self-play live session configuration: %w", err)
		}
	}
	return opts, nil
}

func validateSelfPlayOutputTarget(path string) error {
	destination := filepath.Clean(path)
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

func selfPlaySessionRunOptions(opts SelfPlayRunOptions) SessionRunOptions {
	return SessionRunOptions{
		Provider:        opts.Provider,
		Model:           opts.Model,
		ModelProvided:   true,
		APIKey:          opts.APIKey,
		BaseURL:         opts.BaseURL,
		ConfigDir:       opts.ConfigDir,
		WebSocketDialer: opts.WebSocketDialer,
		// Phase 1 is intentionally no-tools. These fields stay nil even when
		// callers provide a composed CLI executor elsewhere in the process.
		ToolExecutor:    nil,
		ToolDefinitions: nil,
	}
}

func defaultSelfPlaySessionFactory(opts SessionRunOptions, instructions string) (messages.SessionInferencer, error) {
	inferencer, _, err := NewLiveSessionInferencer(opts, instructions)
	return inferencer, err
}

type selfPlaySideResult struct {
	name string
	err  error
}

type selfPlayStopState struct {
	done         chan struct{}
	bridgeCancel context.CancelFunc
	once         sync.Once
	mu           sync.Mutex
	reason       SelfPlayStopReason
	err          error
	turns        [2]int
}

func newSelfPlayStopState(bridgeCancel context.CancelFunc) *selfPlayStopState {
	return &selfPlayStopState{done: make(chan struct{}), bridgeCancel: bridgeCancel}
}

func (s *selfPlayStopState) stop(reason SelfPlayStopReason, err error) {
	if reason == "" {
		reason = SelfPlayStopFailure
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reason != "" {
		return
	}
	s.reason = reason
	s.err = err
	s.publishLocked()
}

// publishLocked closes the shared stop boundary after the terminal reason,
// error, and accepted turn counts have been committed. Callers must hold mu.
func (s *selfPlayStopState) publishLocked() {
	s.once.Do(func() {
		close(s.done)
		if s.bridgeCancel != nil {
			s.bridgeCancel()
		}
	})
}

func (s *selfPlayStopState) fail(err error) {
	if err == nil {
		err = errors.New("self-play stopped because an agent failed")
	}
	s.stop(SelfPlayStopFailure, err)
}

func (s *selfPlayStopState) doneErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *selfPlayStopState) stopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reason != ""
}

// recordTurn admits one completed turn at the same synchronized boundary as
// terminal publication. A false result means the event was already terminal
// or that side had reached its bound, so no downstream completed-turn
// observation should be emitted for it.
func (s *selfPlayStopState) recordTurn(side int, target int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if side < 0 || side >= len(s.turns) || target <= 0 {
		return false
	}
	if s.reason != "" {
		return false
	}
	if s.turns[side] >= target {
		return false
	}
	s.turns[side]++
	if s.turns[0] == target && s.turns[1] == target {
		s.reason = SelfPlayStopTurnTarget
		s.err = nil
		s.publishLocked()
	}
	return true
}

func (s *selfPlayStopState) result() SelfPlayResult {
	result, _ := s.snapshot()
	return result
}

func (s *selfPlayStopState) snapshot() (SelfPlayResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SelfPlayResult{
		StopReason:     s.reason,
		CustomerTurns:  s.turns[0],
		AssistantTurns: s.turns[1],
	}, s.err
}

type selfPlayPCMBridge struct {
	reader *io.PipeReader
	writer *io.PipeWriter
	once   sync.Once
	mu     sync.Mutex
	closed bool
}

func newSelfPlayPCMBridge(ctx context.Context) *selfPlayPCMBridge {
	reader, writer := io.Pipe()
	bridge := &selfPlayPCMBridge{reader: reader, writer: writer}
	go func() {
		<-ctx.Done()
		bridge.close()
	}()
	return bridge
}

func (b *selfPlayPCMBridge) write(pcm []byte) error {
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

func (b *selfPlayPCMBridge) pumpWithObserver(ctx context.Context, loopReady <-chan *agentloop.AgentLoop, fail func(error), name string, observeInput func([]byte)) {
	var loop *agentloop.AgentLoop
	select {
	case loop = <-loopReady:
	case <-ctx.Done():
		return
	}
	if loop == nil {
		return
	}

	buffer := make([]byte, 64*1024)
	for {
		count, err := b.reader.Read(buffer)
		if count > 0 {
			pcm := append([]byte(nil), buffer[:count]...)
			if sendErr := loop.SendAudioInput(ctx, pcm); sendErr != nil {
				if !isSessionCancellation(sendErr) {
					fail(fmt.Errorf("%s PCM bridge send: %w", name, sendErr))
				}
				return
			}
			if observeInput != nil {
				observeInput(pcm)
			}
		}
		if err != nil {
			b.mu.Lock()
			closed := b.closed
			b.mu.Unlock()
			if !closed && !isSessionCancellation(err) {
				fail(fmt.Errorf("%s PCM bridge read: %w", name, err))
			}
			return
		}
	}
}

func (b *selfPlayPCMBridge) close() {
	b.once.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
		_ = b.writer.Close()
		_ = b.reader.Close()
	})
}

func runSelfPlayConversation(ctx context.Context, opts SelfPlayRunOptions, customer messages.SessionInferencer, assistant messages.SessionInferencer) (SelfPlayResult, error) {
	evidence, err := newSelfPlayEvidence(opts.OutputDir, opts, time.Now().UTC())
	if err != nil {
		return SelfPlayResult{StopReason: SelfPlayStopFailure}, err
	}

	bridgeCtx, bridgeCancel := context.WithCancel(ctx)
	defer bridgeCancel()
	stop := newSelfPlayStopState(bridgeCancel)

	customerReady := make(chan *agentloop.AgentLoop, 1)
	assistantReady := make(chan *agentloop.AgentLoop, 1)
	customerToAssistant := newSelfPlayPCMBridge(bridgeCtx)
	assistantToCustomer := newSelfPlayPCMBridge(bridgeCtx)

	var bridgeWG sync.WaitGroup
	bridgeWG.Add(2)
	go func() {
		defer bridgeWG.Done()
		customerToAssistant.pumpWithObserver(bridgeCtx, assistantReady, stop.fail, "customer-to-assistant", func(pcm []byte) {
			if side := evidence.side(1); side != nil && side.runtimeRecord != nil {
				side.runtimeRecord.audioInput(pcm)
			}
		})
	}()
	go func() {
		defer bridgeWG.Done()
		assistantToCustomer.pumpWithObserver(bridgeCtx, customerReady, stop.fail, "assistant-to-customer", func(pcm []byte) {
			if side := evidence.side(0); side != nil && side.runtimeRecord != nil {
				side.runtimeRecord.audioInput(pcm)
			}
		})
	}()

	results := make(chan selfPlaySideResult, 2)
	runSide := func(name string, side int, inferencer messages.SessionInferencer, prompt string, output *selfPlayPCMBridge, ready chan<- *agentloop.AgentLoop) {
		sideEvidence := evidence.side(side)
		sideEvidence.diagnosticErr = func(err error) {
			wrapped := fmt.Errorf("%s diagnostic evidence: %w", name, err)
			evidence.fail(wrapped)
			stop.fail(wrapped)
		}
		observer := newSessionProgressObserver(sideEvidence, nil, opts.Provider, opts.Model)
		observer.runtime = sideEvidence.runtimeRecord
		observer.turnAdmission = func(messages.StreamMessage) bool {
			return stop.recordTurn(side, opts.MaxTurns)
		}
		observer.streamObserver = func(msg messages.StreamMessage) {
			if err := sideEvidence.observeStreamDelta(msg); err != nil {
				evidence.fail(fmt.Errorf("%s stream delta evidence: %w", name, err))
				stop.fail(fmt.Errorf("%s stream delta evidence: %w", name, err))
			}
			if msg.Type == messages.StreamTypeAudioDelta && assistantAudioDelta(msg) {
				value, ok := msg.Value.(*messages.AudioDeltaValue)
				if !ok {
					stop.fail(fmt.Errorf("%s emitted AUDIO.DELTA with unexpected value %T", name, msg.Value))
					return
				}
				if err := output.write(value.Content); err != nil && !isSessionCancellation(err) && !stop.stopped() {
					stop.fail(fmt.Errorf("%s PCM bridge write: %w", name, err))
				}
				if err := sideEvidence.observeAudio(value.Content); err != nil {
					evidence.fail(fmt.Errorf("%s WAV evidence: %w", name, err))
					stop.fail(fmt.Errorf("%s WAV evidence: %w", name, err))
				}
				if sideEvidence.runtimeRecord != nil {
					sideEvidence.runtimeRecord.audioOutput(value.Content)
				}
			}
		}

		err := runAgentLoopSession(ctx, io.Discard, inferencer, sessionLoopOptions{
			Prompt:       prompt,
			WaitForClose: true,
			Done:         stop.done,
			DoneErr:      stop.doneErr,
			observer:     observer,
			runtime:      sideEvidence.runtimeRecord,
			loopReady:    ready,
		})
		results <- selfPlaySideResult{name: name, err: err}
	}

	go runSide("customer", 0, customer, SelfPlayOpeningSeed, customerToAssistant, customerReady)
	go runSide("assistant", 1, assistant, "", assistantToCustomer, assistantReady)

	timer := time.NewTimer(opts.MaxDuration)
	defer timer.Stop()
	var timerCh <-chan time.Time = timer.C
	var doneCh <-chan struct{} = stop.done
	ctxDone := ctx.Done()
	remaining := 2
	for remaining > 0 {
		select {
		case <-timerCh:
			timerCh = nil
			stop.stop(SelfPlayStopMaxDuration, nil)
		case <-doneCh:
			doneCh = nil
		case <-ctxDone:
			ctxDone = nil
			stop.fail(ctx.Err())
		case result := <-results:
			remaining--
			if result.err != nil && !stop.stopped() {
				stop.fail(fmt.Errorf("%s session: %w", result.name, result.err))
			} else if result.err == nil && !stop.stopped() {
				stop.fail(fmt.Errorf("%s session ended before a self-play bound", result.name))
			}
		}
	}

	// Both session loops have returned at this point. Closing the pipes makes
	// the cleanup ordering explicit and lets the pump goroutines leave even if
	// they were waiting for a final Read after the loop stopped.
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
	finalizeErr := evidence.finalize(result, runErr, time.Now().UTC())
	return result, errors.Join(runErr, finalizeErr)
}
