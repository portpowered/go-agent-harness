package audiofixture

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

const manifestFile = "manifest.json"

// Loader validates and loads one corpus root. It performs no writes and does
// not cache validation, so every Load observes the current committed bytes.
type Loader struct{ root string }

// NewLoader creates a loader for a corpus directory. An empty root selects the
// repository's committed go-agent-loop/testdata/audio corpus.
func NewLoader(root string) *Loader {
	if root == "" {
		root = defaultCorpusRoot()
	}
	return &Loader{root: filepath.Clean(root)}
}

// New is a concise alias for NewLoader.
func New(root string) *Loader { return NewLoader(root) }

// DefaultLoader creates a loader for the committed corpus.
func DefaultLoader() *Loader { return NewLoader("") }

// DefaultCorpusRoot returns the package-owned committed corpus location.
func DefaultCorpusRoot() string { return defaultCorpusRoot() }

// Load validates the committed corpus and loads the exact manifest ID.
func Load(id string) (*Source, error) { return DefaultLoader().Load(id) }

// LoadFrom loads an exact ID from a caller-selected corpus root. The path is
// configuration for the loader, never an identifier or fallback for Load.
func LoadFrom(root, id string) (*Source, error) { return NewLoader(root).Load(id) }

// LoadFromDir is an explicit alias for LoadFrom.
func LoadFromDir(root, id string) (*Source, error) { return LoadFrom(root, id) }

// Load validates the complete corpus before resolving id, hashing its bytes,
// and decoding the selected PCM16 WAV.
func (l *Loader) Load(id string) (*Source, error) {
	if l == nil {
		return nil, &CorpusIOError{Operation: "load", Path: manifestFile, Err: ErrCorpusIO}
	}
	manifest, err := l.readManifest()
	if err != nil {
		return nil, err
	}
	actual, err := l.actualAudioPaths()
	if err != nil {
		return nil, err
	}
	if err := validateClosedSet(manifest.Files, actual); err != nil {
		return nil, err
	}

	entry, ok := manifest.byID[id]
	if !ok {
		return nil, &UnknownIDError{ID: id}
	}

	relativePath := entry.Path
	fullPath := filepath.Join(l.root, filepath.FromSlash(relativePath))
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, &CorpusIOError{Operation: "read audio", Path: relativePath, Err: err}
	}
	actualHashBytes := sha256.Sum256(data)
	actualHash := hex.EncodeToString(actualHashBytes[:])
	if actualHash != entry.SHA256 {
		return nil, &HashMismatchError{ID: entry.ID, Path: relativePath, Expected: entry.SHA256, Actual: actualHash}
	}

	rate, samples, err := wavio.Read(bytes.NewReader(data))
	if err != nil {
		return nil, &InvalidAudioError{ID: entry.ID, Path: relativePath, Err: err}
	}
	if len(samples) == 0 {
		return nil, &InvalidAudioError{ID: entry.ID, Path: relativePath, Err: wavio.ErrEmptySamples}
	}
	if rate != SampleRate {
		samples, err = wavio.Resample(samples, rate, SampleRate)
		if err != nil {
			return nil, &InvalidAudioError{ID: entry.ID, Path: relativePath, Err: err}
		}
	}
	if len(samples) == 0 {
		return nil, &InvalidAudioError{ID: entry.ID, Path: relativePath, Err: wavio.ErrEmptySamples}
	}
	return newSource(entry.ID, relativePath, samples), nil
}

type manifest struct {
	SchemaVersion   int             `json:"schema_version"`
	CorpusByteTotal *uint64         `json:"corpus_byte_total"`
	VADThresholdRMS *float64        `json:"vad_threshold_rms"`
	Classes         []string        `json:"classes"`
	SampleRatesHz   []int           `json:"sample_rates_hz"`
	Files           []manifestEntry `json:"files"`
	byID            map[string]manifestEntry
}

type manifestEntry struct {
	ID              string          `json:"id"`
	Path            string          `json:"path"`
	Class           string          `json:"class"`
	Source          string          `json:"source"`
	Voice           string          `json:"voice"`
	Structure       string          `json:"structure"`
	Format          *formatMetadata `json:"format"`
	SampleRateHz    int             `json:"sample_rate_hz"`
	Channels        int             `json:"channels"`
	BitsPerSample   int             `json:"bits_per_sample"`
	SampleCount     int             `json:"sample_count"`
	ByteSize        int             `json:"byte_size"`
	DurationSeconds float64         `json:"duration_seconds"`
	RMSEnergy       float64         `json:"rms_energy"`
	SHA256          string          `json:"sha256"`
}

type formatMetadata struct {
	Container     string `json:"container"`
	Encoding      string `json:"encoding"`
	SampleFormat  string `json:"sample_format"`
	Channels      int    `json:"channels"`
	BitsPerSample int    `json:"bits_per_sample"`
}

