package services

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestRunSelfPlay_BidirectionalPCMAndTextIsolation(t *testing.T) {
	pair := newSelfPlayEchoPair()
	var calls []struct {
		options      SessionRunOptions
		instructions string
	}
	var callsMu sync.Mutex
	factory := func(options SessionRunOptions, instructions string) (messages.SessionInferencer, error) {
		callsMu.Lock()
		callIndex := len(calls)
		calls = append(calls, struct {
			options      SessionRunOptions
			instructions string
		}{options: options, instructions: instructions})
		callsMu.Unlock()
		if callIndex == 0 {
			return pair.customer, nil
		}
		return pair.assistant, nil
	}

	result, err := RunSelfPlayWithResult(context.Background(), io.Discard, SelfPlayRunOptions{
		OutputDir:      t.TempDir() + "/run",
		MaxDuration:    time.Second,
		MaxTurns:       2,
		SessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("RunSelfPlayWithResult: %v", err)
	}
	if result.StopReason != SelfPlayStopTurnTarget {
		t.Fatalf("stop reason = %q, want %q", result.StopReason, SelfPlayStopTurnTarget)
	}
	if result.CustomerTurns != 2 {
		t.Fatalf("customer turns = %d, want 2", result.CustomerTurns)
	}
	if result.AssistantTurns != 2 {
		t.Fatalf("assistant turns = %d, want 2", result.AssistantTurns)
	}

	callsMu.Lock()
	gotCalls := append([]struct {
		options      SessionRunOptions
		instructions string
	}{}, calls...)
	callsMu.Unlock()
	if len(gotCalls) != 2 {
		t.Fatalf("factory calls = %d, want 2", len(gotCalls))
	}
	if gotCalls[0].options.Prompt != SelfPlayOpeningSeed || gotCalls[1].options.Prompt != "" {
		t.Fatalf("factory prompts = %q/%q, want opening seed/empty", gotCalls[0].options.Prompt, gotCalls[1].options.Prompt)
	}
	if gotCalls[0].options.ToolExecutor != nil || gotCalls[1].options.ToolExecutor != nil || len(gotCalls[0].options.ToolDefinitions) != 0 || len(gotCalls[1].options.ToolDefinitions) != 0 {
		t.Fatal("self-play factory received a tool executor or tool definitions")
	}
	if gotCalls[0].instructions != SelfPlayCustomerPersona || gotCalls[1].instructions != SelfPlayAssistantPersona {
		t.Fatalf("factory instructions do not match fixed personas: %q / %q", gotCalls[0].instructions, gotCalls[1].instructions)
	}

	customerSent := pair.customerSession.sentMessages()
	assistantSent := pair.assistantSession.sentMessages()
	if !containsAudio(customerSent, selfPlayAssistantAudio) {
		t.Fatalf("customer did not receive assistant PCM through its session: %+v", customerSent)
	}
	if !containsAudio(assistantSent, selfPlayCustomerAudio) {
		t.Fatalf("assistant did not receive customer PCM through its session: %+v", assistantSent)
	}
	if countMessageType(assistantSent, messages.StreamTypeTextDelta) != 0 {
		t.Fatalf("assistant received bridged text: %+v", assistantSent)
	}
	if countMessageType(customerSent, messages.StreamTypeTextDelta) != 1 {
		t.Fatalf("customer opening seed count = %d, want 1: %+v", countMessageType(customerSent, messages.StreamTypeTextDelta), customerSent)
	}
}

func TestSelfPlayStopState_ConcurrentFinalTurnsHaveExactTarget(t *testing.T) {
	const target = 2
	published := make(chan struct{}, 1)
	stop := newSelfPlayStopState(func() { published <- struct{}{} })
	if !stop.recordTurn(0, target) || !stop.recordTurn(1, target) {
		t.Fatal("first turn pair was not admitted")
	}

	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	accepted := make(chan bool, 2)
	var wg sync.WaitGroup
	for side := 0; side < 2; side++ {
		wg.Add(1)
		go func(side int) {
			defer wg.Done()
			ready <- struct{}{}
			<-start
			accepted <- stop.recordTurn(side, target)
		}(side)
	}
	for range 2 {
		<-ready
	}
	close(start)
	wg.Wait()
	for range 2 {
		if !<-accepted {
			t.Fatal("final turn was rejected")
		}
	}

	result, err := stop.snapshot()
	if err != nil {
		t.Fatalf("stop error = %v, want nil", err)
	}
	if result.StopReason != SelfPlayStopTurnTarget || result.CustomerTurns != target || result.AssistantTurns != target {
		t.Fatalf("stop snapshot = %+v, want turn-target with exact counts", result)
	}
	select {
	case <-stop.done:
	default:
		t.Fatal("target transition did not publish the stop boundary")
	}
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("target transition did not cancel the bridge")
	}
	select {
	case <-published:
		t.Fatal("target transition published more than once")
	default:
	}
}

