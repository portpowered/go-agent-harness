package discovery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type persistenceBrowserIDMapper struct{ id string }

func (m persistenceBrowserIDMapper) BrowserID(BrowserIdentity) string { return m.id }

type atomicPersistenceStore struct {
	record       PersistedSelection
	atomicWrites int
	normalWrites int
	hasRecord    bool
	err          error
}

type bytePersistenceStore struct {
	data         []byte
	atomicWrites int
}

func (s *bytePersistenceStore) Load(context.Context) ([]byte, error) {
	if len(s.data) == 0 {
		return nil, ErrSelectionNotFound
	}
	return append([]byte(nil), s.data...), nil
}

func (s *bytePersistenceStore) SaveAtomic(_ context.Context, data []byte) error {
	s.data = append(s.data[:0], data...)
	s.atomicWrites++
	return nil
}

func (s *atomicPersistenceStore) Load(context.Context) (PersistedSelection, error) {
	if s.err != nil {
		return PersistedSelection{}, s.err
	}
	if !s.hasRecord {
		return PersistedSelection{}, ErrSelectionNotFound
	}
	return s.record, nil
}

func (s *atomicPersistenceStore) Save(context.Context, PersistedSelection) error {
	s.normalWrites++
	return errors.New("non-atomic save must not be selected")
}

func (s *atomicPersistenceStore) SaveAtomic(_ context.Context, record PersistedSelection) error {
	if s.err != nil {
		return s.err
	}
	s.atomicWrites++
	s.record = record
	s.hasRecord = true
	return nil
}

func persistenceBrowser() BrowserCandidate {
	return BrowserCandidate{ID: "browser-persist", Source: SourceConfigured, Loopback: true}
}

func persistenceDescriptor(rawID, title, pageURL, marker string, tools int) TargetDescriptor {
	descriptor := targetDescriptor(rawID, title, pageURL, tools)
	descriptor.ContinuityMarker = marker
	return descriptor
}

func persistenceInputs() (ConnectionInputs, *fakeWebSocketProbe) {
	probe := &fakeWebSocketProbe{version: BrowserVersion{
		Browser:              "Chrome/151",
		ProtocolVersion:      "1.3",
		WebSocketDebuggerURL: "ws://127.0.0.1:9222/devtools/browser/browser-secret",
	}}
	return ConnectionInputs{
		ConfiguredSources: []ConfiguredSource{StaticConfiguredSource{
			SourceName: "persisted-test",
			Value:      Endpoint{BrowserWSEndpoint: "ws://127.0.0.1:9222/devtools/browser/current?token=secret"},
		}},
	}, probe
}

func persistenceService(store any, descriptors *[]TargetDescriptor, probe WebSocketProbe) *Service {
	return New(Options{
		SelectionStore: store,
		IDMapper:       persistenceBrowserIDMapper{id: persistenceBrowser().ID},
		WebSocketProbe: probe,
		TargetLister: TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
			return append([]TargetDescriptor(nil), (*descriptors)...), nil
		}),
		Clock: selectionClock{now: time.Date(2026, 8, 28, 13, 0, 0, 0, time.UTC)},
	})
}

func persistedTargetID(rawID string) string {
	return (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: persistenceBrowser().ID, RawID: rawID})
}

func persistInitialSelection(t *testing.T, store any, descriptors *[]TargetDescriptor) (Selection, string) {
	t.Helper()
	browser := persistenceBrowser()
	targetID := persistedTargetID((*descriptors)[0].ID)
	service := persistenceService(store, descriptors, nil)
	selected, err := service.SelectTarget(context.Background(), browser, targetID)
	if err != nil {
		t.Fatalf("initial selection: %v", err)
	}
	return selected, targetID
}

