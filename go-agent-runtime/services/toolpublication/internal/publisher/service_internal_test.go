package publisher

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/toolpublication"
)

func TestNoOpCommitAdvancesStaleGenerationGuard(t *testing.T) {
	event := publicationEvent{
		browserID:  "browser",
		targetID:   "tab",
		generation: 4,
		sequence:   7,
	}
	publisher := &publisher{
		state: toolpublication.State{
			LastSuccessfulDefinitions: []messages.ToolDefinition{{Name: "page"}},
			LastSuccessfulDigest:      "digest",
		},
		pending:    event,
		hasPending: true,
	}

	publisher.commitSuccessfulPublication(event, true, publisher.state.LastSuccessfulDefinitions, "digest", false)
	state := publisher.Snapshot()
	if state.PublicationCount != 0 {
		t.Fatalf("no-op publication count = %d, want zero", state.PublicationCount)
	}
	if state.LastSuccessfulBrowserID != event.browserID || state.LastSuccessfulTargetID != event.targetID ||
		state.LastSuccessfulGeneration != event.generation || state.LastSuccessfulEventSequence != event.sequence {
		t.Fatalf("no-op event state = %#v", state)
	}
	if publisher.hasPending {
		t.Fatal("no-op commit left pending publication work")
	}

	if publisher.consumeEvent(toolpublication.Event{
		Type:       toolpublication.EventGenerationChanged,
		BrowserID:  event.browserID,
		TargetID:   event.targetID,
		Generation: 3,
		Sequence:   8,
	}) {
		t.Fatal("stale generation after no-op was accepted")
	}
	state = publisher.Snapshot()
	if state.LatestEventSequence != 8 || state.LastSuccessfulGeneration != event.generation {
		t.Fatalf("stale event after no-op changed state = %#v", state)
	}
	if publisher.consumeEvent(toolpublication.Event{
		Type:       toolpublication.EventCatalogChanged,
		BrowserID:  event.browserID,
		TargetID:   event.targetID,
		Generation: 5,
		Sequence:   8,
	}) {
		t.Fatal("duplicate event sequence after no-op was accepted")
	}
}
