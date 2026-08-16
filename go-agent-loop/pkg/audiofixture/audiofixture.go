// Package audiofixture loads hash-verified, framed speech audio by manifest ID.
package audiofixture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	SampleRate   = 16000
	Channels     = 1
	FrameSize    = 480
	manifestFile = "manifest.json"
)

type fixtureError string

func (e fixtureError) Error() string { return string(e) }

type UnknownIDError struct {
	fixtureError
	ID string
}
type MalformedManifestError struct {
	fixtureError
	Path, Field, Reason string
}
type MissingFileError struct {
	fixtureError
	ID, Path string
}
type UnmanifestedFileError struct {
	fixtureError
	Path string
}
type HashMismatchError struct {
	fixtureError
	ID, Path, Expected, Actual string
}
type Source struct {
	ID         string
	SampleRate int
	Channels   int
	samples    []int16
	position   int
}

func (s *Source) ReadFrame(_ context.Context, buf []int16) error {
	if len(buf) != FrameSize {
		return fmt.Errorf("audio fixture frame has %d samples; want exactly %d", len(buf), FrameSize)
	}
	if s == nil || s.position >= len(s.samples) {
		return io.EOF
	}
	clear(buf)
	end := s.position + FrameSize
	if end > len(s.samples) {
		end = len(s.samples)
	}
	copy(buf, s.samples[s.position:end])
	s.position = end
	return nil
}

type Loader struct{ root string }

func NewLoader(root string) *Loader {
	if root == "" {
		_, file, _, _ := runtime.Caller(0)
		root = filepath.Join(filepath.Dir(file), "..", "..", "testdata", "audio")
	}
	return &Loader{root: filepath.Clean(root)}
}
func Load(id string) (*Source, error) { return NewLoader("").Load(id) }
func (l *Loader) Load(id string) (*Source, error) {
	m, err := l.readManifest()
	if err != nil {
		return nil, err
	}
	actual, err := l.audioPaths()
	if err != nil {
		return nil, err
	}
	if err := validateClosedSet(m.Files, actual); err != nil {
		return nil, err
	}
	var entry manifestEntry
	for _, candidate := range m.Files {
		if candidate.ID == id {
			entry = candidate
			break
		}
	}
	if entry.ID == "" {
		return nil, &UnknownIDError{fixtureError: fixtureError(fmt.Sprintf("unknown audio fixture ID %q", id)), ID: id}
	}
	data, err := os.ReadFile(filepath.Join(l.root, filepath.FromSlash(entry.Path)))
	if err != nil {
		return nil, fmt.Errorf("read audio fixture %q: %w", entry.Path, err)
	}
	digest := sha256.Sum256(data)
	actualHash := hex.EncodeToString(digest[:])
	if actualHash != entry.SHA256 {
		return nil, &HashMismatchError{fixtureError: fixtureError(fmt.Sprintf("fixture %q hash mismatch %q: %s != %s", entry.ID, entry.Path, entry.SHA256, actualHash)), ID: entry.ID, Path: entry.Path, Expected: entry.SHA256, Actual: actualHash}
	}
	rate, samples, err := wavio.Read(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode audio fixture %q: %w", entry.Path, err)
	}
	if rate != SampleRate {
		samples, err = wavio.Resample(samples, rate, SampleRate)
		if err != nil {
			return nil, fmt.Errorf("resample audio fixture %q: %w", entry.Path, err)
		}
	}
	return &Source{ID: entry.ID, SampleRate: SampleRate, Channels: Channels, samples: samples}, nil
}

type manifest struct {
	SchemaVersion int             `json:"schema_version"`
	Files         []manifestEntry `json:"files"`
}
type manifestEntry struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func (l *Loader) readManifest() (manifest, error) {
	data, err := os.ReadFile(filepath.Join(l.root, manifestFile))
	if err != nil {
		return manifest{}, malformed(manifestFile, "file", "cannot read manifest")
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return manifest{}, malformed(manifestFile, "json", "invalid JSON")
	}
	if err := validateManifest(&m); err != nil {
		return manifest{}, err
	}
	return m, nil
}
func validateManifest(m *manifest) error {
	if m.SchemaVersion != 1 {
		return malformed(manifestFile, "schema_version", "want 1")
	}
	if len(m.Files) == 0 {
		return malformed(manifestFile, "files", "must not be empty")
	}
	ids := map[string]bool{}
	for i, e := range m.Files {
		field := fmt.Sprintf("files[%d]", i)
		if e.ID == "" || strings.TrimSpace(e.ID) != e.ID || strings.ContainsAny(e.ID, "/\\\x00") {
			return malformed(manifestFile, field+".id", "must be a semantic ID")
		}
		if ids[e.ID] {
			return malformed(manifestFile, field+".id", "duplicates another ID")
		}
		if !safeAudioPath(e.Path) {
			return malformed(manifestFile, field+".path", "must be a safe relative .wav path")
		}
		if len(e.SHA256) != sha256.Size*2 || e.SHA256 != strings.ToLower(e.SHA256) {
			return malformed(manifestFile, field+".sha256", "must be lowercase SHA-256")
		}
		if _, err := hex.DecodeString(e.SHA256); err != nil {
			return malformed(manifestFile, field+".sha256", "must be hexadecimal")
		}
		ids[e.ID] = true
	}
	return nil
}
func safeAudioPath(value string) bool {
	clean := pathpkg.Clean(value)
	return value != "" && !strings.ContainsAny(value, ":\\\x00") && !pathpkg.IsAbs(value) && !filepath.IsAbs(value) && clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../") && strings.EqualFold(pathpkg.Ext(clean), ".wav")
}
func (l *Loader) audioPaths() ([]string, error) {
	var paths []string
	err := filepath.WalkDir(l.root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() || !strings.EqualFold(filepath.Ext(name), ".wav") {
			return nil
		}
		paths = append(paths, filepath.ToSlash(strings.TrimPrefix(name, l.root+string(filepath.Separator))))
		return nil
	})
	if err != nil {
		return nil, malformed(".", "corpus", "cannot enumerate audio")
	}
	return paths, nil
}
func validateClosedSet(entries []manifestEntry, actual []string) error {
	actualSet := make(map[string]bool, len(actual))
	for _, path := range actual {
		actualSet[path] = true
	}
	for _, entry := range entries {
		if !actualSet[entry.Path] {
			return &MissingFileError{fixtureError: fixtureError(fmt.Sprintf("fixture %q missing file %q", entry.ID, entry.Path)), ID: entry.ID, Path: entry.Path}
		}
	}
	declared := make(map[string]bool, len(entries))
	for _, entry := range entries {
		declared[entry.Path] = true
	}
	for _, path := range actual {
		if !declared[path] {
			return &UnmanifestedFileError{fixtureError: fixtureError(fmt.Sprintf("unmanifested audio %q", path)), Path: path}
		}
	}
	return nil
}
func malformed(path, field, reason string) error {
	return &MalformedManifestError{fixtureError: fixtureError(fmt.Sprintf("malformed manifest %q %s: %s", path, field, reason)), Path: path, Field: field, Reason: reason}
}
