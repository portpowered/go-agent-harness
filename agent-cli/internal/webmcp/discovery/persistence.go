package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// SelectionPersistenceVersion is the only persisted selection schema this
	// package understands. Unknown versions fail closed instead of being
	// interpreted as a compatible older or newer record.
	SelectionPersistenceVersion uint = 1

	maxPersistedContinuity = 128
)

var ErrSelectionNotFound = errors.New("webmcp selection was not found")

// PersistedSelection is a locator and continuity claim, not an authorization
// token. Its JSON shape deliberately has no endpoint URL, page URL, websocket
// URL, credentials, query string, fragment, or tool reference.
type PersistedSelection struct {
	Version          uint      `json:"version"`
	EndpointID       string    `json:"endpoint_id"`
	BrowserID        string    `json:"browser_id"`
	TargetID         string    `json:"target_id"`
	Origin           string    `json:"origin"`
	ContinuityMarker string    `json:"continuity_marker"`
	Generation       uint64    `json:"generation"`
	SelectedAt       time.Time `json:"selected_at"`

	// These aliases make programmatic fakes readable without adding alternate
	// fields to the persisted JSON contract. ContinuityMarker wins when both
	// values are supplied.
	Continuity             string `json:"-"`
	TargetContinuityMarker string `json:"-"`
}

// SelectionRecord and StoredSelection are descriptive aliases for callers
// that use those names for the same versioned state record.
type SelectionRecord = PersistedSelection
type StoredSelection = PersistedSelection

// SelectionStore is the conventional typed persistence seam. Save must
// replace the stored record atomically from the caller's perspective; a
// production filesystem implementation can use a same-directory temporary
// file and rename, while fakes can replace one in-memory value.
type SelectionStore interface {
	Load(context.Context) (PersistedSelection, error)
	Save(context.Context, PersistedSelection) error
}

// AtomicSelectionStore names the same seam when the implementation exposes
// the atomic operation explicitly.
type AtomicSelectionStore interface {
	Load(context.Context) (PersistedSelection, error)
	SaveAtomic(context.Context, PersistedSelection) error
}

// ByteSelectionStore is a useful seam for stores that own their JSON bytes.
// The adapter validates and redacts the record before passing bytes to Save.
type ByteSelectionStore interface {
	Load(context.Context) ([]byte, error)
	Save(context.Context, []byte) error
}

// AtomicByteSelectionStore is the byte-oriented atomic variant.
type AtomicByteSelectionStore interface {
	Load(context.Context) ([]byte, error)
	SaveAtomic(context.Context, []byte) error
}

// AutoSelectMode controls reconnect selection when no explicit browser/target
// pair is supplied. Persisted mode never falls back to another target.
type AutoSelectMode string

const (
	AutoSelectOff       AutoSelectMode = "off"
	AutoSelectSingle    AutoSelectMode = "single"
	AutoSelectPersisted AutoSelectMode = "persisted"

	// Descriptive aliases used by configuration adapters.
	AutoSelectModeOff       = AutoSelectOff
	AutoSelectModeSingle    = AutoSelectSingle
	AutoSelectModePersisted = AutoSelectPersisted
)

// ReconnectOptions controls exact restoration after a fresh discovery pass.
// BrowserID, TargetID, and Origin are explicit inputs and therefore take
// precedence over a persisted record. Origin is a constraint, never an
// alternative selector.
type ReconnectOptions struct {
	AutoSelect AutoSelectMode
	Mode       AutoSelectMode

	BrowserID string
	TargetID  string
	Origin    string
	Activate  bool
	Reason    string

	// RejectPersistedConflict is available to callers that want a strict
	// reconciliation check. Normal C0 behavior leaves it false: an explicit
	// selection wins and atomically replaces the persisted locator.
	RejectPersistedConflict bool

	// ContinuityMarker is an internal exact-reconnect constraint used when a
	// live disconnected selection is restored. It is never persisted or exposed
	// as a model-facing selector.
	ContinuityMarker string `json:"-"`
}

// SelectionReconnectOptions is a descriptive alias for ReconnectOptions.
type SelectionReconnectOptions = ReconnectOptions

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