func TestPersistedSelectionSurvivesRestartWithExactContinuity(t *testing.T) {
	store := NewMemorySelectionStore()
	descriptors := []TargetDescriptor{
		persistenceDescriptor("raw-page", "Restart", "https://restart.test/path?secret=removed#fragment", "document-a", 2),
	}
	selected, targetID := persistInitialSelection(t, store, &descriptors)
	if store.Writes() != 1 {
		t.Fatalf("initial writes = %d, want one atomic selection write", store.Writes())
	}
	if selected.Target.ContinuityMarker == "" {
		t.Fatal("initial selection has no continuity marker")
	}
	for _, secret := range []string{"ws://", "token=secret", "secret=removed", "#fragment", "browser-secret"} {
		if strings.Contains(string(store.Bytes()), secret) {
			t.Fatalf("persisted bytes contain %q: %s", secret, store.Bytes())
		}
	}
	record, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("stored record = %#v err=%v", record, err)
	}
	if record.Version != SelectionPersistenceVersion || record.EndpointID != selected.BrowserID || record.BrowserID != selected.BrowserID || record.TargetID != targetID || record.Origin != "https://restart.test" || record.Generation != 1 || !record.SelectedAt.Equal(selected.SelectedAt) {
		t.Fatalf("stored record = %#v", record)
	}

	inputs, probe := persistenceInputs()
	service := NewService(Options{
		SelectionStore: store,
		IDMapper:       persistenceBrowserIDMapper{id: persistenceBrowser().ID},
		WebSocketProbe: probe,
		TargetLister: TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
			return append([]TargetDescriptor(nil), descriptors...), nil
		}),
		Clock: selectionClock{now: time.Date(2026, 8, 28, 13, 0, 0, 0, time.UTC)},
	})
	restored, err := service.Reconnect(context.Background(), inputs, ReconnectOptions{AutoSelect: AutoSelectPersisted})
	if err != nil {
		t.Fatalf("persisted reconnect: %v", err)
	}
	if restored.BrowserID != selected.BrowserID || restored.TargetID != selected.TargetID || restored.Generation != 2 || !restored.SelectedAt.Equal(selected.SelectedAt) || !restored.Context().Ready {
		t.Fatalf("restored selection = %#v context=%#v", restored, restored.Context())
	}
	if store.Writes() != 2 {
		t.Fatalf("reconnect writes = %d, want updated current generation", store.Writes())
	}
	if got, ok := service.Selected(); !ok || got.Generation != restored.Generation {
		t.Fatalf("service current selection = %#v ok=%v", got, ok)
	}
}

func TestPersistedSelectionStaleAndUnsupportedFailuresNeverFallback(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func([]TargetDescriptor) []TargetDescriptor
		wantCode   Code
		wantReason string
	}{
		{
			name: "missing target",
			mutate: func([]TargetDescriptor) []TargetDescriptor {
				return nil
			},
			wantCode:   CodeStaleSelection,
			wantReason: "target_missing_after_reconnect",
		},
		{
			name: "changed origin",
			mutate: func(descriptors []TargetDescriptor) []TargetDescriptor {
				descriptors[0].URL = "https://other.test/replaced"
				return descriptors
			},
			wantCode:   CodeStaleSelection,
			wantReason: "origin_changed",
		},
		{
			name: "changed continuity",
			mutate: func(descriptors []TargetDescriptor) []TargetDescriptor {
				descriptors[0].ContinuityMarker = "document-b"
				return descriptors
			},
			wantCode:   CodeStaleSelection,
			wantReason: "continuity_changed",
		},
		{
			name: "unsupported target",
			mutate: func(descriptors []TargetDescriptor) []TargetDescriptor {
				unsupported := false
				descriptors[0].WebMCPSupported = &unsupported
				return descriptors
			},
			wantCode: CodeUnsupportedWebMCP,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemorySelectionStore()
			initial := []TargetDescriptor{persistenceDescriptor("raw-page", "Page", "https://restart.test", "document-a", 1)}
			selected, _ := persistInitialSelection(t, store, &initial)
			descriptors := test.mutate(append([]TargetDescriptor(nil), initial...))
			inputs, probe := persistenceInputs()
			service := persistenceService(store, &descriptors, probe)
			_, err := service.Reconnect(context.Background(), inputs, ReconnectOptions{AutoSelect: AutoSelectPersisted})
			discoveryErr := assertDiscoveryError(t, err, test.wantCode)
			if test.wantReason != "" && discoveryErr.Details["reason"] != test.wantReason {
				t.Fatalf("failure details = %#v, want reason %q", discoveryErr.Details, test.wantReason)
			}
			if _, ok := service.Selected(); ok {
				t.Fatal("stale/unsupported persisted state created a live selection")
			}
			if store.Writes() != 1 {
				t.Fatalf("stale/unsupported reconnect overwrote record: writes=%d selected=%#v", store.Writes(), selected)
			}
		})
	}
}

