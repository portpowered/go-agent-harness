package probe

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
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

// Stable IDs are part of the acceptance fleet's aggregation contract. Keep
// the required set in code as well as in the embedded data so deleting a goal
// from the data cannot silently reduce fleet coverage.
const (
	GoalIDTextHelpfulAnswer         = "text-helpful-answer"
	GoalIDAudioSpokenAnswer         = "audio-spoken-answer"
	GoalIDToolListCurrentFolder     = "tool-list-current-folder"
	GoalIDMultimodalDescribePicture = "multimodal-describe-picture"
)

type requiredGoalDefinition struct {
	id         string
	capability CapabilityArea
}

var requiredGoalDefinitions = [...]requiredGoalDefinition{
	{id: GoalIDTextHelpfulAnswer, capability: CapabilityTextInteraction},
	{id: GoalIDAudioSpokenAnswer, capability: CapabilityAudioInteraction},
	{id: GoalIDToolListCurrentFolder, capability: CapabilityToolUse},
	{id: GoalIDMultimodalDescribePicture, capability: CapabilityMultimodalInput},
}

// GoalInputSourceKind identifies how a non-text input is supplied to a goal.
type GoalInputSourceKind string

const (
	GoalInputSourceEmbeddedAsset GoalInputSourceKind = "embedded_asset"
	GoalInputAssetRedApple                           = "red-apple-image"
)

// GoalInputSource is catalog-owned non-text input. Data is decoded from the
// JSON asset as bytes, so a fleet adapter can attach it without a path, network
// lookup, or extra field on GoalRunInput.
type GoalInputSource struct {
	Kind      GoalInputSourceKind `json:"kind"`
	AssetID   string              `json:"asset_id"`
	MediaType string              `json:"media_type"`
	Data      []byte              `json:"data"`
}

// ArtifactExpectation describes objective evidence in a recorded probe
// artifact. It deliberately does not contain a subjective probe verdict.
type ArtifactExpectation struct {
	ArtifactClass string `json:"artifact_class"`
	Description   string `json:"description"`
}

// Goal is one customer request that a blind acceptance probe can attempt.
// Text is the exact plain-English input handed to the probe. Expectation
// identifies the recorded artifact that can prove the goal was attained.
// InputSource names a deterministic non-text attachment when the goal needs
// one; fleet run inputs still contain only ID and Text.
type Goal struct {
	ID          string              `json:"id"`
	Text        string              `json:"text"`
	Capability  CapabilityArea      `json:"capability"`
	Expectation ArtifactExpectation `json:"expectation"`
	InputSource *GoalInputSource    `json:"input_source,omitempty"`
}

// GoalText returns the exact blind-probe text without adding any fleet hints.
func (g Goal) GoalText() string { return g.Text }

// CapabilityArea returns the typed area exercised by the goal.
func (g Goal) CapabilityArea() CapabilityArea { return g.Capability }

// GoalCatalog is the ordered, machine-readable set of shipped goals.
// Ordering is part of the catalog contract and is stable across loads.
type GoalCatalog []Goal

var (
	// ErrInvalidGoalCatalog is the broad class for rejected goal catalogs.
	ErrInvalidGoalCatalog = errors.New("probe: invalid goal catalog")
	// ErrEmptyGoalCatalog identifies a catalog with no goals.
	ErrEmptyGoalCatalog = errors.New("probe: empty goal catalog")
	// ErrBlankGoalID identifies a goal without a stable ID.
	ErrBlankGoalID = errors.New("probe: blank goal ID")
	// ErrMissingGoalID identifies a required shipped goal that was removed.
	ErrMissingGoalID = errors.New("probe: missing required goal ID")
	// ErrDuplicateGoalID identifies an ID that occurs more than once.
	ErrDuplicateGoalID = errors.New("probe: duplicate goal ID")
	// ErrBlankGoalText identifies a goal without usable plain-English text.
	ErrBlankGoalText = errors.New("probe: blank goal text")
	// ErrBlankGoalCapability identifies a goal without a capability area.
	ErrBlankGoalCapability = errors.New("probe: blank goal capability")
	// ErrUnknownGoalCapability identifies a capability outside the supported set.
	ErrUnknownGoalCapability = errors.New("probe: unknown goal capability")
	// ErrGoalCapabilityMismatch identifies a required goal assigned to another area.
	ErrGoalCapabilityMismatch = errors.New("probe: goal capability mismatch")
	// ErrMissingGoalInputSource identifies a multimodal goal without an input.
	ErrMissingGoalInputSource = errors.New("probe: missing goal input source")
	// ErrInvalidGoalInputSource identifies an unknown or incomplete input.
	ErrInvalidGoalInputSource = errors.New("probe: invalid goal input source")
	// ErrMissingGoalExpectation identifies a goal without objective artifact evidence.
	ErrMissingGoalExpectation = errors.New("probe: missing goal expectation")
	// ErrGoalTextNotBlindProbeReady identifies goal text that gives a probe
	// implementation or repository hints instead of a customer request.
	ErrGoalTextNotBlindProbeReady = errors.New("probe: goal text is not blind-probe-ready")
)

