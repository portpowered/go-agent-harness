package livehost

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/livedelegation"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	live "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/fakelive"
	"go.uber.org/goleak"
)

// GPT-Live delegates "where is my order"; the backend calls the session's
// lookup tool and GPT-Live is handed the result as commentary to speak,
// tagged with the delegation id, after a quiet progress note. Nothing in the
// voice loop changes: the tool never becomes a loop tool call and nothing
// asks GPT-Live for a response.
func TestDelegationRunsASessionToolAndSpeaksTheResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := fakelive.New(fakelive.WithAPIKey(delegationLiveKey), fakelive.WithScript(
			fakelive.AwaitStarted(),
			fakelive.Send(userSaid("Where is order forty-two?", 0, 1500), delegationAt("del_order", 1400)),
			fakelive.AwaitClient(live.TypeCommentaryAppend, 1),
			fakelive.CloseSession("close_requested"),
		))
		backend := newScriptedBackend(map[string][]backendReply{
			"order forty-two": {callReply("call_1", "lookup_order", `{"order":"42"}`), textReply("Order 42 ships tomorrow.")},
		})
		tools := &sessionTools{tools: map[string]sessionTool{
			"lookup_order": func(context.Context, messages.ToolCall) (string, error) { return "status: packed, ships tomorrow", nil },
		}}
		run := startDelegationSession(t, fake, backend, tools, livedelegation.Limits{})
		if err := run.wait(t); err != nil {
			t.Fatalf("session: %v", err)
		}

		calls, _ := tools.snapshot()
		if len(calls) != 1 || calls[0].Name != "lookup_order" || calls[0].Arguments != `{"order":"42"}` {
			t.Fatalf("tool calls = %+v, want one lookup_order for order 42", calls)
		}
		got := receivedAppends(fake)
		want := []appendEvent{
			{kind: live.TypeThinkingAppend, delegationID: "del_order", content: "Delegated task in progress: running the lookup_order tool."},
			{kind: live.TypeCommentaryAppend, delegationID: "del_order", content: "Order 42 ships tomorrow."},
		}
		if !slices.Equal(got, want) {
			t.Fatalf("appends = %+v, want %+v", got, want)
		}
		if types := clientEventTypes(fake); slices.Contains(types, live.TypeResponseCreate) || slices.Contains(types, live.TypeResponseItemCreate) {
			t.Fatalf("client events = %v, want no response request", types)
		}
		assertVoiceLoopUntouched(t, run.eventKinds())
		wantEvidence := []string{
			"delegation_tool_call del_order call_1 lookup_order",
			"delegation_tool_result del_order call_1 lookup_order",
		}
		if got := run.delegationToolEvents(); !slices.Equal(got, wantEvidence) {
			t.Fatalf("delegation tool evidence = %v, want %v", got, wantEvidence)
		}
		if task := backend.requests[0].Messages; !strings.Contains(lastUserText(task), "Where is order forty-two?") {
			t.Fatalf("backend task = %q, want the transcript window", lastUserText(task))
		}
		if errs := fake.Errors(); len(errs) != 0 {
			t.Fatalf("fake errors: %v", errs)
		}
	})
}

