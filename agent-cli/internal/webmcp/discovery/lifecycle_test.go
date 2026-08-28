package discovery

import (
	"context"
	"errors"
	"testing"
)

type lifecycleTargetLister struct {
	descriptors []TargetDescriptor
	calls       int
}

func (l *lifecycleTargetLister) List(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
	l.calls++
	return append([]TargetDescriptor(nil), l.descriptors...), nil
}

func TestLifecycleNavigationAdvancesOnceAndRejectsStaleSelection(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-generation", Source: SourceConfigured, Loopback: true}
	lister := &lifecycleTargetLister{descriptors: []TargetDescriptor{
		targetDescriptor("raw-page", "Before", "https://generation.test/old", 1),
	}}
	recorder := &eventRecorder{}
	service := New(Options{TargetLister: lister, EventSink: recorder})
	targetID := (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: browser.ID, RawID: "raw-page"})

	selected, err := service.SelectTarget(context.Background(), browser, targetID)
	if err != nil {
		t.Fatalf("initial selection: %v", err)
	}
	if selected.Generation != 1 {
		t.Fatalf("initial generation = %d, want 1", selected.Generation)
	}

	event := LifecycleEvent{
		Type:               LifecycleNavigation,
		BrowserID:          browser.ID,
		TargetID:           targetID,
		EventID:            "navigation-1",
		PreviousGeneration: 1,
		Generation:         2,
		Reason:             "top_level_navigation",
	}
	current, err := service.HandleLifecycle(context.Background(), event)
	if err != nil {
		t.Fatalf("navigation: %v", err)
	}
	if current.Generation != 2 || current.Context().Generation != 2 || !current.Context().Ready {
		t.Fatalf("current selection after navigation = %#v context=%#v", current, current.Context())
	}

	duplicate, err := service.HandleLifecycle(context.Background(), event)
	if err != nil {
		t.Fatalf("duplicate navigation: %v", err)
	}
	if duplicate.Generation != 2 {
		t.Fatalf("duplicate navigation generation = %d, want 2", duplicate.Generation)
	}
	generationEvents := 0
	var generationEvent Event
	for _, observed := range recorder.events {
		if observed.Type != EventPageGenerationChanged {
			continue
		}
		generationEvents++
		generationEvent = observed
	}
	if generationEvents != 1 {
		t.Fatalf("generation events = %d, want one", generationEvents)
	}
	if generationEvent.Generation != 2 || generationEvent.Payload["previous_generation"] != uint64(1) || generationEvent.Payload["current_generation"] != uint64(2) {
		t.Fatalf("generation event = %#v", generationEvent)
	}

	_, err = service.ValidateSelection(context.Background(), selected)
	stale := assertDiscoveryError(t, err, CodeStaleSelection)
	if stale.Details["browser_id"] != browser.ID || stale.Details["target_id"] != targetID || stale.Details["selected_generation"] != uint64(1) || stale.Details["reason"] != "generation_changed" {
		t.Fatalf("stale selection details = %#v", stale.Details)
	}
	if lister.calls != 2 {
		t.Fatalf("target refresh calls = %d, want initial selection plus one lifecycle refresh", lister.calls)
	}
}

func TestLifecycleForUnselectedTargetDoesNotChangeActiveSelection(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-unselected", Source: SourceConfigured, Loopback: true}
	lister := &lifecycleTargetLister{descriptors: []TargetDescriptor{
		targetDescriptor("raw-a", "A", "https://a.test", 1),
		targetDescriptor("raw-b", "B", "https://b.test", 1),
	}}
	service := New(Options{TargetLister: lister})
	aID := (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: browser.ID, RawID: "raw-a"})
	bID := (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: browser.ID, RawID: "raw-b"})

	selected, err := service.SelectTarget(context.Background(), browser, aID)
	if err != nil {
		t.Fatalf("initial selection: %v", err)
	}
	if _, err := service.HandleLifecycle(context.Background(), LifecycleEvent{
		Type:      LifecycleNavigation,
		BrowserID: browser.ID,
		TargetID:  bID,
		EventID:   "unselected-navigation-1",
	}); err != nil {
		t.Fatalf("unselected navigation: %v", err)
	}
	current, ok := service.Selected()
	if !ok || current.BrowserID != selected.BrowserID || current.TargetID != selected.TargetID || current.Generation != selected.Generation {
		t.Fatalf("active selection changed after unselected event: current=%#v ok=%v selected=%#v", current, ok, selected)
	}
	state := service.targets[browser.ID][bID]
	if state.generation != 2 {
		t.Fatalf("unselected target generation = %d, want 2", state.generation)
	}
}

