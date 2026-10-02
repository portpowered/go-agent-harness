package openailive_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	live "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/fakelive"
)

// testDelegationID is the delegation most tests answer.
const testDelegationID = "del_1"

// delegationAt is a client delegation decided at offsetMS on the timeline.
func delegationAt(id string, offsetMS int64) live.DelegationCreated {
	return live.DelegationCreated{EventID: "evt_del", OffsetMS: offsetMS, Delegation: live.DelegationInfo{ID: id, Type: "delegation", Target: live.DelegationClient}}
}

// runLoop runs a duplex agent loop over the GPT-Live provider until the test
// ends.
func runLoop(t *testing.T, server *fakelive.Server, options ...agentloop.Option) *agentloop.AgentLoop {
	t.Helper()
	provider := live.New(live.WithCredentialProvider(live.APIKeyCredentials(sessionKey)), live.WithWebSocketDialer(server.Dialer()))
	options = append([]agentloop.Option{
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(liveInferencer{provider: provider, cfg: pcmConfig()}),
		agentloop.WithToolExecutionDisabled(),
	}, options...)
	loop, err := agentloop.New(options...)
	if err != nil {
		t.Fatalf("agentloop.New: %v", err)
	}
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Run: %v", err)
		}
	})
	return loop
}

func delegationValue(t *testing.T, msg messages.StreamMessage) *messages.DelegationCreatedValue {
	t.Helper()
	value, ok := msg.Value.(*messages.DelegationCreatedValue)
	if msg.Type != messages.StreamTypeDelegationCreated || !ok || value == nil {
		t.Fatalf("message = %s %T, want DELEGATION.CREATED", msg.Type, msg.Value)
	}
	if msg.ResponseID != "" || msg.ResponsePurpose != "" {
		t.Fatalf("delegation carries response %q purpose %q, want neither", msg.ResponseID, msg.ResponsePurpose)
	}
	return value
}

// GPT-Live keeps talking while it delegates ("let me check"). The delegation
// arrives mid-segment through the real agent loop: it must not retire the
// segment, so every audio and transcript delta of the segment reaches the
// loop's delta stream, the segment ends completed, and the delegation is
// reported once with the user's request and no response id.
func TestDelegationMidSegmentLosesNoSegmentOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newFake(fakelive.AwaitStarted(), fakelive.Send(
			inputText("A table for two at seven, please.", 0, 1800),
			audioDelta([]byte{1, 0}),
			outputText("Let me check ", 2000, 2300),
			delegationAt("del_abc123", 1900),
			audioDelta([]byte{2, 0}),
			outputText("that for you.", 2300, 2600),
			audioDelta([]byte{3, 0}),
		))
		loop := runLoop(t, server)

		audio, text, delegations := 0, "", 0
		for msg := range loop.Deltas().Chan() {
			switch value := msg.Value.(type) {
			case *messages.AudioDeltaValue:
				audio++
			case *messages.TranscriptDeltaValue:
				if msg.Role == messages.RoleAssistant {
					text += value.Text
				}
			case *messages.DelegationCreatedValue:
				delegations++
				got := delegationValue(t, msg)
				if got.ID != "del_abc123" || got.Target != messages.DelegationTargetClient || got.OffsetMS != 1900 {
					t.Fatalf("delegation = %+v, want del_abc123 for the client at 1900 ms", got)
				}
				if len(got.Transcript) == 0 || got.Transcript[0].Speaker != messages.RoleUser || got.Transcript[0].Text != "A table for two at seven, please." {
					t.Fatalf("delegation transcript = %+v, want the user's request first", got.Transcript)
				}
			case *messages.MessageEndValue:
				if msg.ResponseID != firstSeg || value.Status != completed {
					t.Fatalf("segment %q ended %q, want %s completed", msg.ResponseID, value.Status, firstSeg)
				}
				if audio != 3 || text != "Let me check that for you." || delegations != 1 {
					t.Fatalf("segment delivered %d audio deltas, text %q and %d delegations; want 3, the whole text and 1", audio, text, delegations)
				}
				return
			}
		}
		t.Fatal("delta stream closed before the segment ended")
	})
}

