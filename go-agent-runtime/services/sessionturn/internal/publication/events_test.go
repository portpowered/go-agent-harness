package publication

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const (
	maxDiagnosticLength = 420
	publishedGeneration = 4
	publishedSequence   = 10
	longCauseLength     = 1000
	phaseBrokerSend     = phaseBrokerEvent + suffixSend
)

func unitPublisher(base, initial []messages.ToolDefinition, refresh func(context.Context) ([]messages.ToolDefinition, error), publish func(context.Context, []messages.ToolDefinition) error) *Publisher {
	return New(sessionturn.PublicationRequest{
		StaticStableDefinitions: base, InitialDefinitions: initial,
		Watch:   eventWatch(make(chan sessionturn.BrowserEvent)),
		Refresh: refresh, Publish: publish,
	})
}

func fixed(definitions []messages.ToolDefinition, err error) func(context.Context) ([]messages.ToolDefinition, error) {
	return func(context.Context) ([]messages.ToolDefinition, error) { return definitions, err }
}

func catalogEventAt(generation, sequence uint64) sessionturn.BrowserEvent {
	return sessionturn.BrowserEvent{Type: sessionturn.BrowserEventCatalogChanged, BrowserID: browserID, TargetID: tabID, Generation: generation, Sequence: sequence}
}

func TestNoOpRefreshKeepsLastSuccessfulState(t *testing.T) {
	base := []messages.ToolDefinition{definition(stableTool, "stable")}
	page := []messages.ToolDefinition{definition(pageToolName, "page")}
	publisher := unitPublisher(base, concat(base, page), fixed(page, nil), nil)
	if !publisher.consumeEvent(sessionturn.BrowserEvent{Type: sessionturn.BrowserEventCatalogChanged, Sequence: 9}) {
		t.Fatal("first sequenced catalog event was not accepted")
	}
	if publisher.consumeEvent(sessionturn.BrowserEvent{Type: sessionturn.BrowserEventCatalogChanged, Sequence: 9}) {
		t.Fatal("duplicate sequenced catalog event triggered a second refresh")
	}
	if err := publisher.refreshAndPublish(context.Background(), "duplicate_event"); err != nil {
		t.Fatalf("no-op refresh = %v", err)
	}
	state := publisher.State()
	if !reflect.DeepEqual(state.LastSuccessfulDefinitions, Merge(base, page)) || state.PublicationCount != 0 || publisher.hasPending {
		t.Fatalf("state = %#v, pending=%v", state, publisher.hasPending)
	}
}

func TestDigestIncludesCompleteParameterSchema(t *testing.T) {
	first := definition(pageToolName, "page")
	first.ParameterSchema = json.RawMessage(`{"type":"object","properties":{"moves":{"type":"array","items":{"type":"string"}}}}`)
	second := first
	second.ParameterSchema = json.RawMessage(`{"type":"object","properties":{"moves":{"type":"array","items":{"type":"integer"}}}}`)
	firstDigest, err := Digest([]messages.ToolDefinition{first})
	if err != nil {
		t.Fatalf("digest first: %v", err)
	}
	secondDigest, err := Digest([]messages.ToolDefinition{second})
	if err != nil || firstDigest == secondDigest {
		t.Fatalf("schema-only change kept digest %s (%v)", firstDigest, err)
	}
	if merged := Merge(nil, []messages.ToolDefinition{second, first}); len(merged) != 2 {
		t.Fatalf("merge without base = %#v", merged)
	}
}

func TestRejectsStaleGenerationNotifications(t *testing.T) {
	base := []messages.ToolDefinition{definition(stableTool, "stable")}
	page := []messages.ToolDefinition{definition(pageToolName, "page")}
	publisher := unitPublisher(base, concat(base, page), fixed(page, nil), nil)
	definitions := Merge(base, page)
	digest, err := Digest(definitions)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	published := target{browserID: browserID, targetID: tabID, generation: publishedGeneration, sequence: publishedSequence}
	publisher.commit(published, true, definitions, digest, true)
	if publisher.consumeEvent(catalogEventAt(publishedGeneration-1, publishedSequence+1)) || publisher.hasPending {
		t.Fatal("stale generation notification was accepted")
	}
	state := publisher.State()
	if state.LastSuccessfulGeneration != publishedGeneration || state.LastSuccessfulEventSequence != publishedSequence || state.LatestEventSequence != publishedSequence+1 {
		t.Fatalf("stale notification changed state = %#v", state)
	}
	newer := catalogEventAt(publishedGeneration+1, publishedSequence+2)
	newer.Type = sessionturn.BrowserEventGenerationChanged
	if !publisher.consumeEvent(newer) {
		t.Fatal("newer generation notification was not accepted")
	}
	if publisher.consumeEvent(catalogEventAt(publishedGeneration, publishedSequence+3)) || publisher.pending.generation != publishedGeneration+1 {
		t.Fatalf("older generation replaced newer pending work: %#v", publisher.pending)
	}
}

