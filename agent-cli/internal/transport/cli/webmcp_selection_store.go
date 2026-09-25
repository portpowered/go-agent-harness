package cli

import (
	"os"
	"path/filepath"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/selectionstore"
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
