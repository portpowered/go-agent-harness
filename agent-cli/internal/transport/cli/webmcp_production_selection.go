package cli

import (
	"context"
	"errors"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

type productionCLISelectionStore struct{ store WebMCPSelectionStore }

func (s productionCLISelectionStore) Load(ctx context.Context) (discovery.PersistedSelection, error) {
	if err := contextErrorForProduction(ctx); err != nil {
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

func (s productionCLISelectionStore) Save(ctx context.Context, record discovery.PersistedSelection) error {
	if err := contextErrorForProduction(ctx); err != nil {
		return err
	}
	if s.store == nil {
		return errors.New("WebMCP selection store is unavailable")
	}
	return s.store.Save(WebMCPSelection{
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

func productionSelectionStore(value any) any {
	if value == nil {
		return nil
	}
	if store, ok := value.(WebMCPSelectionStore); ok {
		return productionCLISelectionStore{store: store}
	}
	return value
}

func contextErrorForProduction(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