func TestLifecycleTargetCloseInvalidatesAndDetachesOnly(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-close", Source: SourceConfigured, Loopback: true}
	lister := &lifecycleTargetLister{descriptors: []TargetDescriptor{
		targetDescriptor("raw-page", "Page", "https://close.test", 1),
	}}
	detacher := &selectionDetacher{}
	recorder := &eventRecorder{}
	service := New(Options{
		TargetLister: lister,
		EventSink:    recorder,
		TargetAttacher: TargetAttacherFunc(func(context.Context, BrowserCandidate, Target) (TargetDetacher, error) {
			return detacher, nil
		}),
	})
	targetID := (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: browser.ID, RawID: "raw-page"})
	selected, err := service.SelectTarget(context.Background(), browser, targetID)
	if err != nil {
		t.Fatalf("initial selection: %v", err)
	}

	closeEvent := LifecycleEvent{
		Type:      LifecycleTargetClosed,
		BrowserID: browser.ID,
		TargetID:  targetID,
		EventID:   "close-1",
		Reason:    "tab_closed",
	}
	if _, err := service.HandleLifecycle(context.Background(), closeEvent); err != nil {
		t.Fatalf("target close: %v", err)
	}
	if _, ok := service.Selected(); ok {
		t.Fatal("closed target remained selected")
	}
	if detacher.detachCalls != 1 || detacher.closeTarget != 0 || detacher.closeBrowser != 0 || detacher.terminate != 0 || detacher.deleteProfile != 0 {
		t.Fatalf("close lifecycle operations = %#v", *detacher)
	}

	if _, err := service.HandleLifecycle(context.Background(), closeEvent); err != nil {
		t.Fatalf("duplicate target close: %v", err)
	}
	if detacher.detachCalls != 1 {
		t.Fatalf("duplicate close detach calls = %d, want 1", detacher.detachCalls)
	}
	state := service.targets[browser.ID][targetID]
	if !state.closed || state.generation != 2 {
		t.Fatalf("closed target state = %#v", state)
	}
	_, err = service.ValidateSelection(context.Background(), selected)
	stale := assertDiscoveryError(t, err, CodeStaleSelection)
	if stale.Details["selected_generation"] != uint64(1) || stale.Details["reason"] != "target_closed" {
		t.Fatalf("closed stale details = %#v", stale.Details)
	}

	var detached *Event
	for i := range recorder.events {
		if recorder.events[i].Type == EventTargetDetached {
			detached = &recorder.events[i]
		}
	}
	if detached == nil || detached.Generation != 2 || detached.Payload["reason"] != "tab_closed" || detached.Payload["ownership_mode"] != string(TargetOwnershipExternal) {
		t.Fatalf("detach event = %#v", detached)
	}
}

func TestLifecycleRefreshRejectsMissingWebMCPWithoutReadySelection(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-capability-refresh", Source: SourceConfigured, Loopback: true}
	webmcp := true
	descriptor := targetDescriptor("raw-page", "Page", "https://capability-refresh.test", 1)
	lister := &lifecycleTargetLister{descriptors: []TargetDescriptor{descriptor}}
	service := New(Options{TargetLister: lister})
	targetID := (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: browser.ID, RawID: descriptor.ID})
	selected, err := service.SelectTarget(context.Background(), browser, targetID)
	if err != nil {
		t.Fatalf("initial selection: %v", err)
	}

	webmcp = false
	*lister.descriptors[0].WebMCPSupported = webmcp
	refreshed, err := service.HandleLifecycle(context.Background(), LifecycleEvent{
		Type:      LifecycleDocumentReplaced,
		BrowserID: browser.ID,
		TargetID:  targetID,
		EventID:   "document-replacement-1",
	})
	unsupported := assertDiscoveryError(t, err, CodeUnsupportedWebMCP)
	if unsupported.Details["browser_id"] != browser.ID || unsupported.Details["target_id"] != targetID {
		t.Fatalf("unsupported details = %#v", unsupported.Details)
	}
	if refreshed.Generation != selected.Generation+1 || refreshed.Context().Ready {
		t.Fatalf("unsupported refresh selection = %#v context=%#v", refreshed, refreshed.Context())
	}
	current, ok := service.Selected()
	if !ok || current.Context().Ready {
		t.Fatalf("service preserved a ready selection after unsupported refresh: %#v ok=%v", current, ok)
	}

	_, err = service.ValidateSelectionGeneration(context.Background(), SelectionValidationRequest{
		BrowserID:  browser.ID,
		TargetID:   targetID,
		Generation: refreshed.Generation,
	})
	assertDiscoveryError(t, err, CodeUnsupportedWebMCP)
	_, err = service.ValidateSelection(context.Background(), selected)
	assertDiscoveryError(t, err, CodeStaleSelection)
	if !errors.Is(err, ErrStaleSelection) {
		t.Fatalf("stale selection does not match sentinel: %v", err)
	}
}
