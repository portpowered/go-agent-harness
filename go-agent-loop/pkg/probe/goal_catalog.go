package probe

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// CapabilityArea identifies the kind of customer interaction a goal exercises.
// The values are stable machine-readable identifiers used by fleet consumers.
type CapabilityArea string

const (
	CapabilityTextInteraction  CapabilityArea = "text_interaction"
	CapabilityAudioInteraction CapabilityArea = "audio_interaction"
	CapabilityToolUse          CapabilityArea = "tool_use"
	CapabilityMultimodalInput  CapabilityArea = "multimodal_input"
)

// Capability is a compatibility alias for callers that use the shorter name.
type Capability = CapabilityArea

// ArtifactExpectation describes objective evidence in a recorded probe
// artifact. It deliberately does not contain a subjective probe verdict.
type ArtifactExpectation struct {
	ArtifactClass string `json:"artifact_class"`
	Description   string `json:"description"`
}

// Goal is one customer request that a blind acceptance probe can attempt.
// Text is the exact plain-English input handed to the probe. Expectation
// identifies the recorded artifact that can prove the goal was attained.
type Goal struct {
	ID          string              `json:"id"`
	Text        string              `json:"text"`
	Capability  CapabilityArea      `json:"capability"`
	Expectation ArtifactExpectation `json:"expectation"`
}

// GoalText returns the exact blind-probe text without adding any fleet hints.
func (g Goal) GoalText() string { return g.Text }

// CapabilityArea returns the typed area exercised by the goal.
func (g Goal) CapabilityArea() CapabilityArea { return g.Capability }

// GoalCatalog is the ordered, machine-readable set of shipped goals.
// Ordering is part of the catalog contract and is stable across loads.
type GoalCatalog []Goal

// Catalog is a shorter compatibility name for GoalCatalog.
type Catalog = GoalCatalog

var (
	// ErrInvalidGoalCatalog is the broad class for rejected goal catalogs.
	ErrInvalidGoalCatalog = errors.New("probe: invalid goal catalog")
	// ErrEmptyGoalCatalog identifies a catalog with no goals.
	ErrEmptyGoalCatalog = errors.New("probe: empty goal catalog")
	// ErrBlankGoalID identifies a goal without a stable ID.
	ErrBlankGoalID = errors.New("probe: blank goal ID")
	// ErrDuplicateGoalID identifies an ID that occurs more than once.
	ErrDuplicateGoalID = errors.New("probe: duplicate goal ID")
	// ErrBlankGoalText identifies a goal without usable plain-English text.
	ErrBlankGoalText = errors.New("probe: blank goal text")
	// ErrMissingGoalExpectation identifies a goal without objective artifact evidence.
	ErrMissingGoalExpectation = errors.New("probe: missing goal expectation")
)

// GoalCatalogValidationError identifies the first invalid catalog entry.
// GoalID names the offending goal when the error concerns an individual goal;
// Index is -1 for catalog-wide errors such as an empty catalog.
type GoalCatalogValidationError struct {
	Index  int
	GoalID string
	Field  string
	Reason string
	Kind   error
}

// Error returns an actionable catalog validation diagnostic.
func (e *GoalCatalogValidationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	location := "catalog"
	if e.Index >= 0 {
		location = fmt.Sprintf("goals[%d]", e.Index)
		if e.GoalID != "" {
			location += fmt.Sprintf(" (goal %q)", e.GoalID)
		}
	}
	if e.Field != "" {
		location += "." + e.Field
	}
	if e.Reason == "" {
		return fmt.Sprintf("%s: invalid", location)
	}
	return fmt.Sprintf("%s: %s", location, e.Reason)
}

// Unwrap exposes the specific validation sentinel to errors.Is.
func (e *GoalCatalogValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Kind
}

// Is makes every catalog validation error match the broad catalog sentinel.
func (e *GoalCatalogValidationError) Is(target error) bool {
	return e != nil && (target == ErrInvalidGoalCatalog || target == e.Kind)
}

// ValidateGoalCatalog rejects catalogs that could silently weaken fleet
// coverage or leave a goal without objective evidence.
func ValidateGoalCatalog(catalog GoalCatalog) error { return catalog.Validate() }

// ValidateCatalog is the concise spelling of ValidateGoalCatalog.
func ValidateCatalog(catalog GoalCatalog) error { return ValidateGoalCatalog(catalog) }

// Validate checks the catalog's structural invariants and returns the first
// failure in deterministic slice order.
func (c GoalCatalog) Validate() error {
	if len(c) == 0 {
		return &GoalCatalogValidationError{
			Index:  -1,
			Field:  "goals",
			Kind:   ErrEmptyGoalCatalog,
			Reason: "must contain at least one goal",
		}
	}

	seen := make(map[string]int, len(c))
	for index, goal := range c {
		if strings.TrimSpace(goal.ID) == "" {
			return &GoalCatalogValidationError{
				Index:  index,
				Field:  "id",
				Kind:   ErrBlankGoalID,
				Reason: "must be non-empty",
			}
		}
		if firstIndex, ok := seen[goal.ID]; ok {
			return &GoalCatalogValidationError{
				Index:  index,
				GoalID: goal.ID,
				Field:  "id",
				Kind:   ErrDuplicateGoalID,
				Reason: fmt.Sprintf("duplicates goal at index %d", firstIndex),
			}
		}
		seen[goal.ID] = index

		if strings.TrimSpace(goal.Text) == "" {
			return &GoalCatalogValidationError{
				Index:  index,
				GoalID: goal.ID,
				Field:  "text",
				Kind:   ErrBlankGoalText,
				Reason: "must be non-empty plain English",
			}
		}
		if strings.TrimSpace(goal.Expectation.ArtifactClass) == "" {
			return &GoalCatalogValidationError{
				Index:  index,
				GoalID: goal.ID,
				Field:  "expectation.artifact_class",
				Kind:   ErrMissingGoalExpectation,
				Reason: "must name the recorded artifact class",
			}
		}
		if strings.TrimSpace(goal.Expectation.Description) == "" {
			return &GoalCatalogValidationError{
				Index:  index,
				GoalID: goal.ID,
				Field:  "expectation.description",
				Kind:   ErrMissingGoalExpectation,
				Reason: "must describe objective recorded evidence",
			}
		}
	}
	return nil
}

// The catalog is committed as readable JSON but compiled into the package so
// loading is hermetic and never depends on the caller's working directory.
var (
	//go:embed testdata/goal_catalog.json
	shippedGoalCatalog embed.FS
)

// LoadGoalCatalog loads the complete embedded goal catalog. It performs no
// network or filesystem I/O at runtime: the only data source is the embedded
// package asset, and each call returns newly decoded values.
func LoadGoalCatalog() (GoalCatalog, error) {
	data, err := shippedGoalCatalog.ReadFile("testdata/goal_catalog.json")
	if err != nil {
		return nil, fmt.Errorf("read embedded goal catalog: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var catalog GoalCatalog
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decode embedded goal catalog: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode embedded goal catalog: multiple JSON values")
		}
		return nil, fmt.Errorf("decode embedded goal catalog: %w", err)
	}
	if catalog == nil {
		return nil, fmt.Errorf("decode embedded goal catalog: catalog must be a JSON array")
	}
	if err := catalog.Validate(); err != nil {
		return nil, fmt.Errorf("validate embedded goal catalog: %w", err)
	}
	return catalog, nil
}

// LoadCatalog is the concise spelling of LoadGoalCatalog for fleet consumers.
func LoadCatalog() (GoalCatalog, error) { return LoadGoalCatalog() }