func TestSelfPlayStopState_RejectsExtraTurnAtBoundBeforePeerFinal(t *testing.T) {
	const target = 2
	published := make(chan struct{}, 1)
	stop := newSelfPlayStopState(func() { published <- struct{}{} })
	if !stop.recordTurn(0, target) {
		t.Fatal("first customer turn was not admitted")
	}
	if !stop.recordTurn(0, target) {
		t.Fatal("second customer turn was not admitted")
	}
	if !stop.recordTurn(1, target) {
		t.Fatal("assistant turn was not admitted")
	}

	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	type turnResult struct {
		side     int
		accepted bool
	}
	results := make(chan turnResult, 2)
	var wg sync.WaitGroup
	for _, side := range []int{0, 1} {
		wg.Add(1)
		go func(side int) {
			defer wg.Done()
			ready <- struct{}{}
			<-start
			results <- turnResult{side: side, accepted: stop.recordTurn(side, target)}
		}(side)
	}
	for range 2 {
		<-ready
	}
	close(start)
	wg.Wait()
	for range 2 {
		turn := <-results
		if turn.side == 0 && turn.accepted {
			t.Fatal("extra customer turn was admitted at the bound")
		}
		if turn.side == 1 && !turn.accepted {
			t.Fatal("peer final turn was rejected")
		}
	}

	result, err := stop.snapshot()
	if err != nil {
		t.Fatalf("stop error = %v, want nil", err)
	}
	if result.StopReason != SelfPlayStopTurnTarget || result.CustomerTurns != target || result.AssistantTurns != target {
		t.Fatalf("stop snapshot = %+v, want turn-target with exact counts", result)
	}
	if stop.recordTurn(0, target) {
		t.Fatal("post-target customer turn was admitted")
	}
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("target transition did not cancel the bridge")
	}
	select {
	case <-published:
		t.Fatal("target transition published more than once")
	default:
	}
}

func TestSessionProgressObserver_TurnAdmissionRejectsPostBoundCompletion(t *testing.T) {
	runtimeObserver := &recordingSessionRuntimeObserver{}
	observer := newSessionProgressObserver(nil, nil, SelfPlayDefaultProvider, SelfPlayDefaultModel)
	observer.runtime = newSessionRuntimeObservationRecorder(runtimeObserver, nil)
	admitted := 0
	observer.turnAdmission = func(messages.StreamMessage) bool {
		admitted++
		return admitted == 1
	}
	messageEnd := messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}
	observer.observe(messageEnd)
	observer.observe(messageEnd)

	if observer.turnsCompleted != 1 {
		t.Fatalf("observer completed turns = %d, want 1", observer.turnsCompleted)
	}
	if len(runtimeObserver.observations) != 1 || runtimeObserver.observations[0].Kind != SessionRuntimeObservationTurnCompleted || runtimeObserver.observations[0].TurnsCompleted != 1 {
		t.Fatalf("runtime observations = %#v, want one admitted turn", runtimeObserver.observations)
	}
}