func TestZeroGenerationInheritsTargetGeneration(t *testing.T) {
	publisher := unitPublisher(nil, nil, fixed(nil, nil), nil)
	publisher.commit(target{browserID: browserID, targetID: tabID, generation: publishedGeneration}, true, nil, "", true)
	if !publisher.consumeEvent(catalogEventAt(0, 1)) || publisher.pending.generation != publishedGeneration {
		t.Fatalf("inherited from last success = %#v", publisher.pending)
	}
	publisher.pending.generation = publishedGeneration + 1
	if !publisher.consumeEvent(catalogEventAt(0, 2)) || publisher.pending.generation != publishedGeneration+1 {
		t.Fatalf("inherited from pending = %#v", publisher.pending)
	}
	other := catalogEventAt(0, 3)
	other.TargetID = "other"
	if !publisher.consumeEvent(other) || publisher.pending.generation != 0 {
		t.Fatalf("unrelated target = %#v", publisher.pending)
	}
}

func TestRefreshFailureRetainsLastSuccessfulState(t *testing.T) {
	base := []messages.ToolDefinition{definition(stableTool, "stable")}
	pageA := []messages.ToolDefinition{definition("page_a", "page A")}
	refreshErr := errors.New("catalog transport unavailable " + strings.Repeat("x", longCauseLength))
	publisher := unitPublisher(base, concat(base, pageA), fixed([]messages.ToolDefinition{definition("page_b", "page B")}, refreshErr), nil)
	publisher.consumeEvent(catalogEventAt(2, 7))
	err := publisher.refreshAndPublish(context.Background(), phaseBrokerEvent)
	if !errors.Is(err, sessionturn.ErrPublication) || !errors.Is(err, refreshErr) || len(err.Error()) > maxDiagnosticLength {
		t.Fatalf("refresh error = %v", err)
	}
	state := publisher.State()
	if !reflect.DeepEqual(state.LastSuccessfulDefinitions, Merge(base, pageA)) || state.LatestEventSequence != 7 || state.LastSuccessfulGeneration != 0 || !publisher.hasPending {
		t.Fatalf("failed refresh advanced state = %#v", state)
	}
	if state.Lifecycle != sessionturn.PublicationFailed || state.Err == nil {
		t.Fatalf("failure state = %#v", state)
	}
	if again := publisher.fail("later", 0, nil); !errors.Is(again, refreshErr) {
		t.Fatalf("second failure = %v, want the first recorded error", again)
	}
	publisher.setLifecycle(sessionturn.PublicationStopped)
	if publisher.State().Lifecycle != sessionturn.PublicationFailed {
		t.Fatal("failed lifecycle was overwritten")
	}
	if err := <-publisher.Errors(); !errors.Is(err, refreshErr) {
		t.Fatalf("reported error = %v", err)
	}
}

func TestWallTimerFiresAfterReset(t *testing.T) {
	timer := wallTimers{}.NewTimer(waitBound)
	if !timer.Stop() {
		t.Fatal("new wall timer was not active")
	}
	timer.Reset(0)
	select {
	case <-timer.C():
	case <-time.After(waitBound):
		t.Fatal("reset wall timer did not fire")
	}
}

func TestPublishFailuresAndCancellation(t *testing.T) {
	page := []messages.ToolDefinition{definition(pageToolName, "page")}
	sendErr := errors.New("session.update rejected")
	failing := unitPublisher(nil, nil, fixed(page, nil), func(context.Context, []messages.ToolDefinition) error { return sendErr })
	var publicationErr *sessionturn.PublicationError
	if err := failing.refreshAndPublish(context.Background(), phaseBrokerEvent); !errors.As(err, &publicationErr) || publicationErr.Phase != phaseBrokerSend || !errors.Is(err, sendErr) {
		t.Fatalf("publish failure = %v", err)
	}
	missing := unitPublisher(nil, nil, fixed(page, nil), nil)
	if err := missing.refreshAndPublish(context.Background(), phaseBrokerEvent); !errors.Is(err, errNoPublisher) {
		t.Fatalf("missing publisher = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceledRefresh := unitPublisher(nil, nil, fixed(nil, context.Canceled), nil)
	if err := canceledRefresh.refreshAndPublish(ctx, phaseBrokerEvent); !errors.Is(err, context.Canceled) || canceledRefresh.State().Lifecycle == sessionturn.PublicationFailed {
		t.Fatalf("canceled refresh = %v", err)
	}
	canceledPublish := unitPublisher(nil, nil, fixed(page, nil), func(context.Context, []messages.ToolDefinition) error { return context.Canceled })
	if err := canceledPublish.refreshAndPublish(ctx, phaseBrokerEvent); !errors.Is(err, context.Canceled) || canceledPublish.State().Lifecycle == sessionturn.PublicationFailed {
		t.Fatalf("canceled publish = %v", err)
	}
	if err := unitPublisher(nil, nil, nil, nil).fail("phase", 1, nil); !errors.Is(err, errUnknownFailure) {
		t.Fatalf("nil cause = %v", err)
	}
}
