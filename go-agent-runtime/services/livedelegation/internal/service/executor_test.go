package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/livedelegation"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	llmproviders "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// backend is a provider service and provider in one. Each inference calls
// answer with the task, which returns a tool call, text, or blocks.
type backend struct {
	mu       sync.Mutex
	buildErr error
	configs  []providers.Config
	answer   func(ctx context.Context, task string, turn int) []messages.StreamMessage
	turns    map[string]int
}

func (b *backend) Build(_ context.Context, config providers.Config) (llmproviders.Provider, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.configs = append(b.configs, config)
	if b.buildErr != nil {
		return nil, b.buildErr
	}
	return b, nil
}

func (*backend) Name() string { return "unit-backend" }

func (*backend) Infer(context.Context, llmproviders.InferenceRequest) (llmproviders.InferenceResponse, error) {
	return llmproviders.InferenceResponse{}, errors.New("stream only")
}

func (b *backend) InferStream(ctx context.Context, req llmproviders.InferenceRequest) (<-chan messages.StreamMessage, error) {
	task := ""
	for _, message := range req.Messages {
		if message.Role == messages.RoleUser {
			task = message.TextContent()
			break
		}
	}
	b.mu.Lock()
	if b.turns == nil {
		b.turns = make(map[string]int)
	}
	turn := b.turns[task]
	b.turns[task]++
	b.mu.Unlock()
	out := make(chan messages.StreamMessage)
	go func() {
		defer close(out)
		for _, msg := range b.answer(ctx, task, turn) {
			select {
			case out <- msg:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func text(content string) []messages.StreamMessage {
	return []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextStart, Value: messages.NewTextStartValue()},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(content)},
		{Type: messages.StreamTypeTextEnd, Value: messages.NewTextEndValue()},
		{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{TotalTokens: 3})},
	}
}

func toolCall(id, name string) []messages.StreamMessage {
	return []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeToolCallStart, ActorProvidedIndex: 1, Value: messages.NewToolCallStartValue(id, name)},
		{Type: messages.StreamTypeToolCallEnd, ActorProvidedIndex: 1, Value: messages.NewToolCallEndValue(id, name, `{}`)},
		{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{TotalTokens: 3})},
	}
}

// appends records what an executor answered.
type appends struct {
	mu      sync.Mutex
	sent    []messages.ContextAppendValue
	changed chan struct{}
}

func newAppends() *appends { return &appends{changed: make(chan struct{}, 1)} }

func (a *appends) append(_ context.Context, value *messages.ContextAppendValue) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, *value)
	select {
	case a.changed <- struct{}{}:
	default:
	}
	return nil
}

// awaitCommentaries waits until n delegations have been answered.
func (a *appends) awaitCommentaries(n int) {
	for len(a.commentary()) < n {
		<-a.changed
	}
}

func (a *appends) commentary() map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]string)
	for _, value := range a.sent {
		if value.Kind == messages.ContextAppendCommentary && value.DelegationID != nil {
			out[*value.DelegationID] = value.Content
		}
	}
	return out
}

func open(t *testing.T, ctx context.Context, b *backend, binding livedelegation.Binding, credentials livedelegation.CredentialResolver) livedelegation.Executor {
	t.Helper()
	if binding.Policy.Backend.Provider == "" {
		binding.Policy.Backend.Provider = "unit-backend"
	}
	executor, err := New(b, credentials, nil, nil).Open(ctx, binding)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return executor
}

func delegation(id, task string) messages.DelegationCreatedValue {
	return messages.DelegationCreatedValue{ID: id, Target: messages.DelegationTargetClient, Task: task}
}

func TestOpenRequiresAContextAndAnAppendPort(t *testing.T) {
	service := New(&backend{}, nil, nil, nil)
	//nolint:staticcheck // A nil context is the input under test.
	if _, err := service.Open(nil, livedelegation.Binding{Append: newAppends().append}); err == nil {
		t.Fatal("Open(nil context) succeeded")
	}
	if _, err := service.Open(t.Context(), livedelegation.Binding{}); !errors.Is(err, livedelegation.ErrAppendUnavailable) {
		t.Fatalf("Open without append = %v, want ErrAppendUnavailable", err)
	}
}