func TestSelfPlayStopState_TerminalPrecedenceWithBarriers(t *testing.T) {
	sideErr := errors.New("assistant session failed")
	tests := []struct {
		name        string
		reason      SelfPlayStopReason
		terminalErr error
		targetFirst bool
	}{
		{name: "target before max duration", reason: SelfPlayStopMaxDuration, targetFirst: true},
		{name: "max duration before target", reason: SelfPlayStopMaxDuration},
		{name: "target before caller cancellation", reason: SelfPlayStopFailure, terminalErr: context.Canceled, targetFirst: true},
		{name: "caller cancellation before target", reason: SelfPlayStopFailure, terminalErr: context.Canceled},
		{name: "target before side failure", reason: SelfPlayStopFailure, terminalErr: sideErr, targetFirst: true},
		{name: "side failure before target", reason: SelfPlayStopFailure, terminalErr: sideErr},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const target = 2
			published := make(chan struct{}, 1)
			stop := newSelfPlayStopState(func() { published <- struct{}{} })
			if !stop.recordTurn(0, target) {
				t.Fatal("first customer turn was not admitted")
			}
			if !stop.recordTurn(0, target) {
				t.Fatal("second customer turn was not admitted")
			}
			if !stop.recordTurn(1, target-1) {
				t.Fatal("failed to establish the final-turn barrier")
			}

			start := make(chan struct{})
			targetCommitted := make(chan struct{})
			contenderCommitted := make(chan struct{})
			targetDone := make(chan bool, 1)
			contenderDone := make(chan bool, 1)
			if test.targetFirst {
				go func() {
					<-start
					targetDone <- stop.recordTurn(1, target)
					close(targetCommitted)
				}()
				go func() {
					<-targetCommitted
					contenderDone <- stop.stop(test.reason, test.terminalErr)
					close(contenderCommitted)
				}()
			} else {
				go func() {
					<-start
					contenderDone <- stop.stop(test.reason, test.terminalErr)
					close(contenderCommitted)
				}()
				go func() {
					<-contenderCommitted
					targetDone <- stop.recordTurn(1, target)
					close(targetCommitted)
				}()
			}
			close(start)

			acceptedTarget := <-targetDone
			acceptedContender := <-contenderDone
			if acceptedTarget != test.targetFirst {
				t.Fatalf("target admission = %t, targetFirst = %t", acceptedTarget, test.targetFirst)
			}
			if acceptedContender == test.targetFirst {
				t.Fatalf("contender admission = %t, targetFirst = %t", acceptedContender, test.targetFirst)
			}

			result, gotErr := stop.snapshot()
			if test.targetFirst {
				if result.StopReason != SelfPlayStopTurnTarget || result.CustomerTurns != target || result.AssistantTurns != target || gotErr != nil {
					t.Fatalf("target-first snapshot = %+v, error = %v", result, gotErr)
				}
			} else {
				if result.StopReason != test.reason || result.CustomerTurns != target || result.AssistantTurns != target-1 {
					t.Fatalf("contender-first snapshot = %+v, want reason %q and exact accepted counts", result, test.reason)
				}
				if test.terminalErr == nil {
					if gotErr != nil {
						t.Fatalf("contender-first error = %v, want nil", gotErr)
					}
				} else if !errors.Is(gotErr, test.terminalErr) {
					t.Fatalf("contender-first error = %v, want %v", gotErr, test.terminalErr)
				}
			}
			if doneErr := stop.doneErr(); (doneErr == nil) != (gotErr == nil) || (doneErr != nil && !errors.Is(doneErr, gotErr)) {
				t.Fatalf("done error = %v, snapshot error = %v", doneErr, gotErr)
			}

			for range 8 {
				repeated, repeatedErr := stop.snapshot()
				if repeated != result || (repeatedErr == nil) != (gotErr == nil) || (repeatedErr != nil && !errors.Is(repeatedErr, gotErr)) {
					t.Fatalf("terminal snapshot changed: first=%+v/%v later=%+v/%v", result, gotErr, repeated, repeatedErr)
				}
			}
			if stop.recordTurn(1, target) {
				t.Fatal("post-terminal completed turn was admitted")
			}
			select {
			case <-published:
			default:
				t.Fatal("terminal transition did not publish its stop notification")
			}
			select {
			case <-published:
				t.Fatal("terminal transition published more than once")
			default:
			}
		})
	}
}

