package selectionstore

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

// discoveryStore adapts a Store to the context-aware discovery persistence
// contract used by the neutral discovery service.
type discoveryStore struct{ store Store }

// ForDiscovery adapts value to discovery persistence when it is a Store.
// A nil value stays nil and any other value is returned unchanged so callers
// may inject a discovery.SelectionStore directly.
func ForDiscovery(value any) any {
	if value == nil {
		return nil
	}
	if store, ok := value.(Store); ok {
		return discoveryStore{store: store}
	}
	return value
}

func (s discoveryStore) Load(ctx context.Context) (discovery.PersistedSelection, error) {
	if err := contextError(ctx); err != nil {
		return discovery.PersistedSelection{}, err
	}
	if s.store == nil {
		return discovery.PersistedSelection{}, discovery.ErrSelectionNotFound
	}
	selection, err := s.store.Load()
	if err != nil {
		return discovery.PersistedSelection{}, err
	}
	if selection.BrowserID == "" && selection.TargetID == "" {
		return discovery.PersistedSelection{}, discovery.ErrSelectionNotFound
	}
	return discovery.PersistedSelection{
		Version:           uint(selection.Version),
		EndpointID:        selection.EndpointID,
		BrowserID:         selection.BrowserID,
		BrowserInstanceID: selection.BrowserInstanceID,
		TargetID:          selection.TargetID,
		Origin:            selection.Origin,
		ContinuityMarker:  selection.ContinuityMarker,
		Generation:        selection.Generation,
		SelectedAt:        selection.SelectedAt,
	}, nil
}

func (s discoveryStore) Save(ctx context.Context, record discovery.PersistedSelection) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s.store == nil {
		return errors.New("WebMCP selection store is unavailable")
	}
	return s.store.Save(Selection{
		Version:           int(record.Version),
		EndpointID:        record.EndpointID,
		BrowserID:         record.BrowserID,
		BrowserInstanceID: record.BrowserInstanceID,
		TargetID:          record.TargetID,
		Origin:            record.Origin,
		ContinuityMarker:  record.ContinuityMarker,
		Generation:        record.Generation,
		SelectedAt:        record.SelectedAt,
	})
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
