package externalconsumer

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserrunner"
	browserrunnerwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserrunner/wire"
)

type externalRun struct {
	turns []string
}

func (r *externalRun) ObserveCustomerTurn(stepID, observed string) error {
	r.turns = append(r.turns, "customer:"+stepID+":"+observed)
	return nil
}

func (r *externalRun) ObserveAssistantTurn(stepID, observed string) error {
	r.turns = append(r.turns, "assistant:"+stepID+":"+observed)
	return nil
}

func (*externalRun) RecordNavigation(browserrunner.NavigationObservation) error { return nil }

func (*externalRun) RecordCancellation(browserrunner.CancellationObservation) error { return nil }

func (*externalRun) HasInvocation(string) bool { return false }

func TestExternalConsumerUsesOnlyPublicBrowserrunnerContract(t *testing.T) {
	run := &externalRun{}
	tracker := browserrunnerwire.NewEvidenceTracker(browserrunner.EvidenceTrackerConfig{
		Steps: []browserrunner.StepBoundary{{ID: "one"}},
		Run:   run,
	})
	tracker.Configure(context.Background(), func() {}, nil)
	tracker.Observe(messages.StreamMessage{
		Type:  messages.StreamTypeTranscriptEnd,
		Value: messages.NewTranscriptEndValue("hello"),
	})
	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant})
	tracker.Observe(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("world"),
	})
	tracker.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant})
	if len(run.turns) != 2 || run.turns[0] != "customer:one:hello" || run.turns[1] != "assistant:one:world" {
		t.Fatalf("turns = %v", run.turns)
	}
}
