package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

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

// A backend the provider service cannot build is answered as a failure.
func TestBackendBuildFailureIsAnswered(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sent := newAppends()
		executor := open(t, t.Context(), &backend{buildErr: errors.New("run `yui auth chatgpt`")}, livedelegation.Binding{Append: sent.append}, nil)
		if err := executor.Submit(delegation("del", "task")); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		sent.awaitCommentaries(1)
		if err := executor.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if got := sent.commentary()["del"]; !strings.Contains(got, "build delegation backend unit-backend: run `yui auth chatgpt`") {
			t.Fatalf("answer = %q, want the build failure", got)
		}
	})
}

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

// A tool call waiting for a serialized tool fails when its delegation ends.
func TestSerializedToolCallFailsWhenItsDelegationEnds(t *testing.T) {
	gate := newToolGate(func(string) bool { return true })
	release, err := gate.acquire(t.Context(), "browser")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer release()
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(errCancelled)
	tools := delegationTools{gate: gate, inner: toolFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
		t.Fatal("the tool ran without its lock")
		return messages.ToolCallResponse{}, nil
	})}
	if _, err := tools.Execute(ctx, messages.ToolCall{ID: "c", Name: "browser"}); !errors.Is(err, errCancelled) {
		t.Fatalf("Execute = %v, want the delegation's cancel cause", err)
	}
}