func TestRunSelfPlay_MaxDurationStopsBothSides(t *testing.T) {
	customer := newSelfPlayBlockingInferencer()
	assistant := newSelfPlayBlockingInferencer()
	started := time.Now()
	result, err := RunSelfPlayWithResult(context.Background(), io.Discard, SelfPlayRunOptions{
		OutputDir:           t.TempDir() + "/run",
		MaxDuration:         20 * time.Millisecond,
		MaxTurns:            20,
		CustomerInferencer:  customer,
		AssistantInferencer: assistant,
	})
	if err != nil {
		t.Fatalf("duration-bounded self-play: %v", err)
	}
	if result.StopReason != SelfPlayStopMaxDuration {
		t.Fatalf("stop reason = %q, want %q", result.StopReason, SelfPlayStopMaxDuration)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("duration cleanup took %s", elapsed)
	}
	if !customer.closed() || !assistant.closed() {
		t.Fatal("duration stop did not close both provider sessions")
	}
}

func TestRunSelfPlay_CallerCancellationPreservesFailureAndShutsDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	customer := newSelfPlayBlockingInferencer()
	assistant := newSelfPlayBlockingInferencer()
	resultCh := make(chan struct {
		result SelfPlayResult
		err    error
	}, 1)
	go func() {
		result, err := RunSelfPlayWithResult(ctx, io.Discard, SelfPlayRunOptions{
			OutputDir:           t.TempDir() + "/run",
			MaxDuration:         time.Second,
			MaxTurns:            20,
			CustomerInferencer:  customer,
			AssistantInferencer: assistant,
		})
		resultCh <- struct {
			result SelfPlayResult
			err    error
		}{result: result, err: err}
	}()

	for _, connected := range []<-chan struct{}{customer.connected, assistant.connected} {
		select {
		case <-connected:
		case <-time.After(time.Second):
			t.Fatal("self-play side did not connect")
		}
	}
	cancel()

	var outcome struct {
		result SelfPlayResult
		err    error
	}
	select {
	case outcome = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("caller cancellation did not shut down self-play")
	}
	if outcome.err == nil || !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("caller cancellation error = %v, want context.Canceled", outcome.err)
	}
	if outcome.result.StopReason != SelfPlayStopFailure || outcome.result.CustomerTurns != 0 || outcome.result.AssistantTurns != 0 {
		t.Fatalf("caller cancellation result = %+v, want failure with exact accepted counts", outcome.result)
	}
	if !customer.closed() || !assistant.closed() {
		t.Fatal("caller cancellation did not close both provider sessions")
	}
}

func TestRunSelfPlay_SideFailureCancelsPeerAndReturnsError(t *testing.T) {
	wantErr := errors.New("customer dial failed")
	customer := &selfPlayConnectFailInferencer{err: wantErr}
	assistant := newSelfPlayBlockingInferencer()

	result, err := RunSelfPlayWithResult(context.Background(), io.Discard, SelfPlayRunOptions{
		OutputDir:           t.TempDir() + "/run",
		MaxDuration:         time.Second,
		MaxTurns:            20,
		CustomerInferencer:  customer,
		AssistantInferencer: assistant,
	})
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("failure error = %v, want %v", err, wantErr)
	}
	if result.StopReason != SelfPlayStopFailure {
		t.Fatalf("stop reason = %q, want %q", result.StopReason, SelfPlayStopFailure)
	}
	if !assistant.closed() {
		t.Fatal("customer failure did not close the assistant session")
	}
}