func normalizePersistedSelection(record PersistedSelection) (PersistedSelection, error) {
	if record.Version != SelectionPersistenceVersion {
		return PersistedSelection{}, newSelectionStateError("unknown_version", nil)
	}
	record.EndpointID = strings.TrimSpace(record.EndpointID)
	record.BrowserID = strings.TrimSpace(record.BrowserID)
	record.TargetID = strings.TrimSpace(record.TargetID)
	if !publicIDPattern.MatchString(record.EndpointID) || !publicIDPattern.MatchString(record.BrowserID) || !publicIDPattern.MatchString(record.TargetID) {
		return PersistedSelection{}, newSelectionStateError("normalized_ids_required", nil)
	}
	if record.Origin == "" {
		return PersistedSelection{}, newSelectionStateError("origin_required", nil)
	}
	_, origin, internal, reason := normalizePageURL(record.Origin)
	if internal || reason != "" || origin == "" || origin != record.Origin {
		return PersistedSelection{}, newSelectionStateError("canonical_origin_required", nil)
	}
	record.Origin = origin
	if record.ContinuityMarker == "" {
		record.ContinuityMarker = record.TargetContinuityMarker
	}
	if record.ContinuityMarker == "" {
		record.ContinuityMarker = record.Continuity
	}
	record.ContinuityMarker = strings.TrimSpace(record.ContinuityMarker)
	if record.ContinuityMarker == "" || len(record.ContinuityMarker) > maxPersistedContinuity || hasControl(record.ContinuityMarker) || strings.ContainsAny(record.ContinuityMarker, "/?#") || strings.Contains(record.ContinuityMarker, "://") {
		return PersistedSelection{}, newSelectionStateError("continuity_marker_invalid", nil)
	}
	if record.Generation == 0 {
		return PersistedSelection{}, newSelectionStateError("generation_required", nil)
	}
	if record.SelectedAt.IsZero() || record.SelectedAt.Location() == nil {
		return PersistedSelection{}, newSelectionStateError("selection_time_required", nil)
	}
	record.SelectedAt = record.SelectedAt.UTC()
	// Aliases are input conveniences only; never retain them in a value that
	// will be serialized or returned from a store.
	record.Continuity = ""
	record.TargetContinuityMarker = ""
	return record, nil
}

func persistedSelectionFrom(browser BrowserCandidate, target Target, selectedAt time.Time) (PersistedSelection, error) {
	return normalizePersistedSelection(PersistedSelection{
		Version:          SelectionPersistenceVersion,
		EndpointID:       browser.ID,
		BrowserID:        browser.ID,
		TargetID:         target.ID,
		Origin:           target.Origin,
		ContinuityMarker: target.ContinuityMarker,
		Generation:       target.Generation,
		SelectedAt:       selectedAt,
	})
}

func newSelectionStateError(reason string, cause error) *DiscoveryError {
	return &DiscoveryError{
		Code:      CodeBrowserProtocolInvalid,
		Message:   "persisted browser selection state is invalid",
		Retryable: false,
		Cause:     cause,
		Details: map[string]any{
			"phase":       "selection_state",
			"protocol":    "selection_state.v1",
			"reason_code": boundedLabel(reason, 64),
		},
	}
}