func TestPersistedSelectionRejectsCorruptAndUnknownState(t *testing.T) {
	for _, raw := range []string{
		`{"version":2,"endpoint_id":"browser-persist","browser_id":"browser-persist","target_id":"target-x","origin":"https://state.test","continuity_marker":"continuity-x","generation":1,"selected_at":"2026-08-28T13:00:00Z"}`,
		`{"version":1,"endpoint_id":"browser-persist","browser_id":"browser-persist","target_id":"target-x","origin":"https://state.test","continuity_marker":"continuity-x","generation":1`,
		`{"version":1,"endpoint_id":"browser-persist","browser_id":"browser-persist","target_id":"target-x","origin":"https://state.test","continuity_marker":"continuity-x","generation":1,"selected_at":"2026-08-28T13:00:00Z","raw_endpoint":"ws://secret"}`,
	} {
		store := NewMemorySelectionStore()
		store.SetBytes([]byte(raw))
		inputs, probe := persistenceInputs()
		service := persistenceService(store, new([]TargetDescriptor), probe)
		_, err := service.Reconnect(context.Background(), inputs, ReconnectOptions{AutoSelect: AutoSelectPersisted})
		invalid := assertDiscoveryError(t, err, CodeBrowserProtocolInvalid)
		if invalid.Details["phase"] != "selection_state" {
			t.Fatalf("invalid state details = %#v", invalid.Details)
		}
		if store.Writes() != 0 {
			t.Fatalf("invalid state was overwritten: writes=%d", store.Writes())
		}
	}
}

func TestExplicitReconnectSelectionOverridesPersistedTarget(t *testing.T) {
	store := NewMemorySelectionStore()
	descriptors := []TargetDescriptor{
		persistenceDescriptor("raw-a", "A", "https://select.test/a", "document-a", 1),
		persistenceDescriptor("raw-b", "B", "https://select.test/b", "document-b", 1),
	}
	_, firstID := persistInitialSelection(t, store, &descriptors)
	secondID := persistedTargetID("raw-b")
	if firstID == secondID {
		t.Fatal("test targets unexpectedly share an ID")
	}
	inputs, probe := persistenceInputs()
	service := persistenceService(store, &descriptors, probe)
	selected, err := service.Reconnect(context.Background(), inputs, ReconnectOptions{
		BrowserID:  persistenceBrowser().ID,
		TargetID:   secondID,
		AutoSelect: AutoSelectPersisted,
	})
	if err != nil {
		t.Fatalf("explicit reconnect selection: %v", err)
	}
	if selected.TargetID != secondID || selected.Title != "B" {
		t.Fatalf("explicit target did not win over persisted target: %#v", selected)
	}
	record, err := service.LoadSelection(context.Background())
	if err != nil || record.TargetID != secondID {
		t.Fatalf("persisted override record = %#v err=%v", record, err)
	}
}

func TestAutoSelectionModesAreFailClosedAndPersistenceCanBeDisabled(t *testing.T) {
	store := NewMemorySelectionStore()
	descriptors := []TargetDescriptor{
		persistenceDescriptor("raw-a", "A", "https://select.test/a", "document-a", 1),
		persistenceDescriptor("raw-b", "B", "https://select.test/b", "document-b", 1),
	}
	inputs, probe := persistenceInputs()
	service := persistenceService(store, &descriptors, probe)
	_, err := service.Reconnect(context.Background(), inputs, ReconnectOptions{AutoSelect: AutoSelectSingle})
	assertDiscoveryError(t, err, CodeAmbiguousTab)
	if _, ok := service.Selected(); ok {
		t.Fatal("ambiguous auto-selection created a selection")
	}

	disabled := false
	disabledService := New(Options{
		SelectionStore:     store,
		PersistenceEnabled: &disabled,
		TargetLister: TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
			return []TargetDescriptor{descriptors[0]}, nil
		}),
	})
	if _, err := disabledService.SelectTarget(context.Background(), persistenceBrowser(), persistedTargetID("raw-a")); err != nil {
		t.Fatalf("selection with disabled persistence: %v", err)
	}
	if disabledService.PersistenceEnabled() || store.Writes() != 0 {
		t.Fatalf("disabled persistence state = %v writes=%d", disabledService.PersistenceEnabled(), store.Writes())
	}

	oneStore := NewMemorySelectionStore()
	oneDescriptor := []TargetDescriptor{persistenceDescriptor("raw-only", "Only", "https://single.test", "document-only", 0)}
	oneService := persistenceService(oneStore, &oneDescriptor, probe)
	oneSelected, err := oneService.Reconnect(context.Background(), inputs, ReconnectOptions{AutoSelect: AutoSelectSingle})
	if err != nil || oneSelected.TargetID != persistedTargetID("raw-only") {
		t.Fatalf("single auto-selection = %#v err=%v", oneSelected, err)
	}
	if _, err := oneService.Reconnect(context.Background(), inputs, ReconnectOptions{AutoSelect: AutoSelectMode("unknown")}); err == nil {
		t.Fatal("unknown auto-selection mode returned nil error")
	}
}