func TestRunSelfPlay_RejectsInvalidOptionsBeforeFactory(t *testing.T) {
	factoryCalled := false
	factory := func(SessionRunOptions, string) (messages.SessionInferencer, error) {
		factoryCalled = true
		return nil, nil
	}

	_, err := RunSelfPlayWithResult(context.Background(), io.Discard, SelfPlayRunOptions{
		OutputDir:      t.TempDir() + "/run",
		Provider:       "grok",
		MaxDuration:    time.Second,
		MaxTurns:       3,
		SessionFactory: factory,
	})
	if err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("invalid provider error = %v", err)
	}
	if factoryCalled {
		t.Fatal("invalid options called the session factory")
	}

	factoryCalled = false
	_, err = RunSelfPlayWithResult(context.Background(), io.Discard, SelfPlayRunOptions{
		OutputDir:      t.TempDir() + "/run",
		Provider:       SelfPlayDefaultProvider,
		MaxDuration:    0,
		MaxTurns:       3,
		SessionFactory: factory,
	})
	if err == nil || !strings.Contains(err.Error(), "max-duration") {
		t.Fatalf("invalid duration error = %v", err)
	}
	if factoryCalled {
		t.Fatal("invalid duration called the session factory")
	}
}

func TestRunSelfPlay_RejectsUnsafeOutputTargetBeforeFactory(t *testing.T) {
	tests := []struct {
		name string
		path func(string) string
	}{
		{
			name: "non-empty directory",
			path: func(root string) string {
				return root
			},
		},
		{
			name: "regular file",
			path: func(root string) string {
				return root + "/target.txt"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			outputDir := test.path(root)
			if test.name == "non-empty directory" {
				if err := os.WriteFile(filepath.Join(outputDir, "existing.txt"), []byte("occupied"), 0o600); err != nil {
					t.Fatalf("seed output directory: %v", err)
				}
			} else if err := os.WriteFile(outputDir, []byte("not a directory"), 0o600); err != nil {
				t.Fatalf("seed output file: %v", err)
			}

			factoryCalled := false
			factory := func(SessionRunOptions, string) (messages.SessionInferencer, error) {
				factoryCalled = true
				return nil, nil
			}
			_, err := RunSelfPlayWithResult(context.Background(), io.Discard, SelfPlayRunOptions{
				OutputDir:      outputDir,
				MaxDuration:    time.Second,
				MaxTurns:       3,
				SessionFactory: factory,
			})
			if err == nil || !strings.Contains(err.Error(), "output") {
				t.Fatalf("unsafe output error = %v", err)
			}
			if factoryCalled {
				t.Fatal("unsafe output target called the session factory")
			}
		})
	}
}

type selfPlayEchoPair struct {
	customer         *selfPlayEchoInferencer
	assistant        *selfPlayEchoInferencer
	customerSession  *selfPlayEchoSession
	assistantSession *selfPlayEchoSession
}

var (
	selfPlayCustomerAudio  = []byte{1, 2, 3, 4}
	selfPlayAssistantAudio = []byte{5, 6, 7, 8}
)

func newSelfPlayEchoPair() selfPlayEchoPair {
	customerSession := newSelfPlayEchoSession(nil)
	assistantSession := newSelfPlayEchoSession(nil)
	customerSession.onSend = func(msg messages.StreamMessage) {
		if msg.Type == messages.StreamTypeTextDelta {
			customerSession.emitResponse(selfPlayCustomerAudio)
			return
		}
		if msg.Type == messages.StreamTypeAudioDelta {
			customerSession.emitResponse(selfPlayCustomerAudio)
		}
	}
	assistantSession.onSend = func(msg messages.StreamMessage) {
		if msg.Type == messages.StreamTypeAudioDelta {
			assistantSession.emitResponse(selfPlayAssistantAudio)
		}
	}
	return selfPlayEchoPair{
		customer:         &selfPlayEchoInferencer{session: customerSession},
		assistant:        &selfPlayEchoInferencer{session: assistantSession},
		customerSession:  customerSession,
		assistantSession: assistantSession,
	}
}