func (l *Loader) readManifest() (manifest, error) {
	data, err := os.ReadFile(filepath.Join(l.root, manifestFile))
	if err != nil {
		return manifest{}, malformedManifest(manifestFile, "file", "cannot read manifest", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document manifest
	if err := decoder.Decode(&document); err != nil {
		return manifest{}, malformedManifest(manifestFile, "json", "invalid JSON: "+err.Error(), err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return manifest{}, malformedManifest(manifestFile, "json", "multiple JSON values", nil)
		}
		return manifest{}, malformedManifest(manifestFile, "json", "trailing JSON: "+err.Error(), err)
	}
	if err := validateManifest(&document); err != nil {
		return manifest{}, err
	}
	return document, nil
}

func validateManifest(document *manifest) error {
	if document.SchemaVersion != 1 {
		return malformedManifest(manifestFile, "schema_version", fmt.Sprintf("got %d, want 1", document.SchemaVersion), nil)
	}
	if len(document.Files) == 0 {
		return malformedManifest(manifestFile, "files", "must contain at least one entry", nil)
	}
	document.byID = make(map[string]manifestEntry, len(document.Files))
	paths := make(map[string]string, len(document.Files))
	for index, entry := range document.Files {
		field := fmt.Sprintf("files[%d]", index)
		if entry.ID == "" || strings.TrimSpace(entry.ID) != entry.ID || strings.ContainsAny(entry.ID, "/\\\x00") {
			return malformedManifest(manifestFile, field+".id", "must be a non-empty semantic ID", nil)
		}
		if _, exists := document.byID[entry.ID]; exists {
			return malformedManifest(manifestFile, field+".id", "duplicates another fixture ID", nil)
		}
		if !safeAudioPath(entry.Path) {
			return malformedManifest(manifestFile, field+".path", "must be a safe relative .wav path", nil)
		}
		if previousID, exists := paths[entry.Path]; exists {
			return malformedManifest(manifestFile, field+".path", "duplicates fixture "+previousID, nil)
		}
		paths[entry.Path] = entry.ID
		if len(entry.SHA256) != sha256.Size*2 || entry.SHA256 != strings.ToLower(entry.SHA256) {
			return malformedManifest(manifestFile, field+".sha256", "must be a lowercase SHA-256", nil)
		}
		if _, err := hex.DecodeString(entry.SHA256); err != nil {
			return malformedManifest(manifestFile, field+".sha256", "must be hexadecimal", err)
		}
		if entry.SampleRateHz != 0 && entry.SampleRateHz != 16000 && entry.SampleRateHz != 24000 {
			return malformedManifest(manifestFile, field+".sample_rate_hz", "must be 16000 or 24000", nil)
		}
		if entry.Channels != 0 && entry.Channels != Channels {
			return malformedManifest(manifestFile, field+".channels", "must be mono", nil)
		}
		if entry.BitsPerSample != 0 && entry.BitsPerSample != 16 {
			return malformedManifest(manifestFile, field+".bits_per_sample", "must be PCM16", nil)
		}
		if entry.SampleCount < 0 || entry.DurationSeconds < 0 || entry.RMSEnergy < 0 {
			return malformedManifest(manifestFile, field, "numeric metadata cannot be negative", nil)
		}
		if entry.Format != nil {
			if entry.Format.Container != "" && entry.Format.Container != "wav" {
				return malformedManifest(manifestFile, field+".format.container", "must be wav", nil)
			}
			if entry.Format.Encoding != "" && entry.Format.Encoding != "PCM" {
				return malformedManifest(manifestFile, field+".format.encoding", "must be PCM", nil)
			}
			if entry.Format.SampleFormat != "" && entry.Format.SampleFormat != "s16le" {
				return malformedManifest(manifestFile, field+".format.sample_format", "must be s16le", nil)
			}
			if entry.Format.Channels != 0 && entry.Format.Channels != Channels {
				return malformedManifest(manifestFile, field+".format.channels", "must be mono", nil)
			}
			if entry.Format.BitsPerSample != 0 && entry.Format.BitsPerSample != 16 {
				return malformedManifest(manifestFile, field+".format.bits_per_sample", "must be PCM16", nil)
			}
		}
		document.byID[entry.ID] = entry
	}
	return nil
}

func safeAudioPath(value string) bool {
	if value == "" || strings.ContainsAny(value, "\\\x00") || pathpkg.IsAbs(value) || filepath.IsAbs(value) {
		return false
	}
	clean := pathpkg.Clean(value)
	if clean != value || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	return strings.EqualFold(pathpkg.Ext(clean), ".wav")
}

func (l *Loader) actualAudioPaths() ([]string, error) {
	var paths []string
	err := filepath.WalkDir(l.root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == l.root || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(name), ".wav") {
			return nil
		}
		relative, err := filepath.Rel(l.root, name)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return nil, &CorpusIOError{Operation: "enumerate audio", Path: ".", Err: err}
	}
	sort.Strings(paths)
	return paths, nil
}

func validateClosedSet(entries []manifestEntry, actual []string) error {
	declared := make(map[string]manifestEntry, len(entries))
	for _, entry := range entries {
		declared[entry.Path] = entry
	}
	for _, entry := range entries {
		if !containsSorted(actual, entry.Path) {
			return &MissingFileError{ID: entry.ID, Path: entry.Path}
		}
	}
	for _, path := range actual {
		if _, exists := declared[path]; !exists {
			return &UnmanifestedFileError{Path: path}
		}
	}
	return nil
}

func containsSorted(values []string, wanted string) bool {
	index := sort.SearchStrings(values, wanted)
	return index < len(values) && values[index] == wanted
}

func malformedManifest(path, field, reason string, err error) error {
	return &MalformedManifestError{Path: path, Field: field, Reason: reason, Err: err}
}

func defaultCorpusRoot() string {
	if _, sourceFile, _, ok := runtime.Caller(0); ok {
		return filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "testdata", "audio"))
	}
	workingDirectory, err := os.Getwd()
	if err == nil {
		return filepath.Join(workingDirectory, "go-agent-loop", "testdata", "audio")
	}
	return filepath.Join("go-agent-loop", "testdata", "audio")
}