func TestSelectionUsesAtomicPersistenceOperation(t *testing.T) {
	store := &atomicPersistenceStore{}
	descriptors := []TargetDescriptor{persistenceDescriptor("raw-page", "Page", "https://atomic.test", "document-a", 1)}
	selected, _ := persistInitialSelection(t, store, &descriptors)
	if store.atomicWrites != 1 || store.normalWrites != 0 {
		t.Fatalf("persistence calls = atomic %d normal %d", store.atomicWrites, store.normalWrites)
	}
	if store.record.TargetID != selected.TargetID || store.record.ContinuityMarker != selected.Target.ContinuityMarker {
		t.Fatalf("atomic record = %#v selection=%#v", store.record, selected)
	}
}

func TestSelectionPersistenceFailuresAreClassifiedAndDoNotCommit(t *testing.T) {
	descriptors := []TargetDescriptor{persistenceDescriptor("raw-page", "Page", "https://failure.test", "document-a", 1)}
	targetID := persistedTargetID("raw-page")
	writeFailure := &atomicPersistenceStore{}
	writeFailure.err = errors.New("disk path contains credentials and must stay private")
	service := persistenceService(writeFailure, &descriptors, nil)
	_, err := service.SelectTarget(context.Background(), persistenceBrowser(), targetID)
	persistenceErr := assertDiscoveryError(t, err, CodeBrowserProtocolInvalid)
	if persistenceErr.Details["phase"] != "selection_save" || persistenceErr.Details["reason_code"] != "save_failed" {
		t.Fatalf("write failure = %#v", persistenceErr.Details)
	}
	if _, ok := service.Selected(); ok || writeFailure.atomicWrites != 0 {
		t.Fatalf("failed persistence committed state: selected=%v writes=%d", ok, writeFailure.atomicWrites)
	}

	unsupportedStoreService := New(Options{
		SelectionStore: struct{}{},
		TargetLister: TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
			return descriptors, nil
		}),
	})
	_, err = unsupportedStoreService.SelectTarget(context.Background(), persistenceBrowser(), targetID)
	unsupported := assertDiscoveryError(t, err, CodeBrowserProtocolInvalid)
	if unsupported.Details["phase"] != "selection_save" || unsupported.Details["reason_code"] != "store_unavailable" {
		t.Fatalf("unsupported store = %#v", unsupported.Details)
	}
}

func TestPersistenceLoadAndReconnectAliases(t *testing.T) {
	store := NewMemorySelectionStore()
	descriptors := []TargetDescriptor{persistenceDescriptor("raw-page", "Page", "https://alias.test", "document-a", 1)}
	selected, _ := persistInitialSelection(t, store, &descriptors)
	inputs, probe := persistenceInputs()

	service := persistenceService(store, &descriptors, probe)
	restored, err := service.RestoreSelection(context.Background(), inputs, ReconnectOptions{AutoSelect: AutoSelectPersisted})
	if err != nil || restored.TargetID != selected.TargetID {
		t.Fatalf("RestoreSelection = %#v err=%v", restored, err)
	}
	service = persistenceService(store, &descriptors, probe)
	if restored, err = service.ReconnectSelection(context.Background(), inputs, ReconnectOptions{Mode: AutoSelectPersisted}); err != nil || restored.TargetID != selected.TargetID {
		t.Fatalf("ReconnectSelection = %#v err=%v", restored, err)
	}
	service = persistenceService(store, &descriptors, probe)
	if restored, err = service.RestorePersistedSelection(context.Background(), inputs); err != nil || restored.TargetID != selected.TargetID {
		t.Fatalf("RestorePersistedSelection = %#v err=%v", restored, err)
	}

	loaded, present, err := service.LoadPersistedSelection(context.Background())
	if err != nil || !present || loaded.TargetID != selected.TargetID {
		t.Fatalf("LoadPersistedSelection = %#v present=%v err=%v", loaded, present, err)
	}
	if _, err := service.LoadSelection(context.Background()); err != nil {
		t.Fatalf("LoadSelection() error = %v", err)
	}
	empty := New(Options{SelectionStore: NewMemorySelectionStore()})
	if _, present, err := empty.LoadPersistedSelection(context.Background()); err != nil || present {
		t.Fatalf("empty LoadPersistedSelection = present=%v err=%v", present, err)
	}
	if _, err := empty.LoadSelection(context.Background()); !errors.Is(err, ErrSelectionNotFound) {
		t.Fatalf("empty LoadSelection error = %v, want ErrSelectionNotFound", err)
	}
	var nilService *Service
	if nilService.PersistenceEnabled() {
		t.Fatal("nil service reported enabled persistence")
	}
}

