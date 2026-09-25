package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"sync"
)

// selectionStoreAdapter normalizes the supported fake/store method shapes so
// the service has one atomic typed boundary internally.
type selectionStoreAdapter struct {
	load func(context.Context) (PersistedSelection, error)
	save func(context.Context, PersistedSelection) error
}

func firstConfiguredStore(options Options) any {
	for _, candidate := range []any{
		options.SelectionStore,
		options.Persistence,
		options.SelectionPersistence,
		options.Store,
	} {
		if candidate != nil {
			return candidate
		}
	}
	return nil
}

func adaptSelectionStore(value any) (selectionStoreAdapter, error) {
	if value == nil {
		return selectionStoreAdapter{}, nil
	}
	switch store := value.(type) {
	case interface {
		Load(context.Context) (PersistedSelection, error)
		SaveAtomic(context.Context, PersistedSelection) error
	}:
		return selectionStoreAdapter{load: store.Load, save: store.SaveAtomic}, nil
	case interface {
		Load(context.Context) (PersistedSelection, error)
		Save(context.Context, PersistedSelection) error
	}:
		return selectionStoreAdapter{load: store.Load, save: store.Save}, nil
	case interface {
		LoadSelection(context.Context) (PersistedSelection, error)
		SaveSelectionAtomic(context.Context, PersistedSelection) error
	}:
		return selectionStoreAdapter{load: store.LoadSelection, save: store.SaveSelectionAtomic}, nil
	case interface {
		LoadSelection(context.Context) (PersistedSelection, error)
		SaveSelection(context.Context, PersistedSelection) error
	}:
		return selectionStoreAdapter{load: store.LoadSelection, save: store.SaveSelection}, nil
	case interface {
		Load(context.Context) ([]byte, error)
		SaveAtomic(context.Context, []byte) error
	}:
		return byteSelectionStoreAdapter(store.Load, store.SaveAtomic), nil
	case interface {
		Load(context.Context) ([]byte, error)
		Save(context.Context, []byte) error
	}:
		return byteSelectionStoreAdapter(store.Load, store.Save), nil
	case interface {
		LoadSelection(context.Context) ([]byte, error)
		SaveSelectionAtomic(context.Context, []byte) error
	}:
		return byteSelectionStoreAdapter(store.LoadSelection, store.SaveSelectionAtomic), nil
	case interface {
		LoadSelection(context.Context) ([]byte, error)
		SaveSelection(context.Context, []byte) error
	}:
		return byteSelectionStoreAdapter(store.LoadSelection, store.SaveSelection), nil
	case interface {
		Load() (PersistedSelection, error)
		Save(PersistedSelection) error
	}:
		return selectionStoreAdapter{
			load: func(context.Context) (PersistedSelection, error) { return store.Load() },
			save: func(_ context.Context, record PersistedSelection) error { return store.Save(record) },
		}, nil
	case interface {
		Load() ([]byte, error)
		Save([]byte) error
	}:
		return byteSelectionStoreAdapter(
			func(context.Context) ([]byte, error) { return store.Load() },
			func(_ context.Context, data []byte) error { return store.Save(data) },
		), nil
	default:
		return selectionStoreAdapter{}, errors.New("unsupported webmcp selection store")
	}
}

func byteSelectionStoreAdapter(
	load func(context.Context) ([]byte, error),
	save func(context.Context, []byte) error,
) selectionStoreAdapter {
	return selectionStoreAdapter{
		load: func(ctx context.Context) (PersistedSelection, error) {
			data, err := load(ctx)
			if err != nil {
				return PersistedSelection{}, err
			}
			if len(bytes.TrimSpace(data)) == 0 {
				return PersistedSelection{}, ErrSelectionNotFound
			}
			return decodePersistedSelection(data)
		},
		save: func(ctx context.Context, record PersistedSelection) error {
			data, err := marshalPersistedSelection(record)
			if err != nil {
				return err
			}
			return save(ctx, data)
		},
	}
}

// MemorySelectionStore is a small atomic store for deterministic tests and
// local composition. It retains the exact JSON bytes so tests can inspect the
// persistence boundary for accidental transport-secret leakage.
type MemorySelectionStore struct {
	mu     sync.Mutex
	data   []byte
	writes int
}

// NewMemorySelectionStore constructs an empty in-memory selection store.
func NewMemorySelectionStore() *MemorySelectionStore { return &MemorySelectionStore{} }

// Load returns a validated copy of the stored versioned record.
func (s *MemorySelectionStore) Load(ctx context.Context) (PersistedSelection, error) {
	if err := contextError(ctx); err != nil {
		return PersistedSelection{}, err
	}
	s.mu.Lock()
	data := append([]byte(nil), s.data...)
	s.mu.Unlock()
	if len(bytes.TrimSpace(data)) == 0 {
		return PersistedSelection{}, ErrSelectionNotFound
	}
	return decodePersistedSelection(data)
}

// Save atomically replaces the record after validating its safe shape.
func (s *MemorySelectionStore) Save(ctx context.Context, record PersistedSelection) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	data, err := marshalPersistedSelection(record)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.data = append(s.data[:0], data...)
	s.writes++
	s.mu.Unlock()
	return nil
}

