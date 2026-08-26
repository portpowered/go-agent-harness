// Package fleet defines the typed manifest used to compose and execute probe
// scenario fleets.
package fleet

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	// SchemaVersion is the on-disk manifest schema written by this package.
	SchemaVersion = 1
	// ManifestVersion is retained as a descriptive alias for callers that use
	// version terminology rather than schema terminology.
	ManifestVersion = SchemaVersion
)

// Transport identifies the execution path for one manifest entry.
type Transport string

const (
	TransportReplay Transport = "replay"
	TransportLive   Transport = "live"

	// ReplayTransport and LiveTransport are concise aliases for callers that
	// already have the transport context in their type or function name.
	ReplayTransport = TransportReplay
	LiveTransport   = TransportLive
)

var (
	ErrInvalidManifest    = errors.New("invalid fleet manifest")
	ErrUnknownTransport   = errors.New("unknown fleet transport")
	ErrInvalidRepeatCount = errors.New("invalid fleet repeat count")
	ErrInvalidConcurrency = errors.New("invalid fleet concurrency")
	ErrNoScenarios        = errors.New("fleet manifest requires at least one scenario")
	ErrNoTransports       = errors.New("fleet manifest requires at least one transport")
	ErrDuplicateScenario  = errors.New("fleet manifest contains duplicate scenarios")
	ErrDuplicateTransport = errors.New("fleet manifest contains duplicate transports")
	ErrDuplicateEntry     = errors.New("fleet manifest contains duplicate entries")
	ErrEntryCountOverflow = errors.New("fleet manifest entry count overflows int")
)

// ValidationError identifies the exact manifest or composition field that
// made an input invalid. Cause is exposed through errors.Is so callers can
// branch on a stable category without parsing the rendered message.
type ValidationError struct {
	Field   string
	Value   string
	Problem string
	Cause   error
}

func (e *ValidationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := "fleet manifest field " + fmt.Sprintf("%q", e.Field)
	if e.Value != "" {
		message += " " + fmt.Sprintf("%q", e.Value)
	}
	if e.Problem != "" {
		message += ": " + e.Problem
	}
	return message
}

func (e *ValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *ValidationError) Is(target error) bool {
	if e == nil {
		return false
	}
	return target == ErrInvalidManifest || target == e.Cause
}

// ManifestError is an alias for callers that prefer the artifact's domain
// name over the validation-oriented name.
type ManifestError = ValidationError

// ScenarioRef identifies a committed scenario file without copying its
// execution payload into every entry. Path is kept in the manifest so a
// runner can resolve the exact source that was composed.
type ScenarioRef struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Path string `json:"path"`
}

// Scenario is a compatibility alias for callers that use the shorter name.
type Scenario = ScenarioRef

// Entry is one explicit scenario × transport × repeat combination. Repeat
// indexes are zero-based and are part of the stable entry ID.
type Entry struct {
	ID           string    `json:"id"`
	ScenarioID   string    `json:"scenario_id"`
	ScenarioName string    `json:"scenario_name,omitempty"`
	ScenarioPath string    `json:"scenario_path"`
	Transport    Transport `json:"transport"`
	RepeatIndex  int       `json:"repeat_index"`
}

// Coordinates returns the full identity of an entry in a readable form.
func (e Entry) Coordinates() string {
	return fmt.Sprintf("scenario=%s transport=%s repeat=%d", e.ScenarioID, e.Transport, e.RepeatIndex)
}

// EntryID returns the canonical ID for one fleet coordinate.
func EntryID(scenarioID string, transport Transport, repeatIndex int) string {
	return fmt.Sprintf("%s/%s/repeat-%d", scenarioID, transport, repeatIndex)
}

// Manifest is a complete, explicit fleet plan. Entries is the authoritative
// execution list; the other fields preserve the inputs used to derive it.
type Manifest struct {
	SchemaVersion int           `json:"schema_version"`
	Scenarios     []ScenarioRef `json:"scenarios"`
	Transports    []Transport   `json:"transports"`
	RepeatCount   int           `json:"repeat_count"`
	Concurrency   int           `json:"concurrency"`
	Entries       []Entry       `json:"entries"`
}

// EntryCount returns the number of explicit execution entries.
func (m Manifest) EntryCount() int { return len(m.Entries) }