func TestBytePersistenceStoreReceivesOnlyValidatedJSON(t *testing.T) {
	store := &bytePersistenceStore{}
	descriptors := []TargetDescriptor{persistenceDescriptor("raw-page", "Page", "https://bytes.test/path?secret=removed#fragment", "document-a", 1)}
	selected, _ := persistInitialSelection(t, store, &descriptors)
	if store.atomicWrites != 1 {
		t.Fatalf("atomic byte writes = %d, want one", store.atomicWrites)
	}
	for _, secret := range []string{"ws://", "secret=removed", "#fragment", "document-a"} {
		if strings.Contains(string(store.data), secret) {
			t.Fatalf("byte store contains %q: %s", secret, store.data)
		}
	}
	service := persistenceService(store, &descriptors, nil)
	record, present, err := service.LoadPersistedSelection(context.Background())
	if err != nil || !present || record.TargetID != selected.TargetID || record.Origin != "https://bytes.test" {
		t.Fatalf("loaded byte record = %#v present=%v err=%v", record, present, err)
	}
}

func TestExplicitReconnectConflictCanBeRequiredWithoutOverwritingRecord(t *testing.T) {
	store := NewMemorySelectionStore()
	descriptors := []TargetDescriptor{
		persistenceDescriptor("raw-a", "A", "https://conflict.test/a", "document-a", 1),
		persistenceDescriptor("raw-b", "B", "https://conflict.test/b", "document-b", 1),
	}
	_, _ = persistInitialSelection(t, store, &descriptors)
	secondID := persistedTargetID("raw-b")
	inputs, probe := persistenceInputs()
	service := persistenceService(store, &descriptors, probe)
	_, err := service.Reconnect(context.Background(), inputs, ReconnectOptions{
		BrowserID:               persistenceBrowser().ID,
		TargetID:                secondID,
		RejectPersistedConflict: true,
	})
	conflict := assertDiscoveryError(t, err, CodeStaleSelection)
	if conflict.Details["reason"] != "explicit_selection_conflict" || store.Writes() != 1 {
		t.Fatalf("conflict = %#v writes=%d", conflict.Details, store.Writes())
	}
	if _, ok := service.Selected(); ok {
		t.Fatal("conflicting explicit reconnect created a selection")
	}
}

func TestLifecycleRefreshUpdatesPersistedContinuityAndGeneration(t *testing.T) {
	store := NewMemorySelectionStore()
	descriptors := []TargetDescriptor{persistenceDescriptor("raw-page", "Page", "https://lifecycle-persist.test", "document-a", 1)}
	service := persistenceService(store, &descriptors, nil)
	targetID := persistedTargetID(descriptors[0].ID)
	selected, err := service.SelectTarget(context.Background(), persistenceBrowser(), targetID)
	if err != nil {
		t.Fatalf("selection before lifecycle: %v", err)
	}
	oldMarker := selected.Target.ContinuityMarker
	descriptors[0].ContinuityMarker = "document-b"
	if _, err := service.HandleLifecycle(context.Background(), LifecycleEvent{
		Type:       LifecycleDocumentReplaced,
		BrowserID:  selected.BrowserID,
		TargetID:   selected.TargetID,
		DocumentID: "document-b",
		EventID:    "document-replaced-1",
	}); err != nil {
		t.Fatalf("persisted lifecycle refresh: %v", err)
	}
	record, err := service.LoadSelection(context.Background())
	if err != nil || record.Generation != 2 {
		t.Fatalf("lifecycle persisted record = %#v err=%v", record, err)
	}
	if record.TargetID != targetID || record.ContinuityMarker == "" || record.ContinuityMarker == oldMarker || store.Writes() != 2 {
		t.Fatalf("lifecycle persisted identity = %#v writes=%d", record, store.Writes())
	}
}