// Two delegations run at once. The first is blocked in a tool until the
// second has been answered, so the second's commentary is sent first; each
// answer carries its own delegation id.
func TestConcurrentDelegationsAnswerOutOfOrderWithTheirOwnIDs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := fakelive.New(fakelive.WithAPIKey(delegationLiveKey), fakelive.WithScript(
			fakelive.AwaitStarted(),
			fakelive.Send(
				userSaid("Book the first table.", 0, 1000), delegationAt("del_first", 900),
				userSaid("And check the second order.", 2000, 3000), delegationAt("del_second", 2900),
			),
			fakelive.AwaitClient(live.TypeCommentaryAppend, 2),
			fakelive.CloseSession("close_requested"),
		))
		backend := newScriptedBackend(map[string][]backendReply{
			"first table":  {callReply("call_slow", "slow_booking", `{}`), textReply("First table booked.")},
			"second order": {textReply("Second order arrived.")},
		})
		tools := &sessionTools{tools: map[string]sessionTool{
			"slow_booking": func(ctx context.Context, _ messages.ToolCall) (string, error) {
				for !hasCommentary(fake, "del_second") {
					select {
					case <-time.After(10 * time.Millisecond):
					case <-ctx.Done():
						return "", ctx.Err()
					}
				}
				return "booked", nil
			},
		}}
		run := startDelegationSession(t, fake, backend, tools, livedelegation.Limits{})
		if err := run.wait(t); err != nil {
			t.Fatalf("session: %v", err)
		}

		got := commentaries(fake)
		want := []appendEvent{
			{kind: live.TypeCommentaryAppend, delegationID: "del_second", content: "Second order arrived."},
			{kind: live.TypeCommentaryAppend, delegationID: "del_first", content: "First table booked."},
		}
		if !slices.Equal(got, want) {
			t.Fatalf("commentaries = %+v, want the second answered first, each with its own id: %+v", got, want)
		}
	})
}

// With a pool limit of 1, three delegations queue behind each other: none
// is dropped, each is answered in order, and the backend never runs two at
// once.
func TestPoolLimitOneQueuesDelegationsWithoutDroppingAny(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ids := []string{"del_a", "del_b", "del_c"}
		fake := fakelive.New(fakelive.WithAPIKey(delegationLiveKey), fakelive.WithScript(
			fakelive.AwaitStarted(),
			fakelive.Send(
				userSaid("Task alpha.", 0, 500), delegationAt("del_a", 400),
				userSaid("Task bravo.", 600, 1000), delegationAt("del_b", 900),
				userSaid("Task charlie.", 1100, 1500), delegationAt("del_c", 1400),
			),
			fakelive.AwaitClient(live.TypeCommentaryAppend, len(ids)),
			fakelive.CloseSession("close_requested"),
		))
		slow := func(text string) backendReply {
			reply := textReply(text)
			reply.delay = time.Second
			return reply
		}
		backend := newScriptedBackend(map[string][]backendReply{
			"Task alpha":   {slow("alpha done")},
			"Task bravo":   {slow("bravo done")},
			"Task charlie": {slow("charlie done")},
		})
		run := startDelegationSession(t, fake, backend, nil, livedelegation.Limits{Concurrency: 1})
		if err := run.wait(t); err != nil {
			t.Fatalf("session: %v", err)
		}

		got := commentaries(fake)
		if len(got) != len(ids) {
			t.Fatalf("commentaries = %+v, want one per delegation", got)
		}
		for i, id := range ids {
			if got[i].delegationID != id {
				t.Fatalf("commentary %d answers %q, want %q (FIFO under limit 1): %+v", i, got[i].delegationID, id, got)
			}
		}
		if peak := backend.peakConcurrency(); peak != 1 {
			t.Fatalf("peak backend concurrency = %d, want 1", peak)
		}
	})
}