// Validate checks the full manifest shape, including entry-for-entry
// reconciliation with its declared cross product. This makes a partially
// populated manifest unusable by a future runner.
func (m Manifest) Validate() error {
	if m.SchemaVersion != SchemaVersion {
		return validation("schema_version", fmt.Sprint(m.SchemaVersion),
			fmt.Sprintf("must be %d", SchemaVersion), ErrInvalidManifest)
	}
	if len(m.Scenarios) == 0 {
		return validation("scenarios", "", "must not be empty", ErrNoScenarios)
	}
	if len(m.Transports) == 0 {
		return validation("transports", "", "must not be empty", ErrNoTransports)
	}
	if m.RepeatCount <= 0 {
		return validation("repeat_count", fmt.Sprint(m.RepeatCount), "must be greater than zero", ErrInvalidRepeatCount)
	}
	if m.Concurrency <= 0 {
		return validation("concurrency", fmt.Sprint(m.Concurrency), "must be greater than zero", ErrInvalidConcurrency)
	}

	for index, scenario := range m.Scenarios {
		field := fmt.Sprintf("scenarios[%d]", index)
		if strings.TrimSpace(scenario.ID) == "" {
			return validation(field+".id", "", "must not be empty", ErrInvalidManifest)
		}
		if strings.TrimSpace(scenario.Path) == "" {
			return validation(field+".path", "", "must not be empty", ErrInvalidManifest)
		}
		if index > 0 && m.Scenarios[index-1].ID >= scenario.ID {
			return validation(field+".id", scenario.ID, "scenarios must be sorted by ID and unique", ErrDuplicateScenario)
		}
	}

	for index, rawTransport := range m.Transports {
		transport, err := ParseTransport(string(rawTransport))
		if err != nil {
			return validation(fmt.Sprintf("transports[%d]", index), string(rawTransport), "must be a supported transport", ErrUnknownTransport)
		}
		if transport != rawTransport {
			return validation(fmt.Sprintf("transports[%d]", index), string(rawTransport), "must use its canonical spelling", ErrInvalidManifest)
		}
		if index > 0 && m.Transports[index-1] >= rawTransport {
			return validation(fmt.Sprintf("transports[%d]", index), string(rawTransport), "transports must be sorted and unique", ErrDuplicateTransport)
		}
	}

	wantCount, err := crossProductCount(len(m.Scenarios), len(m.Transports), m.RepeatCount)
	if err != nil {
		return validation("entries", "", "declared cross product is too large", ErrEntryCountOverflow)
	}
	if len(m.Entries) != wantCount {
		return validation("entries", fmt.Sprint(len(m.Entries)),
			fmt.Sprintf("must contain exactly %d entries for the declared cross product", wantCount), ErrInvalidManifest)
	}

	seen := make(map[string]struct{}, len(m.Entries))
	entryIndex := 0
	for _, scenario := range m.Scenarios {
		for _, transport := range m.Transports {
			for repeatIndex := 0; repeatIndex < m.RepeatCount; repeatIndex++ {
				entry := m.Entries[entryIndex]
				if _, exists := seen[entry.ID]; exists {
					return validation(fmt.Sprintf("entries[%d].id", entryIndex), entry.ID, "must be unique", ErrDuplicateEntry)
				}
				seen[entry.ID] = struct{}{}
				want := Entry{
					ID:           EntryID(scenario.ID, transport, repeatIndex),
					ScenarioID:   scenario.ID,
					ScenarioName: scenario.Name,
					ScenarioPath: scenario.Path,
					Transport:    transport,
					RepeatIndex:  repeatIndex,
				}
				if entry != want {
					return validation(fmt.Sprintf("entries[%d]", entryIndex), entry.ID,
						fmt.Sprintf("must encode %s", want.Coordinates()), ErrInvalidManifest)
				}
				entryIndex++
			}
		}
	}
	return nil
}

// ParseManifest strictly decodes one JSON manifest and validates its
// cross-product invariants.
func ParseManifest(data []byte) (Manifest, error) {
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, validation("document", "", err.Error(), ErrInvalidManifest)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errors.New("contains more than one JSON value")
		}
		return Manifest{}, validation("document", "", err.Error(), ErrInvalidManifest)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// ReadManifest reads and validates a manifest file.
func ReadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read fleet manifest %q: %w", path, err)
	}
	return ParseManifest(data)
}

// ParseTransport normalizes a user-facing transport name and rejects names
// that the fleet runner cannot execute.
func ParseTransport(raw string) (Transport, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(TransportReplay):
		return TransportReplay, nil
	case string(TransportLive):
		return TransportLive, nil
	default:
		return "", validation("transport", raw, "must be replay or live", ErrUnknownTransport)
	}
}

func validation(field, value, problem string, cause error) *ValidationError {
	return &ValidationError{Field: field, Value: value, Problem: problem, Cause: cause}
}