func newSelectionPersistenceError(phase, reason string, cause error) *DiscoveryError {
	return &DiscoveryError{
		Code:      CodeBrowserProtocolInvalid,
		Message:   "persisted browser selection state could not be processed",
		Retryable: phase == "save",
		Cause:     cause,
		Details: map[string]any{
			"phase":       boundedLabel("selection_"+phase, 32),
			"protocol":    "selection_state.v1",
			"reason_code": boundedLabel(reason, 64),
		},
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func isSelectionMissing(err error) bool {
	return errors.Is(err, ErrSelectionNotFound) || errors.Is(err, os.ErrNotExist)
}

// PersistenceEnabled reports whether this service has an active selection
// store. Supplying a store enables persistence by default; callers can use
// Options.PersistenceEnabled or Options.DisablePersistence to turn it off.
func (s *Service) PersistenceEnabled() bool {
	if s == nil {
		return false
	}
	return s.persistenceEnabled
}

// LoadPersistedSelection loads the safe record without mutating live
// selection state. The boolean is false when persistence is disabled or the
// store has no record.
func (s *Service) LoadPersistedSelection(ctx context.Context) (PersistedSelection, bool, error) {
	record, present, failure := s.loadPersistedSelection(ctx)
	if failure != nil {
		return PersistedSelection{}, false, failure
	}
	return record, present, nil
}

// LoadSelection is the strict two-result form for callers that treat a
// missing persisted record as an ordinary store error.
func (s *Service) LoadSelection(ctx context.Context) (PersistedSelection, error) {
	record, present, err := s.loadPersistedSelection(ctx)
	if err != nil {
		return PersistedSelection{}, err
	}
	if !present {
		return PersistedSelection{}, ErrSelectionNotFound
	}
	return record, nil
}

func (s *Service) loadPersistedSelection(ctx context.Context) (PersistedSelection, bool, *DiscoveryError) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return PersistedSelection{}, false, newSelectionPersistenceError("load", "context_canceled", err)
	}
	if s == nil || !s.persistenceEnabled {
		return PersistedSelection{}, false, nil
	}
	if s.persistenceError != nil {
		return PersistedSelection{}, false, newSelectionPersistenceError("load", "store_unavailable", s.persistenceError)
	}
	if s.selectionStore.load == nil {
		return PersistedSelection{}, false, newSelectionPersistenceError("load", "store_unavailable", nil)
	}
	record, err := s.selectionStore.load(ctx)
	if err != nil {
		if isSelectionMissing(err) {
			return PersistedSelection{}, false, nil
		}
		var discoveryErr *DiscoveryError
		if errors.As(err, &discoveryErr) {
			return PersistedSelection{}, false, discoveryErr
		}
		return PersistedSelection{}, false, newSelectionPersistenceError("load", "load_failed", err)
	}
	normalized, err := normalizePersistedSelection(record)
	if err != nil {
		var discoveryErr *DiscoveryError
		if errors.As(err, &discoveryErr) {
			return PersistedSelection{}, false, discoveryErr
		}
		return PersistedSelection{}, false, newSelectionStateError("invalid_record", err)
	}
	return normalized, true, nil
}

// persistSelectionLocked atomically writes the exact selected identity when
// persistence is enabled. The caller must hold Service.mu.
func (s *Service) persistSelectionLocked(ctx context.Context, browser BrowserCandidate, target Target, selectedAt time.Time) *DiscoveryError {
	if s == nil || !s.persistenceEnabled {
		return nil
	}
	if s.persistenceError != nil {
		return newSelectionPersistenceError("save", "store_unavailable", s.persistenceError)
	}
	if s.selectionStore.save == nil {
		return newSelectionPersistenceError("save", "store_unavailable", nil)
	}
	record, err := persistedSelectionFrom(browser, target, selectedAt)
	if err != nil {
		var discoveryErr *DiscoveryError
		if errors.As(err, &discoveryErr) {
			return discoveryErr
		}
		return newSelectionStateError("invalid_record", err)
	}
	if err := s.selectionStore.save(ctx, record); err != nil {
		var discoveryErr *DiscoveryError
		if errors.As(err, &discoveryErr) {
			return discoveryErr
		}
		return newSelectionPersistenceError("save", "save_failed", err)
	}
	return nil
}

func firstReconnectOptions(options []ReconnectOptions) ReconnectOptions {
	if len(options) == 0 {
		return ReconnectOptions{}
	}
	return options[0]
}

func (options ReconnectOptions) resolvedAutoSelect() AutoSelectMode {
	if options.AutoSelect != "" {
		return options.AutoSelect
	}
	if options.Mode != "" {
		return options.Mode
	}
	return AutoSelectOff
}

func (options ReconnectOptions) hasExplicitSelection() bool {
	return strings.TrimSpace(options.BrowserID) != "" || strings.TrimSpace(options.TargetID) != "" || strings.TrimSpace(options.Origin) != ""
}

