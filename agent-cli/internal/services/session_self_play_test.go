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
	session *selfPlayEchoSession
}

func newSelfPlayBlockingInferencer() *selfPlayBlockingInferencer {
	return &selfPlayBlockingInferencer{session: newSelfPlayEchoSession(nil)}
}

func (i *selfPlayBlockingInferencer) ConnectSession(context.Context) (messages.Session, error) {
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
