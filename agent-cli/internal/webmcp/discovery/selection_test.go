package discovery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type selectionClock struct{ now time.Time }

func (c selectionClock) Now() time.Time { return c.now }

type selectionActivator struct {
	calls int
	ids   []string
	err   error
}

func (a *selectionActivator) Activate(_ context.Context, _ BrowserCandidate, target Target) error {
	a.calls++
	a.ids = append(a.ids, target.ID)
	return a.err
}

type selectionDetacher struct {
	detachCalls   int
	closeTarget   int
	closeBrowser  int
	terminate     int
	deleteProfile int
}

func (d *selectionDetacher) Detach(context.Context) error {
	d.detachCalls++
	return nil
}

func TestSelectTargetRequiresExactIDsAndPreservesPriorSelectionOnFailure(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-selection", Source: SourceConfigured, Loopback: true}
	descriptors := []TargetDescriptor{
		targetDescriptor("raw-b", "B", "https://b.test/orders?secret=removed", 2),
		targetDescriptor("raw-a", "A", "https://a.test/", 1),
	}
	service := New(Options{
		Clock: selectionClock{now: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)},
		TargetLister: TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
			return append([]TargetDescriptor(nil), descriptors...), nil
		}),
	})

	_, err := service.Select(context.Background(), TargetSelectionRequest{
		Browser:   browser,
		BrowserID: browser.ID,
	})
	ambiguous := assertDiscoveryError(t, err, CodeAmbiguousTab)
	if ids, ok := ambiguous.Details["candidate_target_ids"].([]string); !ok || len(ids) != 2 || !strings.HasPrefix(ids[0], "target-") || ids[0] >= ids[1] {
		t.Fatalf("ambiguous target IDs = %#v", ambiguous.Details["candidate_target_ids"])
	}
	if _, ok := service.Selected(); ok {
		t.Fatal("ambiguous selection mutated current selection")
	}

	targetID := (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: browser.ID, RawID: "raw-a"})
	selected, err := service.SelectTarget(context.Background(), browser, targetID)
	if err != nil {
		t.Fatalf("SelectTarget() error = %v", err)
	}
	if selected.BrowserID != browser.ID || selected.TargetID != targetID || selected.Origin != "https://a.test" || selected.Generation == 0 {
		t.Fatalf("selected context = %#v", selected)
	}
	if selected.Context().Generation != selected.Generation || !selected.Context().Connected || !selected.Context().Ready {
		t.Fatalf("selected page context = %#v", selected.Context())
	}

	_, err = service.SelectTarget(context.Background(), browser, "target-does-not-exist")
	assertDiscoveryError(t, err, CodeNoEligibleTab)
	current, ok := service.Selected()
	if !ok || current.TargetID != selected.TargetID || current.Generation != selected.Generation {
		t.Fatalf("failed exact selection replaced prior state: current=%#v ok=%v prior=%#v", current, ok, selected)
	}
}

func TestSelectTargetActivatesOnlyWhenRequestedAndEmitsSelection(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-activate", Source: SourceConfigured, Loopback: true}
	descriptor := targetDescriptor("raw-tab", "Tab", "https://activate.test/path", 1)
	activator := &selectionActivator{}
	recorder := &eventRecorder{}
	service := New(Options{
		Clock:     selectionClock{now: time.Date(2026, 8, 28, 12, 1, 0, 0, time.UTC)},
		EventSink: recorder,
		Activator: activator,
		TargetLister: TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
			return []TargetDescriptor{descriptor}, nil
		}),
	})
	targetID := (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: browser.ID, RawID: descriptor.ID})

	selected, err := service.SelectTarget(context.Background(), browser, targetID)
	if err != nil {
		t.Fatalf("non-activating selection error = %v", err)
	}
	if activator.calls != 0 {
		t.Fatalf("activation calls for default selection = %d, want 0", activator.calls)
	}

	selected, err = service.SelectTarget(context.Background(), browser, targetID, SelectionOptions{Activate: true, Reason: "operator_request"})
	if err != nil {
		t.Fatalf("activating selection error = %v", err)
	}
	if activator.calls != 1 || len(activator.ids) != 1 || activator.ids[0] != selected.TargetID {
		t.Fatalf("activation calls/targets = %d/%v, want one exact target", activator.calls, activator.ids)
	}
	events := recorder.events
	if len(events) == 0 || events[len(events)-1].Type != EventTargetSelected {
		t.Fatalf("selection event stream = %#v", eventTypes(events))
	}
	event := events[len(events)-1]
	if event.BrowserID != browser.ID || event.TargetID != targetID || event.Generation != selected.Generation || event.Payload["reason"] != "operator_request" {
		t.Fatalf("selection event = %#v", event)
	}
}