type selfPlayEchoInferencer struct {
	session *selfPlayEchoSession
}

func (i *selfPlayEchoInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if !i.session.emit(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("self-play", "audio")}) {
		return nil, ctx.Err()
	}
	return i.session, nil
}

type selfPlayEchoSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	once    sync.Once

	mu       sync.Mutex
	isClosed bool
	sent     []messages.StreamMessage
	onSend   func(messages.StreamMessage)
}

func newSelfPlayEchoSession(onSend func(messages.StreamMessage)) *selfPlayEchoSession {
	return &selfPlayEchoSession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](128),
		done:    make(chan struct{}),
		onSend:  onSend,
	}
}

func (s *selfPlayEchoSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if ctx.Err() != nil {
		return false
	}
	s.mu.Lock()
	if s.isClosed {
		s.mu.Unlock()
		return false
	}
	s.sent = append(s.sent, cloneSelfPlayMessage(msg))
	onSend := s.onSend
	s.mu.Unlock()
	if onSend != nil {
		onSend(msg)
	}
	return true
}

func (s *selfPlayEchoSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}
func (s *selfPlayEchoSession) Done() <-chan struct{} { return s.done }

func (s *selfPlayEchoSession) Close() error {
	s.once.Do(func() {
		s.mu.Lock()
		s.isClosed = true
		s.mu.Unlock()
		close(s.done)
	})
	return nil
}

func (s *selfPlayEchoSession) emit(ctx context.Context, msg messages.StreamMessage) bool {
	return s.receive.Write(ctx, msg)
}

func (s *selfPlayEchoSession) emitResponse(pcm []byte) {
	ctx := context.Background()
	_ = s.emit(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()})
	_ = s.emit(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(pcm)})
	_ = s.emit(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()})
	_ = s.emit(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
}

func (s *selfPlayEchoSession) sentMessages() []messages.StreamMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.StreamMessage(nil), s.sent...)
}

func (s *selfPlayEchoSession) closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isClosed
}

func cloneSelfPlayMessage(msg messages.StreamMessage) messages.StreamMessage {
	if audio, ok := msg.Value.(*messages.AudioDeltaValue); ok {
		msg.Value = messages.NewAudioDeltaValue(append([]byte(nil), audio.Content...))
	}
	return msg
}

func containsAudio(messagesToCheck []messages.StreamMessage, want []byte) bool {
	for _, msg := range messagesToCheck {
		value, ok := msg.Value.(*messages.AudioDeltaValue)
		if ok && string(value.Content) == string(want) {
			return true
		}
	}
	return false
}

func countMessageType(messagesToCheck []messages.StreamMessage, want messages.StreamMessageType) int {
	count := 0
	for _, msg := range messagesToCheck {
		if msg.Type == want {
			count++
		}
	}
	return count
}

type selfPlayBlockingInferencer struct {
	session     *selfPlayEchoSession
	connected   chan struct{}
	connectOnce sync.Once
}

func newSelfPlayBlockingInferencer() *selfPlayBlockingInferencer {
	return &selfPlayBlockingInferencer{
		session:   newSelfPlayEchoSession(nil),
		connected: make(chan struct{}),
	}
}

func (i *selfPlayBlockingInferencer) ConnectSession(context.Context) (messages.Session, error) {
	i.connectOnce.Do(func() { close(i.connected) })
	if !i.session.emit(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("blocking", "audio")}) {
		return nil, errors.New("blocking session failed to publish SESSION.OPEN")
	}
	return i.session, nil
}

func (i *selfPlayBlockingInferencer) closed() bool { return i.session.closed() }

type selfPlayConnectFailInferencer struct {
	err error
}

func (i *selfPlayConnectFailInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, i.err
}