// With the loop's delta consumer stalled behind a burst of speech audio, the
// delegation still reaches it: audio may be shed, the delegation never is,
// because GPT-Live never times a client delegation out.
func TestDelegationReachesTheLoopUnderOutboxPressure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Far more audio than every buffer between the provider and the
		// loop's consumer holds together, on both sides of the delegation.
		const half = 600
		burst := make([]live.Event, 0, 2*half+2)
		burst = append(burst, inputText("Check my order.", 0, 900))
		for range half {
			burst = append(burst, audioDelta(make([]byte, 480)))
		}
		burst = append(burst, delegationAt(testDelegationID, 900))
		for range half {
			burst = append(burst, audioDelta(make([]byte, 480)))
		}
		server := newFake(fakelive.AwaitStarted(), fakelive.Send(burst...))
		loop := runLoop(t, server, agentloop.WithBufferCapacity(4))

		synctest.Wait() // the burst has been mapped while nobody reads
		audio, delegations := 0, 0
		for msg := range loop.Deltas().Chan() {
			switch msg.Type { //nolint:exhaustive // Counts audio and delegations up to the segment end.
			case messages.StreamTypeAudioDelta:
				audio++
			case messages.StreamTypeDelegationCreated:
				delegations++
				if got := delegationValue(t, msg); got.ID != testDelegationID {
					t.Fatalf("delegation id = %q, want del_1", got.ID)
				}
			case messages.StreamTypeMessageEnd:
				if audio >= 2*half {
					t.Fatal("every audio delta arrived; the test did not put the outbox under pressure")
				}
				if delegations != 1 {
					t.Fatalf("delegations delivered = %d, want 1 despite %d shed audio deltas", delegations, 2*half-audio)
				}
				return
			}
		}
		t.Fatal("delta stream closed without the delegation")
	})
}

// A delegation can arrive before the user's request is transcribed. It waits
// for the transcript that covers its offset, then is reported exactly once
// with that transcript.
func TestDelegationWaitsForItsTranscript(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const transcriptDelay = 150 * time.Millisecond
		server := newFake(fakelive.AwaitStarted(),
			fakelive.Send(delegationAt(testDelegationID, 1000), inputText("What's the weather", 0, 700)),
			fakelive.Wait(transcriptDelay),
			fakelive.Send(inputText(" in Paris?", 700, 1100)),
		)
		session := connectFake(t, server, pcmConfig())
		skipOpen(t, session)
		start := time.Now()
		got := collectUntil(t, session, start, messages.StreamTypeDelegationCreated)
		delegation := got[len(got)-1]
		if delegation.at != transcriptDelay {
			t.Fatalf("delegation reported at %v, want when its transcript arrived (%v)", delegation.at, transcriptDelay)
		}
		value := delegationValue(t, delegation.msg)
		if len(value.Transcript) != 1 || value.Transcript[0].Text != "What's the weather in Paris?" || value.Transcript[0].EndMS != 1100 {
			t.Fatalf("delegation transcript = %+v, want the whole question as one span", value.Transcript)
		}
		assertNoFurtherDelegation(t, session)
	})
}

// A delegation whose transcript never arrives is reported once the
// configured settle window has passed, with whatever was transcribed.
func TestDelegationIsReportedAfterTheSettleWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const settle = 250 * time.Millisecond
		server := newFake(fakelive.AwaitStarted(), fakelive.Send(delegationAt(testDelegationID, 1000)))
		session := connectFake(t, server, pcmConfig(), live.WithDelegationSettle(settle))
		skipOpen(t, session)
		start := time.Now()
		got := collectUntil(t, session, start, messages.StreamTypeDelegationCreated)
		delegation := got[len(got)-1]
		if delegation.at != settle {
			t.Fatalf("delegation reported at %v, want after the settle window %v", delegation.at, settle)
		}
		if value := delegationValue(t, delegation.msg); len(value.Transcript) != 0 {
			t.Fatalf("delegation transcript = %+v, want none", value.Transcript)
		}
		assertNoFurtherDelegation(t, session)
	})
}