// Reconnect rediscovers the configured browser set and reconciles selection
// state without ever substituting a different target. Explicit IDs take
// precedence over persisted state; automatic modes are deliberately opt-in.
func (s *Service) Reconnect(ctx context.Context, inputs ConnectionInputs, options ...ReconnectOptions) (Selection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reconnectOptions := firstReconnectOptions(options)
	if err := validateReconnectOptions(reconnectOptions); err != nil {
		return Selection{}, err
	}

	if reconnectOptions.hasExplicitSelection() {
		if strings.TrimSpace(reconnectOptions.TargetID) == "" {
			if browserID, _, phase, disconnected := s.disconnectedBrowser(); disconnected &&
				(strings.TrimSpace(reconnectOptions.BrowserID) == "" || strings.TrimSpace(reconnectOptions.BrowserID) == browserID) {
				return Selection{}, newBrowserDisconnected(browserID, "", phase, nil)
			}
			if reconnectOptions.resolvedAutoSelect() == AutoSelectSingle {
				return s.reconnectSingle(ctx, inputs, reconnectOptions)
			}
			return Selection{}, newNoEligibleTab(strings.TrimSpace(reconnectOptions.BrowserID), TargetListOptions{
				BrowserID:      strings.TrimSpace(reconnectOptions.BrowserID),
				OriginContains: strings.TrimSpace(reconnectOptions.Origin),
				EligibleOnly:   Bool(true),
			}, 0)
		}
		var persisted PersistedSelection
		var persistedPresent bool
		if reconnectOptions.RejectPersistedConflict {
			var failure *DiscoveryError
			persisted, persistedPresent, failure = s.loadPersistedSelection(ctx)
			if failure != nil {
				return Selection{}, failure
			}
		}
		candidates, err := s.DiscoverAll(ctx, inputs)
		if err != nil {
			return Selection{}, err
		}
		browser, failure := reconnectBrowser(candidates, reconnectOptions.BrowserID)
		if failure != nil {
			return Selection{}, failure
		}
		if persistedPresent && persistedSelectionConflicts(persisted, reconnectOptions) {
			return Selection{}, newStaleSelection(persisted.BrowserID, persisted.TargetID, persisted.Generation, "explicit_selection_conflict")
		}
		return s.reconnectExact(ctx, browser, reconnectOptions)
	}

	switch reconnectOptions.resolvedAutoSelect() {
	case AutoSelectOff:
		return Selection{}, newNoEligibleTab(strings.TrimSpace(reconnectOptions.BrowserID), TargetListOptions{
			BrowserID:    strings.TrimSpace(reconnectOptions.BrowserID),
			EligibleOnly: Bool(true),
		}, 0)
	case AutoSelectSingle:
		if browserID, targetID, phase, disconnected := s.disconnectedBrowser(); disconnected {
			if current, hasSelection := s.disconnectedSelection(); hasSelection && current.BrowserID == browserID {
				return s.reconnectCurrentSelection(ctx, inputs, current, reconnectOptions)
			}
			if targetID == "" {
				return Selection{}, newBrowserDisconnected(browserID, "", phase, nil)
			}
			reconnectOptions.BrowserID = browserID
			reconnectOptions.TargetID = targetID
			return s.reconnectExactDisconnectedTarget(ctx, inputs, reconnectOptions)
		}
		return s.reconnectSingle(ctx, inputs, reconnectOptions)
	case AutoSelectPersisted:
		return s.reconnectPersisted(ctx, inputs, reconnectOptions)
	default:
		return Selection{}, newSelectionStateError("auto_select_invalid", nil)
	}
}

