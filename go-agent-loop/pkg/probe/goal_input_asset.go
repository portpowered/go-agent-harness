package probe

import (
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// GoalInputSourceKind identifies how a non-text input is supplied to a goal.
type GoalInputSourceKind string

const (
	// GoalInputSourceEmbeddedAsset is a deterministic asset compiled into the
	// probe package and never read from the caller's working directory.
	GoalInputSourceEmbeddedAsset GoalInputSourceKind = "embedded_asset"
	// GoalInputAssetRedApple is the stable ID of the shipped multimodal image.
	GoalInputAssetRedApple = "red-apple-image"
)

// GoalInputSource declares the deterministic input that a fleet adapter can
// attach for a goal. It is catalog metadata, not an additional blind-probe
// hint or a field on GoalRunInput.
type GoalInputSource struct {
	Kind      GoalInputSourceKind `json:"kind"`
	AssetID   string              `json:"asset_id"`
	MediaType string              `json:"media_type"`
}

// GoalInputAsset is the resolved non-text input for a cataloged goal.
// Bytes are newly allocated for each resolution so callers cannot mutate a
// package-level buffer shared by later runs.
type GoalInputAsset struct {
	ID        string
	MediaType string
	Bytes     []byte
}

var (
	// ErrMissingGoalInputSource identifies a multimodal goal without an input.
	ErrMissingGoalInputSource = errors.New("probe: missing goal input source")
	// ErrInvalidGoalInputSource identifies an unknown or inconsistent source.
	ErrInvalidGoalInputSource = errors.New("probe: invalid goal input source")
	// ErrUnknownGoalRunInput identifies a run input not present in a catalog.
	ErrUnknownGoalRunInput = errors.New("probe: unknown goal run input")
	// ErrGoalRunInputTextMismatch identifies tampered run-input text.
	ErrGoalRunInputTextMismatch = errors.New("probe: goal run input text mismatch")
)

type embeddedGoalInputAssetDefinition struct {
	path      string
	mediaType string
}

var embeddedGoalInputAssets = map[string]embeddedGoalInputAssetDefinition{
	GoalInputAssetRedApple: {
		path:      "testdata/goal_inputs/red_apple.png.b64",
		mediaType: "image/png",
	},
}

// The source is committed as base64 text so the asset remains reviewable while
// the resolver still returns the original image bytes to the fleet adapter.
var (
	//go:embed testdata/goal_inputs/red_apple.png.b64
	shippedGoalInputAssets embed.FS
)

func validateGoalInputSource(index int, goal Goal) error {
	if goal.Capability != CapabilityMultimodalInput {
		if goal.InputSource != nil {
			return &GoalCatalogValidationError{
				Index:  index,
				GoalID: goal.ID,
				Field:  "input_source",
				Kind:   ErrInvalidGoalInputSource,
				Reason: "only multimodal goals may declare an input source",
			}
		}
		return nil
	}
	if goal.InputSource == nil {
		return &GoalCatalogValidationError{
			Index:  index,
			GoalID: goal.ID,
			Field:  "input_source",
			Kind:   ErrMissingGoalInputSource,
			Reason: "multimodal goals require a deterministic input source",
		}
	}
	source := goal.InputSource
	if source.Kind != GoalInputSourceEmbeddedAsset {
		return invalidGoalInputSource(index, goal, fmt.Sprintf("kind must be %q", GoalInputSourceEmbeddedAsset))
	}
	if strings.TrimSpace(source.AssetID) == "" {
		return invalidGoalInputSource(index, goal, "asset_id must be non-empty")
	}
	definition, ok := embeddedGoalInputAssets[source.AssetID]
	if !ok {
		return invalidGoalInputSource(index, goal, fmt.Sprintf("asset_id %q is not embedded", source.AssetID))
	}
	if strings.TrimSpace(source.MediaType) == "" {
		return invalidGoalInputSource(index, goal, "media_type must be non-empty")
	}
	if source.MediaType != definition.mediaType {
		return invalidGoalInputSource(index, goal, fmt.Sprintf("media_type must be %q", definition.mediaType))
	}
	return nil
}

func invalidGoalInputSource(index int, goal Goal, reason string) error {
	return &GoalCatalogValidationError{
		Index:  index,
		GoalID: goal.ID,
		Field:  "input_source",
		Kind:   ErrInvalidGoalInputSource,
		Reason: reason,
	}
}

// LoadGoalInputAsset resolves the embedded input declared by goal. Goals that
// need no non-text input return (nil, nil). The function performs only reads
// from compiled package data and allocates fresh bytes for the caller.
func LoadGoalInputAsset(goal Goal) (*GoalInputAsset, error) {
	if err := validateGoalInputSource(-1, goal); err != nil {
		return nil, err
	}
	if goal.InputSource == nil {
		return nil, nil
	}
	definition := embeddedGoalInputAssets[goal.InputSource.AssetID]
	encoded, err := shippedGoalInputAssets.ReadFile(definition.path)
	if err != nil {
		return nil, fmt.Errorf("read embedded goal input %q: %w", goal.InputSource.AssetID, err)
	}
	data, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(encoded)), ""))
	if err != nil {
		return nil, fmt.Errorf("decode embedded goal input %q: %w", goal.InputSource.AssetID, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("decode embedded goal input %q: empty asset", goal.InputSource.AssetID)
	}
	return &GoalInputAsset{
		ID:        goal.InputSource.AssetID,
		MediaType: goal.InputSource.MediaType,
		Bytes:     append([]byte(nil), data...),
	}, nil
}

// ResolveInputAsset joins a two-field run input back to its catalog metadata.
// It lets a fleet adapter attach a deterministic asset without widening
// GoalRunInput with hidden context. A text, audio, or tool goal returns nil,
// nil because those goals have no catalog-owned non-text attachment.
func (c GoalCatalog) ResolveInputAsset(input GoalRunInput) (*GoalInputAsset, error) {
	for _, goal := range c {
		if goal.ID != input.GoalID {
			continue
		}
		if goal.Text != input.GoalText {
			return nil, fmt.Errorf("%w for goal %q", ErrGoalRunInputTextMismatch, input.GoalID)
		}
		return LoadGoalInputAsset(goal)
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownGoalRunInput, input.GoalID)
}

// ResolveGoalInputAsset is the package-level form of GoalCatalog.ResolveInputAsset.
func ResolveGoalInputAsset(catalog GoalCatalog, input GoalRunInput) (*GoalInputAsset, error) {
	return catalog.ResolveInputAsset(input)
}