func TestSelectTargetKeepsReadySelectionWhenActivationFails(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-headless", Source: SourceConfigured, Loopback: true}
	descriptor := targetDescriptor("raw-tab", "Headless page", "https://headless.test/page", 1)
	activator := &selectionActivator{err: errors.New("foreground activation rejected by headless Chrome")}
	service := New(Options{
		Activator: activator,
		TargetLister: TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
			return []TargetDescriptor{descriptor}, nil
		}),
		TargetAttacher: TargetAttacherFunc(func(context.Context, BrowserCandidate, Target) (TargetDetacher, error) {
			return &selectionDetacher{}, nil
		}),
	})
	targetID := (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: browser.ID, RawID: descriptor.ID})

	selected, err := service.Select(context.Background(), TargetSelectionRequest{
		Browser:   browser,
		BrowserID: browser.ID,
		TargetID:  targetID,
		Activate:  true,
		Reason:    "headless_activation_regression",
	})
	if err != nil {
		t.Fatalf("selection with live activation failure: %v", err)
	}
	if selected.BrowserID != browser.ID || selected.TargetID != targetID || !selected.Context().Connected || !selected.Context().Ready {
		t.Fatalf("selected context = %#v, want exact ready selection", selected.Context())
	}
	if activator.calls != 1 || len(activator.ids) != 1 || activator.ids[0] != targetID {
		t.Fatalf("activation calls/targets = %d/%v, want one exact target", activator.calls, activator.ids)
	}
	if current, ok := service.Selected(); !ok || current.TargetID != targetID || !current.Context().Connected || !current.Context().Ready {
		t.Fatalf("current selection = %#v ok=%v, want retained ready selection", current.Context(), ok)
	}
}

func TestSelectTargetUsesDetachOnlyExternalHandle(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-lifecycle", Source: SourceConfigured, Loopback: true}
	descriptors := []TargetDescriptor{
		targetDescriptor("raw-a", "A", "https://a.test/", 1),
		targetDescriptor("raw-b", "B", "https://b.test/", 1),
	}
	detachers := make(map[string]*selectionDetacher)
	service := New(Options{
		TargetLister: TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
			return append([]TargetDescriptor(nil), descriptors...), nil
		}),
		TargetAttacher: TargetAttacherFunc(func(_ context.Context, _ BrowserCandidate, target Target) (TargetDetacher, error) {
			detacher := &selectionDetacher{}
			detachers[target.ID] = detacher
			return detacher, nil
		}),
	})
	firstID := (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: browser.ID, RawID: "raw-a"})
	secondID := (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: browser.ID, RawID: "raw-b"})

	first, err := service.SelectTarget(context.Background(), browser, firstID)
	if err != nil {
		t.Fatalf("first selection error = %v", err)
	}
	if first.Handle == nil || first.Handle.Ownership() != TargetOwnershipExternal {
		t.Fatalf("first handle = %#v, want external detach-only handle", first.Handle)
	}
	second, err := service.SelectTarget(context.Background(), browser, secondID)
	if err != nil {
		t.Fatalf("replacement selection error = %v", err)
	}
	if got := detachers[firstID].detachCalls; got != 1 {
		t.Fatalf("replacement detach calls = %d, want 1", got)
	}
	if err := second.Handle.Close(); err != nil {
		t.Fatalf("close selected handle: %v", err)
	}
	if err := second.Handle.Close(); err != nil {
		t.Fatalf("close selected handle twice: %v", err)
	}
	detacher := detachers[secondID]
	if detacher.detachCalls != 1 || detacher.closeTarget != 0 || detacher.closeBrowser != 0 || detacher.terminate != 0 || detacher.deleteProfile != 0 {
		t.Fatalf("lifecycle operations = %#v, want one detach and no close/terminate/delete", *detacher)
	}
	if err := service.ReleaseSelection(); err != nil {
		t.Fatalf("release service selection: %v", err)
	}
	if _, ok := service.Selected(); ok {
		t.Fatal("ReleaseSelection left a current selection")
	}
}

func TestSelectTargetDoesNotCommitWhenAttachmentFails(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-attach-failure", Source: SourceConfigured, Loopback: true}
	descriptor := targetDescriptor("raw-tab", "Tab", "https://attach.test/", 1)
	service := New(Options{
		TargetLister: TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
			return []TargetDescriptor{descriptor}, nil
		}),
		TargetAttacher: TargetAttacherFunc(func(context.Context, BrowserCandidate, Target) (TargetDetacher, error) {
			return nil, errors.New("transport details must not escape")
		}),
	})
	targetID := (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: browser.ID, RawID: descriptor.ID})
	_, err := service.SelectTarget(context.Background(), browser, targetID)
	attachErr := assertDiscoveryError(t, err, CodeTargetAttachFailed)
	if attachErr.Details["browser_id"] != browser.ID || attachErr.Details["target_id"] != targetID || strings.Contains(err.Error(), "transport") {
		t.Fatalf("safe attachment error = %#v (%v)", attachErr, err)
	}
	if _, ok := service.Selected(); ok {
		t.Fatal("failed attachment committed a selection")
	}
}

func TestSelectDoesNotInferSoleCachedBrowserWhenBrowserIDIsOmitted(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-cached", Source: SourceConfigured, Loopback: true}
	descriptor := targetDescriptor("raw-page", "Page", "https://cached.test", 1)
	listCalls := 0
	service := New(Options{TargetLister: TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
		listCalls++
		return []TargetDescriptor{descriptor}, nil
	})})
	service.browsers[browser.ID] = browser
	targetID := (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: browser.ID, RawID: descriptor.ID})

	_, err := service.Select(context.Background(), TargetSelectionRequest{TargetID: targetID})
	assertDiscoveryError(t, err, CodeNoEligibleTab)
	if listCalls != 0 {
		t.Fatalf("target list calls = %d, want zero without an exact browser ID", listCalls)
	}
	if _, ok := service.Selected(); ok {
		t.Fatal("omitted browser ID unexpectedly committed a selection")
	}
}