func (s *Service) disconnectedSelection() (Selection, bool) {
	if s == nil {
		return Selection{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.selection == nil || s.selection.connected {
		return Selection{}, false
	}
	return *s.selection, true
}

func (s *Service) disconnectedBrowser() (browserID, targetID, phase string, ok bool) {
	if s == nil {
		return "", "", "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.selection != nil && !s.selection.connected {
		if state, exists := s.disconnected[s.selection.BrowserID]; exists {
			return s.selection.BrowserID, state.TargetID, state.Phase, true
		}
	}
	ids := make([]string, 0, len(s.disconnected))
	for browserID := range s.disconnected {
		ids = append(ids, browserID)
	}
	if len(ids) == 0 {
		return "", "", "", false
	}
	sort.Strings(ids)
	state := s.disconnected[ids[0]]
	return ids[0], state.TargetID, state.Phase, true
}

func (s *Service) reconnectExactDisconnectedTarget(ctx context.Context, inputs ConnectionInputs, options ReconnectOptions) (Selection, error) {
	candidates, err := s.DiscoverAll(ctx, inputs)
	if err != nil {
		return Selection{}, err
	}
	browser, failure := reconnectBrowser(candidates, options.BrowserID)
	if failure != nil {
		return Selection{}, newBrowserDisconnected(options.BrowserID, options.TargetID, "reconnect", nil)
	}
	return s.reconnectExact(ctx, browser, options)
}

func (s *Service) reconnectCurrentSelection(ctx context.Context, inputs ConnectionInputs, current Selection, options ReconnectOptions) (Selection, error) {
	candidates, err := s.DiscoverAll(ctx, inputs)
	if err != nil {
		return Selection{}, err
	}
	browser, failure := reconnectBrowser(candidates, current.BrowserID)
	if failure != nil {
		return Selection{}, newStaleSelection(current.BrowserID, current.TargetID, current.Generation, "browser_missing_after_reconnect")
	}
	options.BrowserID = current.BrowserID
	options.TargetID = current.TargetID
	options.Origin = current.Origin
	options.ContinuityMarker = current.Target.ContinuityMarker
	selected, err := s.reconnectExact(ctx, browser, options)
	if err == nil {
		return selected, nil
	}
	var discoveryErr *DiscoveryError
	if errors.As(err, &discoveryErr) && discoveryErr.Code == CodeNoEligibleTab {
		return Selection{}, newStaleSelection(current.BrowserID, current.TargetID, current.Generation, "target_missing_after_reconnect")
	}
	return Selection{}, err
}

// RestoreSelection is a descriptive alias for Reconnect.
func (s *Service) RestoreSelection(ctx context.Context, inputs ConnectionInputs, options ...ReconnectOptions) (Selection, error) {
	return s.Reconnect(ctx, inputs, options...)
}

// ReconnectSelection is a descriptive alias for Reconnect.
func (s *Service) ReconnectSelection(ctx context.Context, inputs ConnectionInputs, options ...ReconnectOptions) (Selection, error) {
	return s.Reconnect(ctx, inputs, options...)
}

// RestorePersistedSelection requests the strict persisted-identity path.
func (s *Service) RestorePersistedSelection(ctx context.Context, inputs ConnectionInputs, options ...ReconnectOptions) (Selection, error) {
	reconnectOptions := firstReconnectOptions(options)
	reconnectOptions.AutoSelect = AutoSelectPersisted
	reconnectOptions.Mode = AutoSelectPersisted
	return s.Reconnect(ctx, inputs, reconnectOptions)
}

func validateReconnectOptions(options ReconnectOptions) *DiscoveryError {
	if browserID := strings.TrimSpace(options.BrowserID); browserID != "" && !publicIDPattern.MatchString(browserID) {
		return newNoEligibleTab(browserID, TargetListOptions{BrowserID: browserID, TargetID: options.TargetID}, 0)
	}
	if targetID := strings.TrimSpace(options.TargetID); targetID != "" && !publicIDPattern.MatchString(targetID) {
		return newNoEligibleTab(options.BrowserID, TargetListOptions{BrowserID: options.BrowserID, TargetID: targetID}, 0)
	}
	if origin := strings.TrimSpace(options.Origin); origin != "" {
		canonical := canonicalOriginValue(origin)
		if canonical == "" || canonical != origin {
			return newNoEligibleTab(options.BrowserID, TargetListOptions{BrowserID: options.BrowserID, TargetID: options.TargetID, OriginContains: origin}, 0)
		}
	}
	return nil
}

func reconnectBrowser(candidates []BrowserCandidate, requestedID string) (BrowserCandidate, *DiscoveryError) {
	requestedID = strings.TrimSpace(requestedID)
	if requestedID != "" {
		for _, candidate := range candidates {
			if candidate.ID == requestedID {
				return candidate, nil
			}
		}
		return BrowserCandidate{}, newNoEligibleTab(requestedID, TargetListOptions{BrowserID: requestedID}, len(candidates))
	}
	if len(candidates) == 0 {
		return BrowserCandidate{}, newNoEligibleTab("", TargetListOptions{}, 0)
	}
	if len(candidates) > 1 {
		ids := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			ids = append(ids, candidate.ID)
		}
		sort.Strings(ids)
		return BrowserCandidate{}, newAmbiguousBrowser(ids)
	}
	return candidates[0], nil
}

func (s *Service) reconnectExact(ctx context.Context, browser BrowserCandidate, options ReconnectOptions) (Selection, error) {
	s.mu.Lock()
	if s.browsers == nil {
		s.browsers = make(map[string]BrowserCandidate)
	}
	s.browsers[browser.ID] = browser
	targets, failure := s.refreshTargetsLocked(ctx, browser, TargetListOptions{
		BrowserID:            browser.ID,
		TargetID:             strings.TrimSpace(options.TargetID),
		EligibleOnly:         Bool(false),
		IncludeZeroToolPages: true,
	})
	if failure != nil {
		s.mu.Unlock()
		return Selection{}, failure
	}
	targetID := strings.TrimSpace(options.TargetID)
	if targetID == "" {
		matches := make([]Target, 0, len(targets))
		for _, candidate := range targets {
			if candidate.Eligible && candidate.WebMCP && candidate.WebMCPKnown && (options.Origin == "" || candidate.Origin == options.Origin) {
				matches = append(matches, candidate)
			}
		}
		switch len(matches) {
		case 0:
			s.mu.Unlock()
			return Selection{}, newNoEligibleTab(browser.ID, TargetListOptions{BrowserID: browser.ID, EligibleOnly: Bool(true), IncludeZeroToolPages: true}, len(targets))
		case 1:
			targetID = matches[0].ID
		default:
			ids := make([]string, 0, len(matches))
			for _, candidate := range matches {
				ids = append(ids, candidate.ID)
			}
			sort.Strings(ids)
			s.mu.Unlock()
			return Selection{}, newAmbiguousTab(browser.ID, ids)
		}
	}
	target, failure := exactReconnectTarget(browser.ID, targets, targetID, options.Origin)
	if failure != nil {
		if options.ContinuityMarker != "" {
			if state, exists := s.targets[browser.ID][targetID]; exists && state.target.ID == targetID {
				reason := ""
				switch {
				case options.Origin != "" && state.target.Origin != options.Origin:
					reason = "origin_changed"
				case state.target.ContinuityMarker != options.ContinuityMarker:
					reason = "continuity_changed"
				}
				if reason != "" {
					selectedGeneration := state.generation
					if s.selection != nil && s.selection.BrowserID == browser.ID && s.selection.TargetID == targetID {
						selectedGeneration = s.selection.Generation
					}
					s.mu.Unlock()
					return Selection{}, newStaleSelection(browser.ID, targetID, selectedGeneration, reason)
				}
			}
		}
		s.mu.Unlock()
		return Selection{}, failure
	}
	if generationFailure := s.advanceDisconnectedSelectionGenerationLocked(browser.ID, target.ID, &target); generationFailure != nil {
		s.mu.Unlock()
		return Selection{}, generationFailure
	}
	selected, previousHandle, selectionFailure := s.commitReconnectSelectionLocked(ctx, browser, target, options, time.Time{})
	s.mu.Unlock()
	if selectionFailure != nil {
		return Selection{}, selectionFailure
	}
	if previousHandle != nil && previousHandle != selected.Handle {
		_ = previousHandle.Close()
	}
	return selected, nil
}

func (s *Service) reconnectSingle(ctx context.Context, inputs ConnectionInputs, options ReconnectOptions) (Selection, error) {
	candidates, err := s.DiscoverAll(ctx, inputs)
	if err != nil {
		return Selection{}, err
	}
	browser, failure := reconnectBrowser(candidates, options.BrowserID)
	if failure != nil {
		return Selection{}, failure
	}
	return s.reconnectUniqueTarget(ctx, browser, options)
}

func (s *Service) reconnectPersisted(ctx context.Context, inputs ConnectionInputs, options ReconnectOptions) (Selection, error) {
	persisted, present, failure := s.loadPersistedSelection(ctx)
	if failure != nil {
		return Selection{}, failure
	}
	if !present {
		return Selection{}, newNoEligibleTab(options.BrowserID, TargetListOptions{
			BrowserID:    strings.TrimSpace(options.BrowserID),
			EligibleOnly: Bool(true),
		}, 0)
	}
	candidates, err := s.DiscoverAll(ctx, inputs)
	if err != nil {
		return Selection{}, err
	}
	browser, browserFailure := reconnectBrowser(candidates, persisted.BrowserID)
	if browserFailure != nil {
		browserFailure = newStaleSelection(persisted.BrowserID, persisted.TargetID, persisted.Generation, "browser_missing_after_reconnect")
		return Selection{}, browserFailure
	}
	if persisted.EndpointID != browser.ID {
		return Selection{}, newStaleSelection(persisted.BrowserID, persisted.TargetID, persisted.Generation, "endpoint_changed")
	}

	s.mu.Lock()
	if s.browsers == nil {
		s.browsers = make(map[string]BrowserCandidate)
	}
	s.browsers[browser.ID] = browser
	targets, listFailure := s.refreshTargetsLocked(ctx, browser, TargetListOptions{
		BrowserID:            browser.ID,
		TargetID:             persisted.TargetID,
		EligibleOnly:         Bool(false),
		IncludeZeroToolPages: true,
	})
	if listFailure != nil {
		s.mu.Unlock()
		return Selection{}, listFailure
	}
	for _, candidate := range targets {
		if candidate.ID == persisted.TargetID && candidate.Origin != persisted.Origin {
			s.mu.Unlock()
			return Selection{}, newStaleSelection(persisted.BrowserID, persisted.TargetID, persisted.Generation, "origin_changed")
		}
	}
	target, targetFailure := exactReconnectTarget(browser.ID, targets, persisted.TargetID, persisted.Origin)
	if targetFailure != nil {
		s.mu.Unlock()
		if targetFailure.Code == CodeNoEligibleTab {
			return Selection{}, newStaleSelection(persisted.BrowserID, persisted.TargetID, persisted.Generation, "target_missing_after_reconnect")
		}
		return Selection{}, targetFailure
	}
	if target.ContinuityMarker != persisted.ContinuityMarker {
		s.mu.Unlock()
		return Selection{}, newStaleSelection(persisted.BrowserID, persisted.TargetID, persisted.Generation, "continuity_changed")
	}
	if target.Generation == ^uint64(0) || persisted.Generation == ^uint64(0) {
		s.mu.Unlock()
		return Selection{}, newSelectionStateError("generation_exhausted", nil)
	}
	currentGeneration := target.Generation
	if next := persisted.Generation + 1; next > currentGeneration {
		currentGeneration = next
	}
	previousGeneration := target.Generation
	target.Generation = currentGeneration
	state := s.targets[browser.ID][target.ID]
	state.target = target
	state.generation = currentGeneration
	state.closed = false
	s.targets[browser.ID][target.ID] = state
	if currentGeneration != previousGeneration {
		s.emitTarget(EventPageGenerationChanged, browser.ID, target.ID, currentGeneration, map[string]any{
			"previous_generation": previousGeneration,
			"current_generation":  currentGeneration,
			"reason":              "reconnect",
		})
	}
	selected, previousHandle, selectionFailure := s.commitReconnectSelectionLocked(ctx, browser, target, options, persisted.SelectedAt)
	s.mu.Unlock()
	if selectionFailure != nil {
		return Selection{}, selectionFailure
	}
	if previousHandle != nil && previousHandle != selected.Handle {
		_ = previousHandle.Close()
	}
	return selected, nil
}

func (s *Service) reconnectUniqueTarget(ctx context.Context, browser BrowserCandidate, options ReconnectOptions) (Selection, error) {
	s.mu.Lock()
	if s.browsers == nil {
		s.browsers = make(map[string]BrowserCandidate)
	}
	s.browsers[browser.ID] = browser
	targets, failure := s.refreshTargetsLocked(ctx, browser, TargetListOptions{
		BrowserID:            browser.ID,
		EligibleOnly:         Bool(true),
		IncludeZeroToolPages: true,
	})
	if failure != nil {
		s.mu.Unlock()
		return Selection{}, failure
	}
	candidates := make([]Target, 0, len(targets))
	for _, target := range targets {
		if target.Eligible && target.WebMCP && target.WebMCPKnown && (options.Origin == "" || target.Origin == options.Origin) {
			candidates = append(candidates, target)
		}
	}
	if len(candidates) == 0 {
		s.mu.Unlock()
		return Selection{}, newNoEligibleTab(browser.ID, TargetListOptions{BrowserID: browser.ID, EligibleOnly: Bool(true), IncludeZeroToolPages: true}, len(targets))
	}
	if len(candidates) > 1 {
		ids := make([]string, 0, len(candidates))
		for _, target := range candidates {
			ids = append(ids, target.ID)
		}
		sort.Strings(ids)
		s.mu.Unlock()
		return Selection{}, newAmbiguousTab(browser.ID, ids)
	}
	selected, previousHandle, selectionFailure := s.commitReconnectSelectionLocked(ctx, browser, candidates[0], options, time.Time{})
	s.mu.Unlock()
	if selectionFailure != nil {
		return Selection{}, selectionFailure
	}
	if previousHandle != nil && previousHandle != selected.Handle {
		_ = previousHandle.Close()
	}
	return selected, nil
}

func exactReconnectTarget(browserID string, targets []Target, targetID, origin string) (Target, *DiscoveryError) {
	targetID = strings.TrimSpace(targetID)
	for _, target := range targets {
		if target.ID != targetID {
			continue
		}
		if origin != "" && target.Origin != origin {
			return Target{}, newNoEligibleTab(browserID, TargetListOptions{BrowserID: browserID, TargetID: targetID, OriginContains: origin, EligibleOnly: Bool(true)}, len(targets))
		}
		if !target.WebMCP || !target.WebMCPKnown {
			return Target{}, newUnsupportedWebMCP(browserID, targetID)
		}
		if !target.Eligible {
			return Target{}, newNoEligibleTab(browserID, TargetListOptions{BrowserID: browserID, TargetID: targetID, EligibleOnly: Bool(true)}, len(targets))
		}
		return target, nil
	}
	return Target{}, newNoEligibleTab(browserID, TargetListOptions{BrowserID: browserID, TargetID: targetID, EligibleOnly: Bool(true)}, len(targets))
}

func persistedSelectionConflicts(record PersistedSelection, options ReconnectOptions) bool {
	if browserID := strings.TrimSpace(options.BrowserID); browserID != "" && browserID != record.BrowserID {
		return true
	}
	if targetID := strings.TrimSpace(options.TargetID); targetID != "" && targetID != record.TargetID {
		return true
	}
	if origin := strings.TrimSpace(options.Origin); origin != "" && origin != record.Origin {
		return true
	}
	return false
}

func (s *Service) commitReconnectSelectionLocked(ctx context.Context, browser BrowserCandidate, target Target, options ReconnectOptions, selectedAt time.Time) (Selection, *TargetHandle, *DiscoveryError) {
	var handle *TargetHandle
	if s.targetAttacher != nil {
		detacher, attachErr := s.targetAttacher.Attach(ctx, browser, target)
		if attachErr != nil {
			failure := classifySelectionOperationError(attachErr, browser.ID, target.ID, "attach", "attach_failed")
			s.noteBrowserDisconnectedFailureLocked(failure, browser.ID, target.ID, "attach")
			return Selection{}, nil, failure
		}
		handle = NewDetachOnlyTargetHandle(detacher)
	}
	if options.Activate && s.activator != nil {
		if activateErr := s.activator.Activate(ctx, browser, target); activateErr != nil {
			if handle != nil {
				_ = handle.Close()
			}
			failure := classifySelectionOperationError(activateErr, browser.ID, target.ID, "activate", "activation_failed")
			s.noteBrowserDisconnectedFailureLocked(failure, browser.ID, target.ID, "activate")
			return Selection{}, nil, failure
		}
	}
	if selectedAt.IsZero() {
		if s.clock != nil {
			selectedAt = s.clock.Now()
		}
		if selectedAt.IsZero() {
			selectedAt = time.Unix(0, 0).UTC()
		}
	}
	selectedAt = selectedAt.UTC()
	selected := Selection{
		BrowserID:  browser.ID,
		TargetID:   target.ID,
		Title:      target.Title,
		URL:        target.URL,
		Origin:     target.Origin,
		Generation: target.Generation,
		SelectedAt: selectedAt,
		Target:     target,
		Handle:     handle,
		statusSet:  true,
		connected:  true,
		ready:      true,
	}
	if failure := s.persistSelectionLocked(ctx, browser, target, selected.SelectedAt); failure != nil {
		if handle != nil {
			_ = handle.Close()
		}
		return Selection{}, nil, failure
	}
	var previousHandle *TargetHandle
	if s.selection != nil {
		previousHandle = s.selection.Handle
	}
	reason := boundedLabel(options.Reason, maxSelectionReason)
	if reason == "" {
		reason = string(options.resolvedAutoSelect())
		if reason == string(AutoSelectOff) {
			reason = defaultSelectionReason
		}
	}
	s.selection = &selected
	s.clearBrowserDisconnectedLocked(browser.ID)
	s.emitTarget(EventTargetSelected, browser.ID, target.ID, target.Generation, map[string]any{
		"generation": target.Generation,
		"reason":     reason,
	})
	if handle != nil {
		s.emitTarget(EventTargetAttached, browser.ID, target.ID, 0, map[string]any{
			"ownership_mode": string(handle.Ownership()),
			"phase":          "attached",
		})
	}
	return selected, previousHandle, nil
}