// A user interrupt during a running delegation cancels nothing in the
// executor: the tool keeps running and its result is still spoken.
func TestUserInterruptDoesNotCancelARunningDelegation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := fakelive.New(fakelive.WithAPIKey(delegationLiveKey), fakelive.WithScript(
			fakelive.AwaitStarted(),
			fakelive.Send(userSaid("Check the weather.", 0, 1000), delegationAt("del_weather", 900)),
			fakelive.AwaitClient(live.TypeCommentaryAppend, 1),
			fakelive.CloseSession("close_requested"),
		))
		backend := newScriptedBackend(map[string][]backendReply{
			"weather": {callReply("call_w", "weather", `{}`), textReply("It is sunny.")},
		})
		started, release := make(chan struct{}), make(chan struct{})
		tools := &sessionTools{tools: map[string]sessionTool{
			"weather": func(ctx context.Context, _ messages.ToolCall) (string, error) {
				close(started)
				select {
				case <-release:
					return "sunny", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			},
		}}
		run := startDelegationSession(t, fake, backend, tools, livedelegation.Limits{})
		<-started
		if err := run.handle.Send(t.Context(), session.LiveControl{Kind: session.LiveControlResponseCancel}); err != nil {
			t.Fatalf("interrupt: %v", err)
		}
		synctest.Wait()
		close(release)
		if err := run.wait(t); err != nil {
			t.Fatalf("session: %v", err)
		}

		if _, cancelled := tools.snapshot(); len(cancelled) != 0 {
			t.Fatalf("cancelled tools = %v, want none: an interrupt leaves backend work running", cancelled)
		}
		if got := commentaries(fake); len(got) != 1 || got[0].delegationID != "del_weather" || got[0].content != "It is sunny." {
			t.Fatalf("commentaries = %+v, want the result after the interrupt", got)
		}
	})
}

// Ending the session cancels a running delegation, joins its worker and
// sends no answer; no goroutine outlives the session.
func TestSessionEndCancelsRunningDelegations(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	synctest.Test(t, func(t *testing.T) {
		fake := fakelive.New(fakelive.WithAPIKey(delegationLiveKey), fakelive.WithScript(
			fakelive.AwaitStarted(),
			fakelive.Send(userSaid("Run the long report.", 0, 1000), delegationAt("del_report", 900)),
		))
		backend := newScriptedBackend(map[string][]backendReply{
			"long report": {callReply("call_r", "long_report", `{}`)},
		})
		started := make(chan struct{})
		tools := &sessionTools{tools: map[string]sessionTool{
			"long_report": func(ctx context.Context, _ messages.ToolCall) (string, error) {
				close(started)
				<-ctx.Done()
				return "", ctx.Err()
			},
		}}
		run := startDelegationSession(t, fake, backend, tools, livedelegation.Limits{})
		<-started
		if err := run.handle.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		<-run.done
		closeEndpoints(t, run.handle)

		if _, cancelled := tools.snapshot(); !slices.Equal(cancelled, []string{"long_report"}) {
			t.Fatalf("cancelled tools = %v, want the running long_report", cancelled)
		}
		if got := commentaries(fake); len(got) != 0 {
			t.Fatalf("commentaries = %+v, want none after the session ended", got)
		}
	})
}

