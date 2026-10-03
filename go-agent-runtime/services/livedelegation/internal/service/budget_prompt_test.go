package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/livedelegation"
)

// streamInferencer streams a fixed message list and reports when the whole
// list was taken from it.
type streamInferencer struct {
	stream []messages.StreamMessage
	taken  chan struct{}
}

func (streamInferencer) Infer(context.Context, messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{}, errors.New("stream only")
}

func (s streamInferencer) InferStream(context.Context, messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	out := make(chan messages.StreamMessage)
	go func() {
		defer close(s.taken)
		defer close(out)
		for _, msg := range s.stream {
			out <- msg
		}
	}()
	return out, nil
}

// When the loop stops reading a backend stream, the budget drains the rest
// so the backend can finish, and the usage it reported still counts once
// although both USAGE.INFO and MESSAGE.END carry it.
func TestBudgetDrainsAnAbandonedStreamAndCountsItsUsageOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		usage := messages.TokenUsage{PromptTokens: 4, CompletionTokens: 3}
		inner := streamInferencer{taken: make(chan struct{}), stream: []messages.StreamMessage{
			{Type: messages.StreamTypeUsageInfo, Value: messages.NewUsageInfoValue(usage)},
			{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(usage)},
		}}
		spent := newBudget(inner, livedelegation.Limits{MaxTurns: 2, MaxTokens: 100})
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		out, err := spent.InferStream(ctx, messages.InferenceRequest{})
		if err != nil {
			t.Fatalf("InferStream: %v", err)
		}
		<-inner.taken
		if _, open := <-out; open {
			t.Fatal("the abandoned stream forwarded a message")
		}
		if spent.tokens != 7 || spent.turns != 1 {
			t.Fatalf("budget counted %d tokens over %d turns, want 7 over 1", spent.tokens, spent.turns)
		}
	})
}

// The task quotes at most the last twelve user and assistant messages of
// the session, each bounded on a rune boundary; other roles and empty text
// are left out.
func TestHistoryKeepsTheRecentConversationBounded(t *testing.T) {
	history := []messages.Message{
		messages.NewTextMessage(messages.RoleSystem, "system prompt"),
		messages.NewTextMessage(messages.RoleUser, "   "),
		messages.NewTextMessage(messages.RoleUser, strings.Repeat("é", historyMessageBytes)),
	}
	for i := range historyMessages {
		history = append(history, messages.NewTextMessage(messages.RoleAssistant, "turn "+string(rune('a'+i))))
	}
	got := historyText(append([]messages.Message{messages.NewTextMessage(messages.RoleUser, "oldest")}, history...))
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != historyMessages || strings.Contains(got, "oldest") || strings.Contains(got, "system prompt") {
		t.Fatalf("history = %q, want the last %d user and assistant messages", got, historyMessages)
	}
	if long := truncate(strings.Repeat("é", historyMessageBytes), historyMessageBytes); !strings.HasSuffix(long, "é...") || len(long) > historyMessageBytes+len("...") {
		t.Fatalf("truncated = %d bytes ending %q, want a whole-rune cut", len(long), long[len(long)-5:])
	}
}

// A backend the provider service cannot build is answered with a generic
// failure, the build error is logged, and the next delegation retries the
// build with the credential resolved the first time: a host reference may be
// single-use, so it is not resolved again.
func TestFailedBackendBuildIsRetriedWithTheResolvedCredential(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		vault := map[string]string{"cli-credential:1": "sk-backend"}
		take := func(_ context.Context, reference string) (string, error) {
			value, ok := vault[reference]
			if !ok {
				return "", errors.New("credential reference " + reference + " is unavailable")
			}
			delete(vault, reference)
			return value, nil
		}
		b := &backend{buildErr: errors.New("run `yui auth chatgpt`"), answer: func(context.Context, string, int) []messages.StreamMessage { return text("built") }}
		sent, logs := newAppends(), &recordingLogger{}
		executor, err := New(b, take, nil, logs).Open(t.Context(), livedelegation.Binding{
			Append: sent.append,
			Policy: livedelegation.Policy{Backend: livedelegation.Backend{Provider: "unit-backend", CredentialReference: "cli-credential:1"}},
		})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := executor.Submit(delegation("first", "task one")); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		sent.awaitCommentaries(1)
		b.mu.Lock()
		b.buildErr = nil
		b.mu.Unlock()
		if err := executor.Submit(delegation("second", "task two")); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		sent.awaitCommentaries(2)
		if err := executor.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		got := sent.commentary()
		if got["first"] != failureCommentary(errBuildFailed) || strings.Contains(got["first"], "yui auth") {
			t.Fatalf("first answer = %q, want the generic build failure", got["first"])
		}
		if got["second"] != "built" {
			t.Fatalf("second answer = %q, want the retried backend's result", got["second"])
		}
		if len(b.configs) != 2 || b.configs[1].APIKey != "sk-backend" {
			t.Fatalf("builds = %+v, want a retry with the key resolved once", b.configs)
		}
		if !strings.Contains(logs.text(), "run `yui auth chatgpt`") {
			t.Fatalf("log = %q, want the build error", logs.text())
		}
	})
}

