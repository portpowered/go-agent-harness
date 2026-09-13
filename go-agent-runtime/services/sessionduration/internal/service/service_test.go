package service

import (
	"errors"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func TestStateProjectsOutputStates(t *testing.T) {
	cases := []struct {
		name string
		seen []messages.StreamMessage
		want messages.TerminalOutputState
	}{
		{name: "none", want: messages.TerminalOutputNone},
		{name: "partial", seen: []messages.StreamMessage{{Type: messages.StreamTypeTextDelta}}, want: messages.TerminalOutputPartial},
		{name: "complete", seen: []messages.StreamMessage{{Type: messages.StreamTypeTextDelta}, {Type: messages.StreamTypeMessageEnd}}, want: messages.TerminalOutputComplete},
		{name: "user transcript is not output", seen: []messages.StreamMessage{{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser}}, want: messages.TerminalOutputNone},
		{name: "assistant transcript is output", seen: []messages.StreamMessage{{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant}}, want: messages.TerminalOutputPartial},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := New().NewState(sessionduration.TerminalSource{})
			for _, msg := range tc.seen {
				state.Observe(msg)
			}
			if got := state.OutputState(); got != tc.want {
				t.Fatalf("output state = %q, want %q", got, tc.want)
			}
		})
	}
	state := New().NewState(sessionduration.TerminalSource{})
	state.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta})
	state.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart})
	if got := state.OutputState(); got != messages.TerminalOutputNone {
		t.Fatalf("message start did not reset output state: %q", got)
	}
}

func TestStateAdmitsProviderTerminalBeforeLoopClose(t *testing.T) {
	provider := providerTerminal("provider-close")
	state := New().NewState(providerSource(provider))
	loopClose := messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("", "loop-close")}
	if _, write := state.Admit(true, loopClose); write {
		t.Fatal("loop shutdown close was treated as provider evidence")
	}
	if state.Written() {
		t.Fatal("rejected loop close marked the terminal written")
	}
	if got, write := state.Admit(true, provider); !write || got.Value != provider.Value {
		t.Fatalf("provider close = (%+v, %v), want admitted provider message", got, write)
	}
	if !state.Written() {
		t.Fatal("provider close did not mark the terminal written")
	}
	if _, write := state.Admit(true, provider); write {
		t.Fatal("duplicate provider close was admitted")
	}
	nonplanned := New().NewState(providerSource(provider))
	if _, write := nonplanned.Admit(false, loopClose); !write || nonplanned.Written() {
		t.Fatal("unplanned loop close precedence changed")
	}
}

func TestStatePublishesProviderTerminalOnceInArtifactThenOutputOrder(t *testing.T) {
	provider := providerTerminal("provider-close")
	state := New().NewState(providerSource(provider))
	var mu sync.Mutex
	var events []string
	publication := sessionduration.Publication{
		Artifacts: artifactFunc(func(messages.StreamMessage) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, "artifact")
			return nil
		}),
		Write: sessionduration.MessageWriter(func(messages.StreamMessage) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, "output")
			return nil
		}),
	}
	if err := state.PublishProviderTerminal(publication); err != nil {
		t.Fatalf("publish provider terminal: %v", err)
	}
	if err := state.PublishProviderTerminal(publication); err != nil {
		t.Fatalf("duplicate provider terminal: %v", err)
	}
	if got, want := len(events), 2; got != want {
		t.Fatalf("publication count = %d, want %d (%v)", got, want, events)
	}
	if events[0] != "artifact" || events[1] != "output" {
		t.Fatalf("publication order = %v, want artifact then output", events)
	}
}

func TestStateRetriesProviderTerminalAfterArtifactFailure(t *testing.T) {
	provider := providerTerminal("provider-close")
	state := New().NewState(providerSource(provider))
	artifactErr := errors.New("artifact identity")
	first := true
	publication := sessionduration.Publication{Artifacts: artifactFunc(func(messages.StreamMessage) error {
		if first {
			first = false
			return artifactErr
		}
		return nil
	}), Write: sessionduration.MessageWriter(func(messages.StreamMessage) error { return nil })}
	if err := state.PublishProviderTerminal(publication); !errors.Is(err, artifactErr) {
		t.Fatalf("artifact error = %v, want identity", err)
	}
	if state.Written() {
		t.Fatal("failed publication marked the terminal written")
	}
	if err := state.PublishProviderTerminal(publication); err != nil {
		t.Fatalf("retry provider terminal: %v", err)
	}
	if !state.Written() {
		t.Fatal("successful retry did not mark the terminal written")
	}
}