var blindProbeGoalTextRules = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{
		name:    "internal package vocabulary",
		pattern: regexp.MustCompile(`(?i)(?:^|[^[:alnum:]_])(?:agent-cli|go-agent-loop|go-llm-gateway|internal|pkg|probe)(?:[^[:alnum:]_]|$)`),
	},
	{
		name:    "flag spelling",
		pattern: regexp.MustCompile(`(?:^|[[:space:]])-{1,2}[[:alpha:]][[:alnum:]-]*(?:$|[[:space:][:punct:]])`),
	},
	{
		name:    "repository file path",
		pattern: regexp.MustCompile(`(?i)(?:^|[^[:alnum:]_])(?:\.{0,2}[\\/]|[a-z]:[\\/])|\b[[:alnum:]_.-]+\.(?:go|md|json|mod|yaml|yml|txt)\b`),
	},
	{
		name:    "program documentation reference",
		pattern: regexp.MustCompile(`(?i)(?:^|[^[:alnum:]_])(?:readme|documentation|docs|manual|manifest|prd|program rules)(?:[^[:alnum:]_]|$)`),
	},
}

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
	}
	if e.GoalID != "" {
		location += fmt.Sprintf(" (goal %q)", e.GoalID)
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

func catalogValidationError(index int, goalID, field string, kind error, reason string) error {
	return &GoalCatalogValidationError{Index: index, GoalID: goalID, Field: field, Kind: kind, Reason: reason}
}

// Validate checks the catalog's structural invariants and returns the first
// failure in deterministic slice order.
func (c GoalCatalog) Validate() error {
	if len(c) == 0 {
		return catalogValidationError(-1, "", "goals", ErrEmptyGoalCatalog, "must contain at least one goal")
	}

	seen := make(map[string]int, len(c))
	for index, goal := range c {
		if strings.TrimSpace(goal.ID) == "" {
			return catalogValidationError(index, "", "id", ErrBlankGoalID, "must be non-empty")
		}
		if firstIndex, ok := seen[goal.ID]; ok {
			return catalogValidationError(index, goal.ID, "id", ErrDuplicateGoalID, fmt.Sprintf("duplicates goal at index %d", firstIndex))
		}
		seen[goal.ID] = index

		if strings.TrimSpace(goal.Text) == "" {
			return catalogValidationError(index, goal.ID, "text", ErrBlankGoalText, "must be non-empty plain English")
		}
		if strings.TrimSpace(string(goal.Capability)) == "" {
			return catalogValidationError(index, goal.ID, "capability", ErrBlankGoalCapability, "must name a supported capability area")
		}
		if !isSupportedCapability(goal.Capability) {
			return catalogValidationError(index, goal.ID, "capability", ErrUnknownGoalCapability, fmt.Sprintf("%q is not supported", goal.Capability))
		}
		if reason := blindProbeGoalTextViolation(goal.Text); reason != "" {
			return catalogValidationError(index, goal.ID, "text", ErrGoalTextNotBlindProbeReady, reason)
		}
		if strings.TrimSpace(goal.Expectation.ArtifactClass) == "" {
			return catalogValidationError(index, goal.ID, "expectation.artifact_class", ErrMissingGoalExpectation, "must name the recorded artifact class")
		}
		if strings.TrimSpace(goal.Expectation.Description) == "" {
			return catalogValidationError(index, goal.ID, "expectation.description", ErrMissingGoalExpectation, "must describe objective recorded evidence")
		}
		if goal.Capability == CapabilityMultimodalInput {
			if goal.InputSource == nil {
				return &GoalCatalogValidationError{Index: index, GoalID: goal.ID, Field: "input_source", Kind: ErrMissingGoalInputSource, Reason: "multimodal goals require a deterministic image"}
			}
			if goal.InputSource.Kind != GoalInputSourceEmbeddedAsset || goal.InputSource.AssetID != GoalInputAssetRedApple || goal.InputSource.MediaType != "image/png" || len(goal.InputSource.Data) == 0 {
				return &GoalCatalogValidationError{Index: index, GoalID: goal.ID, Field: "input_source", Kind: ErrInvalidGoalInputSource, Reason: "must contain the shipped red apple PNG asset"}
			}
		} else if goal.InputSource != nil {
			return &GoalCatalogValidationError{Index: index, GoalID: goal.ID, Field: "input_source", Kind: ErrInvalidGoalInputSource, Reason: "only multimodal goals may declare an input source"}
		}
	}

	// Structural validation above protects each entry. This second pass protects
	// the fleet contract itself: every canonical goal must still be present and
	// must remain assigned to its declared capability area.
	for _, required := range requiredGoalDefinitions {
		index, ok := seen[required.id]
		if !ok {
			return catalogValidationError(-1, required.id, "id", ErrMissingGoalID, "required by the shipped acceptance catalog")
		}
		if c[index].Capability != required.capability {
			return catalogValidationError(index, required.id, "capability", ErrGoalCapabilityMismatch, fmt.Sprintf("must be %q", required.capability))
		}
	}
	return nil
}

func isSupportedCapability(capability CapabilityArea) bool {
	switch capability {
	case CapabilityTextInteraction, CapabilityAudioInteraction, CapabilityToolUse, CapabilityMultimodalInput:
		return true
	default:
		return false
	}
}

func blindProbeGoalTextViolation(text string) string {
	if strings.ContainsAny(text, "\r\n") {
		return "must be a single-line customer request"
	}
	for _, rule := range blindProbeGoalTextRules {
		if rule.pattern.MatchString(text) {
			return "must not contain " + rule.name
		}
	}
	return ""
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
