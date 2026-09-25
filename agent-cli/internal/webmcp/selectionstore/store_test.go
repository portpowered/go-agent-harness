package selectionstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

const testInstanceID = "incarnation-0123456789abcdef01234567"

func validSelection() Selection {
	return Selection{
		EndpointID:        "endpoint-a",
		BrowserID:         "browser-a",
		BrowserInstanceID: testInstanceID,
		TargetID:          "target-a",
		Origin:            "HTTPS://Page.Test/path?secret=1",
		ContinuityMarker:  "document-a",
		Generation:        3,
	}
}

func TestFileStoreRoundTripsRedactedUserOnlySelection(t *testing.T) {
	store := NewFileStore(t.TempDir())
	if filepath.Base(store.Path) != FileName {
		t.Fatalf("store path = %q", store.Path)
	}
	empty, err := store.Load()
	if err != nil || empty.BrowserID != "" {
		t.Fatalf("missing file load = %+v/%v", empty, err)
	}
	if err := store.Save(validSelection()); err != nil {
		t.Fatalf("save: %v", err)
	}
	info, err := os.Stat(store.Path)
	if err != nil || info.Mode().Perm() != fileMode {
		t.Fatalf("selection file mode = %v/%v", info, err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Version != Version || loaded.Origin != "https://page.test" || loaded.SelectedAt.IsZero() || loaded.Generation != 3 {
		t.Fatalf("loaded selection = %+v", loaded)
	}
}

func TestFileStoreRejectsInvalidRecords(t *testing.T) {
	store := NewFileStore(t.TempDir())
	mutations := map[string]func(*Selection){
		"version":    func(s *Selection) { s.Version = 2 },
		"ids":        func(s *Selection) { s.TargetID = "" },
		"instance":   func(s *Selection) { s.BrowserInstanceID = "incarnation-XYZ" },
		"marker":     func(s *Selection) { s.ContinuityMarker = "ws://secret/path" },
		"marker_len": func(s *Selection) { s.ContinuityMarker = strings.Repeat("a", maxContinuityMarkerLength+1) },
	}
	for name, mutate := range mutations {
		selection := validSelection()
		mutate(&selection)
		if err := store.Save(selection); err == nil {
			t.Fatalf("%s: Save accepted invalid selection %+v", name, selection)
		}
	}
	if err := (&FileStore{}).Save(validSelection()); err == nil {
		t.Fatal("pathless store saved")
	}
	if _, err := (*FileStore)(nil).Load(); err == nil {
		t.Fatal("nil store loaded")
	}
}

func TestFileStoreRejectsMalformedFiles(t *testing.T) {
	for name, content := range map[string]string{
		"unknown field": `{"version":1,"browser_id":"b","target_id":"t","extra":1}`,
		"two values":    `{"version":1,"browser_id":"b","target_id":"t"} {}`,
		"trailing junk": `{"version":1,"browser_id":"b","target_id":"t"} ]`,
		"not json":      `nope`,
		"bad version":   `{"version":9,"browser_id":"b","target_id":"t"}`,
	} {
		store := NewFileStore(t.TempDir())
		if err := os.WriteFile(store.Path, []byte(content), fileMode); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		if _, err := store.Load(); err == nil {
			t.Fatalf("%s: Load accepted %q", name, content)
		}
	}
}

func TestSafeOriginFallsBackToBoundedPrintableText(t *testing.T) {
	if got := safeOrigin("plain\x01text?query#frag"); got != "plaintext" {
		t.Fatalf("safeOrigin fallback = %q", got)
	}
	if got := safeOrigin(strings.Repeat("a", maxFallbackOriginLength+10)); len(got) != maxFallbackOriginLength {
		t.Fatalf("safeOrigin length = %d", len(got))
	}
}

type memoryStore struct {
	selection Selection
	loadErr   error
	saved     []Selection
}

func (s *memoryStore) Load() (Selection, error) { return s.selection, s.loadErr }

func (s *memoryStore) Save(selection Selection) error {
	s.saved = append(s.saved, selection)
	return nil
}

func TestForDiscoveryAdaptsStoresAndPassesOtherValuesThrough(t *testing.T) {
	if ForDiscovery(nil) != nil {
		t.Fatal("nil store was adapted")
	}
	passthrough := discovery.NewMemorySelectionStore()
	if ForDiscovery(passthrough) != passthrough {
		t.Fatal("discovery store was not passed through")
	}
	backing := &memoryStore{}
	adapted, ok := ForDiscovery(backing).(discovery.SelectionStore)
	if !ok {
		t.Fatal("Store was not adapted to discovery persistence")
	}
	ctx := context.Background()
	if _, err := adapted.Load(ctx); !errors.Is(err, discovery.ErrSelectionNotFound) {
		t.Fatalf("empty load error = %v", err)
	}
	selectedAt := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	record := discovery.PersistedSelection{Version: 1, BrowserID: "browser-a", TargetID: "target-a", Generation: 2, SelectedAt: selectedAt}
	if err := adapted.Save(ctx, record); err != nil {
		t.Fatalf("save: %v", err)
	}
	backing.selection = backing.saved[0]
	loaded, err := adapted.Load(ctx)
	if err != nil || loaded.BrowserID != "browser-a" || loaded.Generation != 2 || !loaded.SelectedAt.Equal(selectedAt) {
		t.Fatalf("round trip = %+v/%v", loaded, err)
	}
	backing.loadErr = errors.New("disk")
	if _, err := adapted.Load(ctx); err == nil {
		t.Fatal("load error was swallowed")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := adapted.Save(canceled, record); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled save error = %v", err)
	}
	if _, err := adapted.Load(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled load error = %v", err)
	}
	empty := discoveryStore{}
	if _, err := empty.Load(ctx); !errors.Is(err, discovery.ErrSelectionNotFound) {
		t.Fatalf("storeless load = %v", err)
	}
	if err := empty.Save(ctx, record); err == nil {
		t.Fatal("storeless save succeeded")
	}
}