// Failures are answered, never left pending: GPT-Live has no delegation
// timeout, so a backend error and a spent budget each become a short spoken
// failure for that delegation.
func TestDelegationFailuresAreSpoken(t *testing.T) {
	tests := []struct {
		name    string
		replies []backendReply
		tools   map[string]sessionTool
		limits  livedelegation.Limits
		want    string
	}{
		{
			name:    "backend error",
			replies: []backendReply{{err: errors.New("usage limit reached for this plan")}},
			want:    "The delegated task failed: the backend returned an error.",
		},
		{
			name: "turn budget",
			replies: []backendReply{
				callReply("c1", "noop", `{}`), callReply("c2", "noop", `{}`), callReply("c3", "noop", `{}`),
			},
			tools:  map[string]sessionTool{"noop": func(context.Context, messages.ToolCall) (string, error) { return "ok", nil }},
			limits: livedelegation.Limits{MaxTurns: 2},
			want:   "The delegated task stopped before finishing: " + livedelegation.ErrTurnBudget.Error(),
		},
		{
			name:    "token budget",
			replies: []backendReply{{call: &messages.ToolCall{ID: "c1", Name: "noop", Arguments: `{}`}, usage: 5000}, textReply("never")},
			tools:   map[string]sessionTool{"noop": func(context.Context, messages.ToolCall) (string, error) { return "ok", nil }},
			limits:  livedelegation.Limits{MaxTokens: 1000},
			want:    "The delegated task stopped before finishing: " + livedelegation.ErrTokenBudget.Error(),
		},
		{
			name:    "time budget",
			replies: []backendReply{callReply("c1", "stuck", `{}`)},
			tools: map[string]sessionTool{"stuck": func(ctx context.Context, _ messages.ToolCall) (string, error) {
				<-ctx.Done()
				return "", ctx.Err()
			}},
			limits: livedelegation.Limits{MaxDuration: 3 * time.Second},
			want:   "The delegated task stopped before finishing: " + livedelegation.ErrTimeBudget.Error(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				fake := fakelive.New(fakelive.WithAPIKey(delegationLiveKey), fakelive.WithScript(
					fakelive.AwaitStarted(),
					fakelive.Send(userSaid("Do the failing thing.", 0, 1000), delegationAt("del_fail", 900)),
					fakelive.AwaitClient(live.TypeCommentaryAppend, 1),
					fakelive.CloseSession("close_requested"),
				))
				backend := newScriptedBackend(map[string][]backendReply{"failing thing": tt.replies})
				var tools *sessionTools
				if tt.tools != nil {
					tools = &sessionTools{tools: tt.tools}
				}
				run := startDelegationSession(t, fake, backend, tools, tt.limits)
				if err := run.wait(t); err != nil {
					t.Fatalf("session: %v", err)
				}
				got := commentaries(fake)
				if len(got) != 1 || got[0].delegationID != "del_fail" || !strings.HasPrefix(got[0].content, tt.want) {
					t.Fatalf("commentaries = %+v, want one for del_fail starting %q", got, tt.want)
				}
				if strings.Contains(got[0].content, "usage limit") {
					t.Fatalf("commentary %q speaks the backend's error text", got[0].content)
				}
			})
		})
	}
}

// A result longer than one GPT-Live append (500 tokens) reaches GPT-Live as
// several commentary appends of the same delegation, in order, each within
// the limit.
func TestLongDelegationResultIsSplitIntoAppends(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sentence := "The quarterly figures rose in every region we track. "
		long := strings.TrimSpace(strings.Repeat(sentence, 30))
		fake := fakelive.New(fakelive.WithAPIKey(delegationLiveKey), fakelive.WithScript(
			fakelive.AwaitStarted(),
			fakelive.Send(userSaid("Summarize the quarter.", 0, 1000), delegationAt("del_long", 900)),
			fakelive.AwaitClient(live.TypeCommentaryAppend, 3),
			fakelive.CloseSession("close_requested"),
		))
		backend := newScriptedBackend(map[string][]backendReply{"quarter": {textReply(long)}})
		run := startDelegationSession(t, fake, backend, nil, livedelegation.Limits{})
		if err := run.wait(t); err != nil {
			t.Fatalf("session: %v", err)
		}

		got := commentaries(fake)
		if len(got) < 3 {
			t.Fatalf("commentaries = %d, want the %d-byte result split into at least 3", len(got), len(long))
		}
		parts := make([]string, 0, len(got))
		for _, event := range got {
			if event.delegationID != "del_long" || len(event.content) > live.MaxAppendTokens {
				t.Fatalf("append %+v, want del_long within %d bytes", event, live.MaxAppendTokens)
			}
			parts = append(parts, event.content)
		}
		if joined := strings.Join(parts, " "); joined != long {
			t.Fatalf("joined appends differ from the result:\n%q\n%q", joined, long)
		}
	})
}

func lastUserText(history []messages.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == messages.RoleUser {
			return history[i].TextContent()
		}
	}
	return ""
}

