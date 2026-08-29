package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
)

// WebMCPSelection is the small persisted record shared by separate direct
// command invocations. It is intentionally limited to normalized IDs and a
// redacted origin; endpoint credentials and websocket paths never cross this
// boundary.
type WebMCPSelection struct {
	Version           int       `json:"version"`
	EndpointID        string    `json:"endpoint_id"`
	BrowserID         string    `json:"browser_id"`
	BrowserInstanceID string    `json:"browser_instance_id,omitempty"`
	TargetID          string    `json:"target_id"`
	Origin            string    `json:"origin"`
	ContinuityMarker  string    `json:"continuity_marker,omitempty"`
	Generation        uint64    `json:"generation,omitempty"`
	SelectedAt        time.Time `json:"selected_at"`
}

// WebMCPSelectionStore persists and loads one opaque browser selection.
// Implementations may be injected by embedders and command tests.
type WebMCPSelectionStore interface {
	Load() (WebMCPSelection, error)
	Save(WebMCPSelection) error
}

// FileWebMCPSelectionStore is the default user-only selection store.
type FileWebMCPSelectionStore struct {
	Path string
}

// NewFileWebMCPSelectionStore constructs a selection store below configDir.
// An empty configDir follows the same ~/.agent-cli default as ConfigStorage.
func NewFileWebMCPSelectionStore(configDir string) *FileWebMCPSelectionStore {
	if configDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			configDir = filepath.Join(home, config.ConfigDirName)
		}
	}
	return &FileWebMCPSelectionStore{Path: filepath.Join(configDir, WebMCPSelectionFileName)}
}

func (s *FileWebMCPSelectionStore) Load() (WebMCPSelection, error) {
	if s == nil || s.Path == "" {
		return WebMCPSelection{}, errors.New("WebMCP selection path is unavailable")
	}
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return WebMCPSelection{}, nil
	}
	if err != nil {
		return WebMCPSelection{}, fmt.Errorf("read WebMCP selection: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var selection WebMCPSelection
	if err := decoder.Decode(&selection); err != nil {
		return WebMCPSelection{}, fmt.Errorf("decode WebMCP selection: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return WebMCPSelection{}, errors.New("decode WebMCP selection: more than one JSON value")
		}
		return WebMCPSelection{}, fmt.Errorf("decode WebMCP selection: %w", err)
	}
	if err := validateWebMCPSelection(selection); err != nil {
		return WebMCPSelection{}, err
	}
	if selection.Origin != "" {
		selection.Origin = safeOrigin(selection.Origin)
	}
	return selection, nil
}

func (s *FileWebMCPSelectionStore) Save(selection WebMCPSelection) error {
	if s == nil || s.Path == "" {
		return errors.New("WebMCP selection path is unavailable")
	}
	if selection.Version == 0 {
		selection.Version = WebMCPSelectionVersion
	}
	if selection.SelectedAt.IsZero() {
		selection.SelectedAt = time.Now().UTC()
	}
	if selection.Origin != "" {
		selection.Origin = safeOrigin(selection.Origin)
	}
	if err := validateWebMCPSelection(selection); err != nil {
		return err
	}
	directory := filepath.Dir(s.Path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create WebMCP selection directory: %w", err)
	}
	data, err := json.MarshalIndent(selection, "", "  ")
	if err != nil {
		return fmt.Errorf("encode WebMCP selection: %w", err)
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(directory, ".webmcp-selection-*")
	if err != nil {
		return fmt.Errorf("create WebMCP selection temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect WebMCP selection temporary file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write WebMCP selection: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close WebMCP selection temporary file: %w", err)
	}
	if err := os.Rename(temporaryName, s.Path); err != nil {
		return fmt.Errorf("replace WebMCP selection: %w", err)
	}
	return nil
}

func validateWebMCPSelection(selection WebMCPSelection) error {
	if selection.Version != WebMCPSelectionVersion {
		return fmt.Errorf("WebMCP selection version %d is unsupported", selection.Version)
	}
	if selection.BrowserID == "" || selection.TargetID == "" {
		return errors.New("WebMCP selection requires browser_id and target_id")
	}
	if selection.BrowserInstanceID != "" && !isNormalizedBrowserInstanceID(selection.BrowserInstanceID) {
		return errors.New("WebMCP selection browser_instance_id is invalid")
	}
	if selection.Origin != "" {
		selectionOrigin := safeOrigin(selection.Origin)
		if selectionOrigin == "" {
			return errors.New("WebMCP selection origin is invalid")
		}
	}
	if selection.ContinuityMarker != "" && (len(selection.ContinuityMarker) > 128 || strings.ContainsAny(selection.ContinuityMarker, "\r\n\t") || strings.ContainsAny(selection.ContinuityMarker, "/?#") || strings.Contains(selection.ContinuityMarker, "://")) {
		return errors.New("WebMCP selection continuity marker is invalid")
	}
	return nil
}

func isNormalizedBrowserInstanceID(value string) bool {
	const prefix = "incarnation-"
	if len(value) != len(prefix)+24 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range value[len(prefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