// Cancel removes a queued delegation and stops a running one; neither is
// answered. Close then rejects new work.
func TestCancelStopsQueuedAndRunningDelegationsWithoutAnswering(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{}, 2)
		b := &backend{answer: func(ctx context.Context, _ string, _ int) []messages.StreamMessage {
			started <- struct{}{}
			<-ctx.Done()
			return nil
		}}
		sent := newAppends()
		executor := open(t, t.Context(), b, livedelegation.Binding{Append: sent.append, Policy: livedelegation.Policy{Limits: livedelegation.Limits{Concurrency: 1}}}, nil)
		for _, id := range []string{"run", "queued"} {
			if err := executor.Submit(delegation(id, "task "+id)); err != nil {
				t.Fatalf("Submit %s: %v", id, err)
			}
		}
		<-started
		if !executor.Cancel("queued") || !executor.Cancel("run") || executor.Cancel("unknown") {
			t.Fatal("Cancel did not report the known ids exactly")
		}
		synctest.Wait()
		if err := executor.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if got := sent.commentary(); len(got) != 0 {
			t.Fatalf("answers = %v, want none for cancelled delegations", got)
		}
		if err := executor.Submit(delegation("late", "late")); !errors.Is(err, livedelegation.ErrClosed) {
			t.Fatalf("Submit after Close = %v, want ErrClosed", err)
		}
		if err := executor.Close(); err != nil {
			t.Fatalf("second Close: %v", err)
		}
	})
}

// A provider-supplied task and the session history reach the backend task;
// an empty answer is still answered.
func TestTaskQuotesTheProviderTaskAndRecentHistory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var task string
		b := &backend{answer: func(_ context.Context, got string, _ int) []messages.StreamMessage {
			task = got
			return text("")
		}}
		sent := newAppends()
		history := []messages.Message{
			messages.NewTextMessage(messages.RoleUser, "Can you book it?"),
			messages.NewTextMessage(messages.RoleAssistant, "For two at seven?"),
			messages.NewTextMessage(messages.RoleUser, "yes"),
		}
		executor := open(t, t.Context(), b, livedelegation.Binding{Append: sent.append, History: func() []messages.Message { return history }}, nil)
		if err := executor.Submit(delegation("del", "Book a table for two at seven.")); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		synctest.Wait()
		if err := executor.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		for _, want := range []string{"Book a table for two at seven.", "assistant: For two at seven?", "user: yes"} {
			if !strings.Contains(task, want) {
				t.Fatalf("task = %q, want it to contain %q", task, want)
			}
		}
		if got := sent.commentary()["del"]; got != emptyCommentary {
			t.Fatalf("answer = %q, want the empty-result commentary", got)
		}
	})
}

// The backend is built once per session, with the credential reference
// resolved by the host; a missing provider or resolver is answered as a
// failure instead of failing the session.
func TestBackendIsBuiltOnceWithTheResolvedCredential(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resolved := 0
		credentials := func(_ context.Context, reference string) (string, error) {
			resolved++
			return "secret-for-" + reference, nil
		}
		b := &backend{answer: func(context.Context, string, int) []messages.StreamMessage { return text("done") }}
		sent := newAppends()
		executor := open(t, t.Context(), b, livedelegation.Binding{
			Append: sent.append,
			Policy: livedelegation.Policy{Backend: livedelegation.Backend{Provider: "unit-backend", Model: "m", CredentialReference: "ref"}},
		}, credentials)
		for _, id := range []string{"one", "two"} {
			if err := executor.Submit(delegation(id, "task "+id)); err != nil {
				t.Fatalf("Submit: %v", err)
			}
		}
		synctest.Wait()
		if err := executor.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if len(b.configs) != 1 || resolved != 1 || b.configs[0].APIKey != "secret-for-ref" || b.configs[0].Model != "m" {
			t.Fatalf("builds = %+v after %d resolutions, want one build with the resolved key", b.configs, resolved)
		}
		if got := sent.commentary(); got["one"] != "done" || got["two"] != "done" {
			t.Fatalf("answers = %v", got)
		}
	})
}

