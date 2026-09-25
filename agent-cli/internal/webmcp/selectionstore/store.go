// Package selectionstore persists the one opaque WebMCP browser selection
// shared by separate direct command invocations. Records carry only
// normalized IDs and a redacted origin; endpoint credentials and websocket
// paths never cross this boundary.
package selectionstore

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

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

const (
	// FileName is deliberately separate from config.yaml. A selection is
	// ephemeral browser state, not configuration.
	FileName = "webmcp-selection.json"
	// Version is the only supported persisted record version.
	Version = 1

	directoryMode = 0o700
	fileMode      = 0o600

	maxContinuityMarkerLength = 128

	errPathUnavailable = "WebMCP selection path is unavailable"
	errDecodePrefix    = "decode WebMCP selection: %w"
)

// Selection is the persisted record shared by direct command invocations.
type Selection struct {
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

// Store persists and loads one opaque browser selection. Implementations may
// be injected by embedders and command tests.
type Store interface {
	Load() (Selection, error)
	Save(Selection) error
}

// FileStore is the default user-only selection store.
type FileStore struct {
	Path string
}

// NewFileStore constructs a selection store below configDir. The caller owns
// resolving any default configuration directory.
func NewFileStore(configDir string) *FileStore {
	return &FileStore{Path: filepath.Join(configDir, FileName)}
}

// Load reads the persisted selection. A missing file yields the zero record.
func (s *FileStore) Load() (Selection, error) {
	if s == nil || s.Path == "" {
		return Selection{}, errors.New(errPathUnavailable)
	}
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Selection{}, nil
	}
	if err != nil {
		return Selection{}, fmt.Errorf("read WebMCP selection: %w", err)
	}
	selection, err := decode(data)
	if err != nil {
		return Selection{}, err
	}
	if err := validate(selection); err != nil {
		return Selection{}, err
	}
	if selection.Origin != "" {
		selection.Origin = normalize.RedactedOrigin(selection.Origin)
	}
	return selection, nil
}

func decode(data []byte) (Selection, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var selection Selection
	if err := decoder.Decode(&selection); err != nil {
		return Selection{}, fmt.Errorf(errDecodePrefix, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Selection{}, errors.New("decode WebMCP selection: more than one JSON value")
		}
		return Selection{}, fmt.Errorf(errDecodePrefix, err)
	}
	return selection, nil
}

// Save validates and atomically replaces the persisted selection.
func (s *FileStore) Save(selection Selection) error {
	if s == nil || s.Path == "" {
		return errors.New(errPathUnavailable)
	}
	if selection.Version == 0 {
		selection.Version = Version
	}
	if selection.SelectedAt.IsZero() {
		selection.SelectedAt = time.Now().UTC()
	}
	if selection.Origin != "" {
		selection.Origin = normalize.RedactedOrigin(selection.Origin)
	}
	if err := validate(selection); err != nil {
		return err
	}
	directory := filepath.Dir(s.Path)
	if err := os.MkdirAll(directory, directoryMode); err != nil {
		return fmt.Errorf("create WebMCP selection directory: %w", err)
	}
	data, err := json.MarshalIndent(selection, "", "  ")
	if err != nil {
		return fmt.Errorf("encode WebMCP selection: %w", err)
	}
	data = append(data, '\n')
	return replaceFile(directory, s.Path, data)
}

func replaceFile(directory, path string, data []byte) error {
	temporary, err := os.CreateTemp(directory, ".webmcp-selection-*")
	if err != nil {
		return fmt.Errorf("create WebMCP selection temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	// After a successful rename the temporary name no longer exists, so the
	// cleanup's not-found result is expected and carries no information.
	defer func() { _ = os.Remove(temporaryName) }() //nolint:errcheck // Best-effort cleanup of an already-renamed or abandoned temporary file.
	writeErr := writeTemporary(temporary, data)
	closeErr := temporary.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return fmt.Errorf("close WebMCP selection temporary file: %w", closeErr)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace WebMCP selection: %w", err)
	}
	return nil
}

func writeTemporary(temporary *os.File, data []byte) error {
	if err := temporary.Chmod(fileMode); err != nil {
		return fmt.Errorf("protect WebMCP selection temporary file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write WebMCP selection: %w", err)
	}
	return nil
}

func validate(selection Selection) error {
	if selection.Version != Version {
		return fmt.Errorf("WebMCP selection version %d is unsupported", selection.Version)
	}
	if selection.BrowserID == "" || selection.TargetID == "" {
		return errors.New("WebMCP selection requires browser_id and target_id")
	}
	if selection.BrowserInstanceID != "" && !isNormalizedBrowserInstanceID(selection.BrowserInstanceID) {
		return errors.New("WebMCP selection browser_instance_id is invalid")
	}
	if selection.Origin != "" && normalize.RedactedOrigin(selection.Origin) == "" {
		return errors.New("WebMCP selection origin is invalid")
	}
	if selection.ContinuityMarker != "" && !validContinuityMarker(selection.ContinuityMarker) {
		return errors.New("WebMCP selection continuity marker is invalid")
	}
	return nil
}

func validContinuityMarker(marker string) bool {
	return len(marker) <= maxContinuityMarkerLength &&
		!strings.ContainsAny(marker, "\r\n\t") &&
		!strings.ContainsAny(marker, "/?#") &&
		!strings.Contains(marker, "://")
}

func isNormalizedBrowserInstanceID(value string) bool {
	const prefix = "incarnation-"
	const hexDigits = 24
	if len(value) != len(prefix)+hexDigits || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range value[len(prefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
