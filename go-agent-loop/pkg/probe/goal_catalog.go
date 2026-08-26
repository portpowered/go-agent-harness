package probe

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
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
	return catalog, nil
}

// LoadCatalog is the concise spelling of LoadGoalCatalog for fleet consumers.
func LoadCatalog() (GoalCatalog, error) { return LoadGoalCatalog() }