// assertVoiceLoopUntouched checks that the voice loop observed the
// delegation once and saw no tool call, response request or error: the
// delegation ran beside the loop, not through it.
func assertVoiceLoopUntouched(t *testing.T, kinds []string) {
	t.Helper()
	delegations := 0
	for _, kind := range kinds {
		switch messages.StreamMessageType(kind) { //nolint:exhaustive // Only the loop paths a delegation must not reach matter here.
		case messages.StreamTypeDelegationCreated:
			delegations++
		case messages.StreamTypeToolCallStart, messages.StreamTypeToolCallDelta, messages.StreamTypeToolCallEnd,
			messages.StreamTypeResponseCreate, messages.StreamTypeError:
			t.Fatalf("voice loop event %s; a delegation must never reach the loop's tool or response path (events %v)", kind, kinds)
		}
	}
	if delegations != 1 {
		t.Fatalf("voice loop observed %d DELEGATION.CREATED, want 1 (events %v)", delegations, kinds)
	}
}

// fastReadPolicy is an interactive tool policy that classes every tool
// fast/read, as a host binds one to an explicit capability.
type fastReadPolicy struct{}

func (fastReadPolicy) Settings() tools.InteractiveToolPolicySettings {
	return tools.InteractiveToolPolicySettings{}
}
func (fastReadPolicy) ClassForTool(string) tools.InteractiveToolClass {
	return tools.InteractiveToolClassFastRead
}
func (fastReadPolicy) TimeoutForTool(string) time.Duration  { return time.Minute }
func (p fastReadPolicy) Clone() tools.InteractiveToolPolicy { return p }
func (fastReadPolicy) Validate() error                      { return nil }

// A delegation's backend can call only the tools the session advertises.
// With an explicit participant capability (and its tool policy), a call to a
// tool the executor has but the capability does not offer never runs: the
// backend gets the capability refusal as the tool result.
func TestDelegationCannotRunAnUnadvertisedTool(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := fakelive.New(fakelive.WithAPIKey(delegationLiveKey), fakelive.WithScript(
			fakelive.AwaitStarted(),
			fakelive.Send(userSaid("Clean up my files.", 0, 1000), delegationAt("del_clean", 900)),
			fakelive.AwaitClient(live.TypeCommentaryAppend, 1),
			fakelive.CloseSession("close_requested"),
		))
		backend := newScriptedBackend(map[string][]backendReply{
			"Clean up": {callReply("call_rm", "delete_everything", `{}`), textReply("I cannot delete files.")},
		})
		executor := &sessionTools{tools: map[string]sessionTool{
			"lookup_order":      func(context.Context, messages.ToolCall) (string, error) { return "ok", nil },
			"delete_everything": func(context.Context, messages.ToolCall) (string, error) { return "deleted", nil },
		}}
		run := startDelegationSession(t, fake, backend, nil, livedelegation.Limits{}, func(request *session.LiveRequest) {
			request.Capabilities = &session.LiveCapabilities{
				Executor:    executor,
				Definitions: []messages.ToolDefinition{{Name: "lookup_order", Description: "Look up an order."}},
				ToolPolicy:  fastReadPolicy{},
			}
		})
		if err := run.wait(t); err != nil {
			t.Fatalf("session: %v", err)
		}

		if calls, _ := executor.snapshot(); len(calls) != 0 {
			t.Fatalf("executor calls = %+v, want none: delete_everything is not advertised", calls)
		}
		if refusal := toolResultText(backend.requests); !strings.Contains(refusal, `tool "delete_everything" is not available`) {
			t.Fatalf("tool result sent to the backend = %q, want the capability refusal", refusal)
		}
		if got := commentaries(fake); len(got) != 1 || got[0].content != "I cannot delete files." {
			t.Fatalf("commentaries = %+v", got)
		}
	})
}

// toolResultText is the text of the tool results in the backend's last
// request.
func toolResultText(requests []messages.InferenceRequest) string {
	if len(requests) == 0 {
		return ""
	}
	var out []string
	for _, message := range requests[len(requests)-1].Messages {
		if message.Role == messages.RoleTool {
			out = append(out, message.TextContent())
		}
	}
	return strings.Join(out, "\n")
}