// The settle window holds inside an open segment: with the idle watcher
// already asleep until the segment's gap, a delegation arriving 50 ms in is
// still reported at its own deadline, 50 ms + D, not at the gap.
func TestDelegationSettleWindowInsideASegmentKeepsItsDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const arrival = 50 * time.Millisecond
		server := newFake(fakelive.AwaitStarted(),
			fakelive.Send(outputText("Sure, ", 0, 200)),
			fakelive.Wait(arrival),
			fakelive.Send(delegationAt(testDelegationID, 1000)),
		)
		session := connectFake(t, server, pcmConfig())
		skipOpen(t, session)
		received := collectUntil(t, session, time.Now(), messages.StreamTypeDelegationCreated)
		if got, want := received[len(received)-1].at, arrival+live.DefaultDelegationSettle; got != want {
			t.Fatalf("delegation reported at %v, want %v (before the %v segment gap)", got, want, live.DefaultSegmentGap)
		}
		for _, r := range received {
			if r.msg.Type == messages.StreamTypeMessageEnd {
				t.Fatal("the segment closed before the delegation's settle deadline")
			}
		}
	})
}

// A delegation held in its settle window when the session closes is still
// reported, before SESSION.CLOSE. Responses-mode delegations never are.
func TestHeldDelegationIsReportedBeforeTheSessionCloses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		responses := live.DelegationCreated{OffsetMS: 10, Delegation: live.DelegationInfo{ID: "del_r", Type: "delegation", Target: live.DelegationResponses}}
		server := newFake(fakelive.AwaitStarted(),
			fakelive.Send(responses, delegationAt(testDelegationID, 1000)),
			fakelive.CloseSession(live.CloseReasonExpired),
		)
		session := connectFake(t, server, pcmConfig())
		skipOpen(t, session)
		received := collectUntil(t, session, time.Now(), messages.StreamTypeSessionClose)
		types := typesOf(received)
		if len(types) != 2 || types[0] != messages.StreamTypeDelegationCreated {
			t.Fatalf("messages = %v, want DELEGATION.CREATED then SESSION.CLOSE", types)
		}
		if value := delegationValue(t, received[0].msg); value.ID != testDelegationID || received[0].at != 0 {
			t.Fatalf("delegation %q reported at %v, want del_1 at the close", value.ID, received[0].at)
		}
	})
}

// assertNoFurtherDelegation fails if another delegation follows before the
// settle window has passed twice more.
func assertNoFurtherDelegation(t *testing.T, session messages.Session) {
	t.Helper()
	timer := time.NewTimer(2 * live.DefaultDelegationSettle)
	defer timer.Stop()
	for {
		select {
		case msg := <-session.Receive().Chan():
			if msg.Type == messages.StreamTypeDelegationCreated {
				t.Fatal("the delegation was reported twice")
			}
		case <-timer.C:
			return
		}
	}
}

// CONTEXT.APPEND maps each kind to its GPT-Live append command, with the
// delegation id or an explicit null, and the acknowledgements stay inside the
// provider.
func TestContextAppendSendsTheMatchingAppendCommand(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newFake(fakelive.AwaitStarted(), fakelive.Send(inputText("Book it.", 0, 500), delegationAt(testDelegationID, 400)))
		session := connectFake(t, server, pcmConfig())
		skipOpen(t, session)
		collectUntil(t, session, time.Now(), messages.StreamTypeDelegationCreated)

		appends := []*messages.ContextAppendValue{
			messages.NewContextAppendValue(messages.ContextAppendInstructions, "Greet the caller."),
			messages.NewDelegationContextAppendValue(messages.ContextAppendThinking, testDelegationID, "Looking up tables."),
			messages.NewDelegationContextAppendValue(messages.ContextAppendCommentary, testDelegationID, "Booked for seven."),
		}
		for _, value := range appends {
			if outcome := messages.SendSessionWithOutcome(t.Context(), session, messages.StreamMessage{Type: messages.StreamTypeContextAppend, Value: value}); !outcome.OK() {
				t.Fatalf("send %s append: %+v", value.Kind, outcome)
			}
		}
		synctest.Wait()

		var got []string
		for _, event := range server.ClientEvents() {
			switch typed := event.(type) {
			case live.InstructionsAppend:
				got = append(got, describeAppend(event.EventType(), live.ContextAppend(typed)))
			case live.ThinkingAppend:
				got = append(got, describeAppend(event.EventType(), live.ContextAppend(typed)))
			case live.CommentaryAppend:
				got = append(got, describeAppend(event.EventType(), live.ContextAppend(typed)))
			}
		}
		want := []string{
			live.TypeInstructionsAppend + " <null> Greet the caller.",
			live.TypeThinkingAppend + " del_1 Looking up tables.",
			live.TypeCommentaryAppend + " del_1 Booked for seven.",
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("append commands =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		if errs := server.Errors(); len(errs) != 0 {
			t.Fatalf("fake server errors: %v", errs)
		}
		select {
		case msg := <-session.Receive().Chan():
			t.Fatalf("an acknowledgement reached the stream as %s", msg.Type)
		default:
		}
	})
}