// recordingLogger keeps every log line as "message key=value ...".
type recordingLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *recordingLogger) record(msg string, fields []logging.Field) {
	line := msg
	for _, field := range fields {
		line += fmt.Sprintf(" %s=%v", field.Key, field.Value)
	}
	l.mu.Lock()
	l.lines = append(l.lines, line)
	l.mu.Unlock()
}

func (l *recordingLogger) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func (l *recordingLogger) Debug(msg string, fields ...logging.Field) { l.record(msg, fields) }
func (l *recordingLogger) Info(msg string, fields ...logging.Field)  { l.record(msg, fields) }
func (l *recordingLogger) Warn(msg string, fields ...logging.Field)  { l.record(msg, fields) }
func (l *recordingLogger) Error(msg string, fields ...logging.Field) { l.record(msg, fields) }
func (l *recordingLogger) Fatal(msg string, fields ...logging.Field) { l.record(msg, fields) }
func (l *recordingLogger) Panic(msg string, fields ...logging.Field) { l.record(msg, fields) }

// rejectingAppends rejects every append and counts the attempts by kind.
type rejectingAppends struct {
	mu       sync.Mutex
	attempts map[messages.ContextAppendKind]int
}

func (r *rejectingAppends) append(_ context.Context, value *messages.ContextAppendValue) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts[value.Kind]++
	return errors.New("append rejected")
}

// Once an append is rejected, the delegation sends no more progress notes,
// but still tries to deliver its answer.
func TestRejectedAppendStopsProgressNotes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &backend{answer: func(_ context.Context, _ string, turn int) []messages.StreamMessage {
			if turn < 2 {
				return toolCall("call_"+string(rune('a'+turn)), "lookup")
			}
			return text("answer")
		}}
		tool := toolFunc(func(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
			return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "ok"}, nil
		})
		rejected := &rejectingAppends{attempts: make(map[messages.ContextAppendKind]int)}
		executor := open(t, t.Context(), b, livedelegation.Binding{
			Append: rejected.append, Tools: tool,
			Definitions: func() []messages.ToolDefinition { return []messages.ToolDefinition{{Name: "lookup"}} },
		}, nil)
		if err := executor.Submit(delegation("del", "look it up")); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		synctest.Wait()
		if err := executor.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if rejected.attempts[messages.ContextAppendThinking] != 1 || rejected.attempts[messages.ContextAppendCommentary] != 1 {
			t.Fatalf("append attempts = %v, want one progress note and the answer", rejected.attempts)
		}
	})
}

// A tool call waiting for its resource-group lock fails when its delegation
// ends, without running.
func TestLockedToolCallFailsWhenItsDelegationEnds(t *testing.T) {
	lock := groupLock(func(string) bool { return true })
	release, err := lock(t.Context(), "browser")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer release()
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(errCancelled)
	tools := delegationTools{lock: lock, inner: toolFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
		t.Fatal("the tool ran without its lock")
		return messages.ToolCallResponse{}, nil
	})}
	if _, err := tools.Execute(ctx, messages.ToolCall{ID: "c", Name: "browser"}); !errors.Is(err, errCancelled) {
		t.Fatalf("Execute = %v, want the delegation's cancel cause", err)
	}
}