// An unavailable backend is answered with a generic failure; the detail,
// which can name a host credential reference, is logged and never spoken.
func TestUnavailableBackendIsAnsweredAsAFailure(t *testing.T) {
	tests := []struct {
		name       string
		backend    livedelegation.Backend
		wantLogged string
	}{
		{name: "no provider", backend: livedelegation.Backend{Provider: " "}, wantLogged: livedelegation.ErrBackendUnavailable.Error()},
		{name: "no resolver", backend: livedelegation.Backend{Provider: "unit-backend", CredentialReference: "cli-credential:7"}, wantLogged: "no resolver for the backend credential"},
		{name: "unconfigured", backend: livedelegation.Backend{Provider: "openai", Unconfigured: "set session.delegation.model"}, wantLogged: "set session.delegation.model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sent, logs := newAppends(), &recordingLogger{}
				executor, err := New(&backend{}, nil, nil, logs).Open(t.Context(), livedelegation.Binding{Append: sent.append, Policy: livedelegation.Policy{Backend: tt.backend}})
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				if err := executor.Submit(delegation("del", "task")); err != nil {
					t.Fatalf("Submit: %v", err)
				}
				synctest.Wait()
				if err := executor.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
				want := "The delegated task failed: the delegation backend is not available. Tell the user it did not work and offer to try again."
				if got := sent.commentary()["del"]; got != want {
					t.Fatalf("answer = %q, want %q", got, want)
				}
				if logged := logs.text(); !strings.Contains(logged, tt.wantLogged) || !strings.Contains(logged, "delegation_id=del") {
					t.Fatalf("log = %q, want the detail %q for del", logged, tt.wantLogged)
				}
			})
		})
	}
}

// A serialized tool runs one call at a time across delegations; another
// tool runs concurrently.
func TestSerializedToolsRunOneCallAtATime(t *testing.T) {
	for _, tt := range []struct {
		name     string
		serial   bool
		wantPeak int
	}{{name: "serialized", serial: true, wantPeak: 1}, {name: "concurrent", wantPeak: 2}} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				if peak := peakBrowserCalls(t, tt.serial); peak != tt.wantPeak {
					t.Fatalf("peak concurrent browser calls = %d, want %d", peak, tt.wantPeak)
				}
			})
		})
	}
}

// peakBrowserCalls runs two delegations that each call a one-second browser
// tool and returns how many browser calls overlapped at most.
func peakBrowserCalls(t *testing.T, serial bool) int {
	t.Helper()
	b := &backend{answer: func(_ context.Context, task string, turn int) []messages.StreamMessage {
		if turn == 0 {
			return toolCall("call_"+task[len(task)-1:], "browser")
		}
		return text("ok")
	}}
	var mu sync.Mutex
	active, peak := 0, 0
	tool := toolFunc(func(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
		mu.Lock()
		active++
		peak = max(peak, active)
		mu.Unlock()
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
		}
		mu.Lock()
		active--
		mu.Unlock()
		return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "done"}, ctx.Err()
	})
	sent := newAppends()
	executor := open(t, t.Context(), b, livedelegation.Binding{
		Append: sent.append, Tools: tool,
		Definitions: func() []messages.ToolDefinition { return []messages.ToolDefinition{{Name: "browser"}} },
		Serialized:  func(name string) bool { return serial && name == "browser" },
	}, nil)
	for _, id := range []string{"a", "b"} {
		if err := executor.Submit(delegation(id, "use the browser "+id)); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	sent.awaitCommentaries(2)
	if err := executor.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := sent.commentary(); got["a"] != "ok" || got["b"] != "ok" {
		t.Fatalf("answers = %v, want both", got)
	}
	mu.Lock()
	defer mu.Unlock()
	return peak
}

type toolFunc func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error)

func (f toolFunc) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	return f(ctx, call)
}

// A delegation waiting for a serialized tool stops waiting when it is
// cancelled.
func TestToolGateWaitEndsWithTheDelegation(t *testing.T) {
	gate := newToolGate(func(string) bool { return true })
	release, err := gate.acquire(t.Context(), "browser")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer release()
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(errCancelled)
	if _, err := gate.acquire(ctx, "browser"); !errors.Is(err, errCancelled) {
		t.Fatalf("acquire after cancel = %v, want the cancel cause", err)
	}
}