// SaveAtomic is the explicit atomic spelling of Save.
func (s *MemorySelectionStore) SaveAtomic(ctx context.Context, record PersistedSelection) error {
	return s.Save(ctx, record)
}

// Bytes returns a copy of the persisted JSON bytes.
func (s *MemorySelectionStore) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.data...)
}

// Writes reports the number of successful atomic replacements.
func (s *MemorySelectionStore) Writes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

// SetBytes installs raw bytes for corrupt/unknown-version tests.
func (s *MemorySelectionStore) SetBytes(data []byte) {
	s.mu.Lock()
	s.data = append(s.data[:0], data...)
	s.mu.Unlock()
}

// InMemorySelectionStore is a descriptive alias for MemorySelectionStore.
type InMemorySelectionStore = MemorySelectionStore

func marshalPersistedSelection(record PersistedSelection) ([]byte, error) {
	record, err := normalizePersistedSelection(record)
	if err != nil {
		return nil, err
	}
	return json.Marshal(record)
}

func decodePersistedSelection(data []byte) (PersistedSelection, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record PersistedSelection
	if err := decoder.Decode(&record); err != nil {
		return PersistedSelection{}, newSelectionStateError("malformed_json", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return PersistedSelection{}, newSelectionStateError("trailing_data", nil)
		}
		return PersistedSelection{}, newSelectionStateError("malformed_json", err)
	}
	return normalizePersistedSelection(record)
}

// discardTargetHandle releases a handle on a path that is already failing or
// superseded; the release error cannot change that outcome. Like Close, the
// detach ignores the caller's cancellation so cleanup still runs.
func discardTargetHandle(ctx context.Context, handle *TargetHandle) {
	if err := handle.Detach(context.WithoutCancel(ctx)); err != nil {
		return
	}
}

// discardRelease closes a detach-only handle on a superseded, failing, or
// abandoned path; the release error cannot change that decided outcome.
func discardRelease(handle interface{ Close() error }) {
	if err := handle.Close(); err != nil {
		return
	}
}

// rememberedBrowserIdentity returns the identity recorded for an accepted
// candidate's endpoint. An unparsable debugger URL leaves the identity empty:
// the candidate itself was already validated, so there is nothing to record.
func rememberedBrowserIdentity(version BrowserVersion, fallback *url.URL) BrowserIdentity {
	identity, failure := browserIdentityFromVersion(version, fallback)
	if failure != nil {
		return BrowserIdentity{}
	}
	return identity
}

func applyProbedCapabilities(target *Target, capabilities TargetCapabilities) {
	if capabilities.DomainKnown {
		target.WebMCP = capabilities.DomainSupported
		target.WebMCPKnown = true
		target.WebMCPDomainSupported = capabilities.DomainSupported
		target.WebMCPDomainKnown = true
	} else {
		target.WebMCP = capabilities.WebMCP
		target.WebMCPKnown = true
		target.WebMCPDomainSupported = capabilities.WebMCP
		target.WebMCPDomainKnown = true
	}
	target.PageToolsReady = capabilities.PageToolsReady
	target.PageToolsKnown = capabilities.PageToolsKnown
	target.PageToolsEvidence = capabilities.PageToolsEvidence
	target.DocumentReadyState = capabilities.DocumentReadyState
	target.DocumentLoading = capabilities.DocumentLoading
	target.DocumentLoadingKnown = capabilities.DocumentLoadingKnown
	if capabilities.ToolCount >= 0 {
		target.ToolCount = capabilities.ToolCount
		target.ToolCountKnown = capabilities.ToolCountKnown || capabilities.ToolCount >= 0
	}
}

// Selected returns a snapshot of the service's current selection. The boolean
// is false when no selection has been committed.
func (s *Service) Selected() (Selection, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.selection == nil {
		return Selection{}, false
	}
	return *s.selection, true
}

// CurrentSelection is a descriptive alias for Selected.
func (s *Service) CurrentSelection() (Selection, bool) { return s.Selected() }

// ReleaseSelection clears and detaches the current selection. Releasing an
// already empty service is a successful no-op.
func (s *Service) ReleaseSelection() error {
	s.mu.Lock()
	if s.selection == nil {
		s.mu.Unlock()
		return nil
	}
	previous := s.selection
	s.selection = nil
	s.mu.Unlock()
	if previous.Handle == nil {
		return nil
	}
	return previous.Handle.Close()
}

// Close is the service-level selection cleanup hook. Discovery itself owns no
// browser process, so closing the service only releases its attached target.
func (s *Service) Close() error { return s.ReleaseSelection() }

// Close releases the selected target handle, if this selection owns one.
// Selection values remain safe to close after the service selects another
// target because the handle is independently idempotent.
func (s Selection) Close() error {
	if s.Handle == nil {
		return nil
	}
	return s.Handle.Close()
}

// Release is an alias for Selection.Close.
func (s Selection) Release() error { return s.Close() }
