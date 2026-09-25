package cli

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/selectionstore"
)

const (
	// WebMCPSelectionFileName names the persisted selection file; it is
	// selectionstore.FileName.
	WebMCPSelectionFileName = selectionstore.FileName
	// WebMCPSelectionVersion is the persisted record version; it is
	// selectionstore.Version.
	WebMCPSelectionVersion = selectionstore.Version
)

// WebMCPSelection is the small persisted record shared by separate direct
// command invocations. Persistence lives in internal/webmcp/selectionstore.
type WebMCPSelection = selectionstore.Selection

// WebMCPSelectionStore persists and loads one opaque browser selection.
// Implementations may be injected by embedders and command tests.
type WebMCPSelectionStore = selectionstore.Store

// FileWebMCPSelectionStore is the default user-only selection store.
type FileWebMCPSelectionStore = selectionstore.FileStore

// NewFileWebMCPSelectionStore constructs a selection store below configDir.
// An empty configDir follows the same ~/.agent-cli default as ConfigStorage.
func NewFileWebMCPSelectionStore(configDir string) *FileWebMCPSelectionStore {
	if configDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			configDir = filepath.Join(home, config.ConfigDirName)
		}
	}
	return selectionstore.NewFileStore(configDir)
}

func (c *WebMCPOperationsCommand) loadDirectSelection() (WebMCPSelection, error) {
	store, err := c.selectionStore()
	if err != nil {
		return WebMCPSelection{}, err
	}
	return store.Load()
}

func (c *WebMCPOperationsCommand) saveDirectSelection(selection WebMCPSelection) error {
	store, err := c.selectionStore()
	if err != nil {
		return err
	}
	return store.Save(selection)
}

// selectionStore returns the injected store, or the default file store
// below the configured directory.
func (c *WebMCPOperationsCommand) selectionStore() (WebMCPSelectionStore, error) {
	if c != nil && c.SelectionStore != nil {
		return c.SelectionStore, nil
	}
	configDir := ""
	if c != nil && c.globalFlags != nil {
		configDir = c.globalFlags.ConfigDir()
	}
	store := NewFileWebMCPSelectionStore(configDir)
	if store.Path == "" {
		return nil, errors.New("WebMCP selection store is unavailable")
	}
	return store, nil
}
