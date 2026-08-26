package fleet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ComposeInput contains the declared fleet dimensions. Scenario files are
// read only for their stable identity; their execution payload remains at the
// committed path recorded in the resulting manifest.
type ComposeInput struct {
	ScenarioFiles []string
	Transports    []Transport
	RepeatCount   int
	Concurrency   int
}

// Options is a compatibility alias for callers that prefer option language.
type Options = ComposeInput

// Composer expands a validated ComposeInput into a complete manifest.
type Composer struct{}

// NewComposer returns a stateless fleet composer.
func NewComposer() Composer { return Composer{} }

// Compose creates one entry for every scenario × transport × repeat-index
// coordinate. Inputs are normalized into sorted order so equivalent sets
// produce byte-equivalent manifests regardless of caller order.
func (Composer) Compose(input ComposeInput) (Manifest, error) {
	scenarios, err := loadScenarioRefs(input.ScenarioFiles)
	if err != nil {
		return Manifest{}, err
	}
	transports, err := normalizeTransports(input.Transports)
	if err != nil {
		return Manifest{}, err
	}
	if input.RepeatCount <= 0 {
		return Manifest{}, validation("repeat_count", fmt.Sprint(input.RepeatCount), "must be greater than zero", ErrInvalidRepeatCount)
	}
	if input.Concurrency <= 0 {
		return Manifest{}, validation("concurrency", fmt.Sprint(input.Concurrency), "must be greater than zero", ErrInvalidConcurrency)
	}
	entryCount, err := crossProductCount(len(scenarios), len(transports), input.RepeatCount)
	if err != nil {
		return Manifest{}, validation("entries", "", "declared cross product is too large", ErrEntryCountOverflow)
	}

	manifest := Manifest{
		SchemaVersion: SchemaVersion,
		Scenarios:     scenarios,
		Transports:    transports,
		RepeatCount:   input.RepeatCount,
		Concurrency:   input.Concurrency,
		Entries:       make([]Entry, 0, entryCount),
	}
	for _, scenario := range scenarios {
		for _, transport := range transports {
			for repeatIndex := 0; repeatIndex < input.RepeatCount; repeatIndex++ {
				manifest.Entries = append(manifest.Entries, Entry{
					ID:           EntryID(scenario.ID, transport, repeatIndex),
					ScenarioID:   scenario.ID,
					ScenarioName: scenario.Name,
					ScenarioPath: scenario.Path,
					Transport:    transport,
					RepeatIndex:  repeatIndex,
				})
			}
		}
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("validate composed fleet: %w", err)
	}
	return manifest, nil
}

// Compose is the function-shaped entry point for callers that do not need to
// retain a Composer value.
func Compose(input ComposeInput) (Manifest, error) {
	return NewComposer().Compose(input)
}

// ComposeFiles is a convenience entry point for string transport names from
// CLI flag parsing or JSON configuration.
func ComposeFiles(scenarioFiles []string, transports []string, repeatCount, concurrency int) (Manifest, error) {
	typed := make([]Transport, len(transports))
	for index, transport := range transports {
		typed[index] = Transport(transport)
	}
	return Compose(ComposeInput{
		ScenarioFiles: scenarioFiles,
		Transports:    typed,
		RepeatCount:   repeatCount,
		Concurrency:   concurrency,
	})
}

// ComposeManifest is a descriptive alias for Compose.
func ComposeManifest(input ComposeInput) (Manifest, error) { return Compose(input) }

func loadScenarioRefs(paths []string) ([]ScenarioRef, error) {
	if len(paths) == 0 {
		return nil, validation("scenario_files", "", "must contain at least one file", ErrNoScenarios)
	}
	refs := make([]ScenarioRef, 0, len(paths))
	seenIDs := make(map[string]string, len(paths))
	for index, rawPath := range paths {
		path := filepath.Clean(strings.TrimSpace(rawPath))
		if path == "." || path == "" {
			return nil, validation(fmt.Sprintf("scenario_files[%d]", index), rawPath, "must name a scenario file", ErrInvalidManifest)
		}
		ref, err := readScenarioRef(path)
		if err != nil {
			return nil, err
		}
		if previousPath, exists := seenIDs[ref.ID]; exists {
			return nil, validation(fmt.Sprintf("scenario_files[%d]", index), ref.ID,
				fmt.Sprintf("duplicates scenario file %q", previousPath), ErrDuplicateScenario)
		}
		seenIDs[ref.ID] = ref.Path
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].ID != refs[j].ID {
			return refs[i].ID < refs[j].ID
		}
		return refs[i].Path < refs[j].Path
	})
	return refs, nil
}

func readScenarioRef(path string) (ScenarioRef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ScenarioRef{}, fmt.Errorf("read scenario file %q: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || object == nil {
		if err == nil {
			err = fmt.Errorf("scenario must be a JSON object")
		}
		return ScenarioRef{}, fmt.Errorf("load scenario file %q: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("contains more than one JSON value")
		}
		return ScenarioRef{}, fmt.Errorf("load scenario file %q: %w", path, err)
	}

	id, err := optionalString(object, "id")
	if err != nil {
		return ScenarioRef{}, fmt.Errorf("load scenario file %q: %w", path, err)
	}
	name, err := optionalString(object, "name")
	if err != nil {
		return ScenarioRef{}, fmt.Errorf("load scenario file %q: %w", path, err)
	}
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if id == "" {
		id = name
	}
	if id == "" {
		return ScenarioRef{}, validation("scenario.id", "", "must be present or derive from a non-empty name", ErrInvalidManifest)
	}
	return ScenarioRef{ID: id, Name: name, Path: filepath.Clean(path)}, nil
}

func optionalString(object map[string]json.RawMessage, field string) (string, error) {
	raw, ok := object[field]
	if !ok {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", validation("scenario."+field, string(raw), "must be a string", ErrInvalidManifest)
	}
	return value, nil
}

func normalizeTransports(raw []Transport) ([]Transport, error) {
	if len(raw) == 0 {
		return nil, validation("transports", "", "must contain at least one transport", ErrNoTransports)
	}
	transports := make([]Transport, 0, len(raw))
	seen := make(map[Transport]struct{}, len(raw))
	for index, candidate := range raw {
		transport, err := ParseTransport(string(candidate))
		if err != nil {
			return nil, validation(fmt.Sprintf("transports[%d]", index), string(candidate), "must be replay or live", ErrUnknownTransport)
		}
		if _, exists := seen[transport]; exists {
			return nil, validation(fmt.Sprintf("transports[%d]", index), string(candidate), "duplicates another transport", ErrDuplicateTransport)
		}
		seen[transport] = struct{}{}
		transports = append(transports, transport)
	}
	sort.Slice(transports, func(i, j int) bool { return transports[i] < transports[j] })
	return transports, nil
}

func crossProductCount(scenarioCount, transportCount, repeatCount int) (int, error) {
	if scenarioCount <= 0 || transportCount <= 0 || repeatCount <= 0 {
		return 0, ErrEntryCountOverflow
	}
	maxInt := int(^uint(0) >> 1)
	if scenarioCount > maxInt/transportCount {
		return 0, ErrEntryCountOverflow
	}
	partial := scenarioCount * transportCount
	if partial > maxInt/repeatCount {
		return 0, ErrEntryCountOverflow
	}
	return partial * repeatCount, nil
}