func TestServicePublishesMaxDurationMetadata(t *testing.T) {
	service := New()
	var accepted, written messages.StreamMessage
	publication := sessionduration.Publication{
		Artifacts: artifactFunc(func(msg messages.StreamMessage) error {
			accepted = msg
			return nil
		}),
		Write: sessionduration.MessageWriter(func(msg messages.StreamMessage) error {
			written = msg
			return nil
		}),
	}
	if err := service.PublishMaxDuration(publication, messages.TerminalOutputPartial); err != nil {
		t.Fatalf("publish max duration: %v", err)
	}
	if accepted.Type != messages.StreamTypeSessionClose || written.Type != messages.StreamTypeSessionClose {
		t.Fatalf("terminal messages were not published: accepted=%+v written=%+v", accepted, written)
	}
	value, ok := written.Value.(*messages.SessionCloseValue)
	if !ok || value == nil {
		t.Fatalf("max duration value = %T, want session close", written.Value)
	}
	if value.Reason != "max_duration" || value.Classification != "max_duration" || value.TerminalReason != messages.TerminalReason("max_duration") || value.TerminalProvenance != messages.TerminalProvenanceLoop || value.OutputState != messages.TerminalOutputPartial {
		t.Fatalf("max duration metadata = %+v", value)
	}
}

func TestServiceJoinsLifecycleAndTransportErrors(t *testing.T) {
	runtimeErr := errors.New("runtime identity")
	closeErr := errors.New("close identity")
	bindingErr := errors.New("binding identity")
	err := New().LifecycleError(sessionduration.LifecycleFailures{Runtime: runtimeErr, Close: closeErr, Binding: bindingErr})
	if !errors.Is(err, runtimeErr) || !errors.Is(err, closeErr) || !errors.Is(err, bindingErr) {
		t.Fatalf("lifecycle identities lost: %v", err)
	}
	transportErr := errors.New("transport identity")
	if got := New().TransportError(transportErr); !errors.Is(got, transportErr) {
		t.Fatalf("transport identity lost: %v", got)
	}
	if New().TransportError(nil) != nil || New().LifecycleError(sessionduration.LifecycleFailures{}) != nil {
		t.Fatal("nil error composition was not nil")
	}
}

func TestStateSerializesDuplicateProviderPublication(t *testing.T) {
	provider := providerTerminal("provider-close")
	state := New().NewState(providerSource(provider))
	var mu sync.Mutex
	writes := 0
	publication := sessionduration.Publication{Write: sessionduration.MessageWriter(func(messages.StreamMessage) error {
		mu.Lock()
		writes++
		mu.Unlock()
		return nil
	})}
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := state.PublishProviderTerminal(publication); err != nil {
				t.Errorf("concurrent publication: %v", err)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if writes != 1 {
		t.Fatalf("concurrent writes = %d, want 1", writes)
	}
}

func TestServiceHandlesNilPublicationPorts(t *testing.T) {
	if err := New().PublishMaxDuration(sessionduration.Publication{}, messages.TerminalOutputNone); err != nil {
		t.Fatalf("nil max-duration ports: %v", err)
	}
	state := New().NewState(providerSource(providerTerminal("provider-close")))
	if err := state.PublishProviderTerminal(sessionduration.Publication{}); err != nil {
		t.Fatalf("nil provider ports: %v", err)
	}
	if !state.Written() {
		t.Fatal("nil publication did not complete the local terminal transition")
	}
}

type artifactFunc func(messages.StreamMessage) error

func (f artifactFunc) Accept(msg messages.StreamMessage) error { return f(msg) }

func providerSource(provider messages.StreamMessage) sessionduration.TerminalSource {
	value := provider.Value.(*messages.SessionCloseValue)
	return sessionduration.TerminalSource{
		Message: func() (messages.StreamMessage, bool) { return provider, true },
		Matches: func(msg messages.StreamMessage) bool {
			candidate, ok := msg.Value.(*messages.SessionCloseValue)
			return ok && candidate == value
		},
	}
}

func providerTerminal(reason string) messages.StreamMessage {
	return messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValueWithTerminal(
		"session", reason, "provider_close", messages.TerminalReasonProviderClose,
		messages.TerminalProvenanceProvider, messages.TerminalOutputPartial,
	)}
}