// A result over the 500-token limit is split into appends that each fit,
// in order, under the same delegation id.
func TestLongContextAppendIsSplitUnderTheTokenLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newFake(fakelive.AwaitStarted(), fakelive.Send(inputText("Summarize it.", 0, 500), delegationAt(testDelegationID, 400)))
		session := connectFake(t, server, pcmConfig())
		skipOpen(t, session)
		collectUntil(t, session, time.Now(), messages.StreamTypeDelegationCreated)

		content := strings.Repeat("The order shipped on Monday and arrives on Thursday. ", 60)
		value := messages.NewDelegationContextAppendValue(messages.ContextAppendCommentary, testDelegationID, content)
		if outcome := messages.SendSessionWithOutcome(t.Context(), session, messages.StreamMessage{Type: messages.StreamTypeContextAppend, Value: value}); !outcome.OK() {
			t.Fatalf("send: %+v", outcome)
		}
		synctest.Wait()

		var chunks []string
		for _, event := range server.ClientEvents() {
			commentary, ok := event.(live.CommentaryAppend)
			if !ok {
				continue
			}
			if commentary.DelegationID == nil || *commentary.DelegationID != testDelegationID {
				t.Fatalf("chunk delegation id = %v, want del_1", commentary.DelegationID)
			}
			// No byte-level BPE token covers less than one byte.
			if len(commentary.Content) > live.MaxAppendTokens {
				t.Fatalf("chunk of %d bytes may exceed %d tokens", len(commentary.Content), live.MaxAppendTokens)
			}
			if !strings.HasSuffix(commentary.Content, ".") {
				t.Fatalf("chunk %q does not end at a sentence", commentary.Content)
			}
			chunks = append(chunks, commentary.Content)
		}
		if len(chunks) < 2 || strings.Join(chunks, " ") != strings.TrimSpace(content) {
			t.Fatalf("%d chunks rejoin to a different text, want the content split in order", len(chunks))
		}
	})
}

// An append with no content or an unknown kind has no wire event and fails
// terminally, never as a silent success.
func TestMalformedContextAppendFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newFake(fakelive.AwaitStarted())
		session := connectFake(t, server, pcmConfig())
		skipOpen(t, session)
		for _, value := range []messages.StreamMessageValue{
			messages.NewContextAppendValue(messages.ContextAppendThinking, "  "),
			messages.NewContextAppendValue("shout", "Hello."),
			messages.NewTextDeltaValue("wrong value"),
		} {
			outcome := messages.SendSessionWithOutcome(t.Context(), session, messages.StreamMessage{Type: messages.StreamTypeContextAppend, Value: value})
			if outcome.Status != messages.SessionSendTerminalFailure || !errors.Is(outcome.Err, live.ErrNoWireEvent) {
				t.Fatalf("send %#v = %+v, want a terminal failure wrapping ErrNoWireEvent", value, outcome)
			}
		}
	})
}

func describeAppend(eventType string, body live.ContextAppend) string {
	id := "<null>"
	if body.DelegationID != nil {
		id = *body.DelegationID
	}
	return eventType + " " + id + " " + body.Content
}
