package tools

import (
	"encoding/json"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// optionalAs returns value as T, or T's zero value when value is absent or has
// another shape. Schemas and arguments are decoded JSON, so an optional field
// of the wrong shape is treated exactly like a missing one.
func optionalAs[T any](value any) T {
	if typed, ok := value.(T); ok {
		return typed
	}
	var zero T
	return zero
}

// decodeBestEffort decodes an advisory schema fragment into target. A fragment
// of an unexpected shape keeps whatever fields did decode; callers fall back
// to defaults for the rest.
func decodeBestEffort(raw []byte, target any) {
	if err := json.Unmarshal(raw, target); err != nil {
		return
	}
}

func filterTargets(targets []webmcp.Target, originContains string, eligibleOnly bool) []webmcp.Target {
	filtered := make([]webmcp.Target, 0, len(targets))
	for _, target := range targets {
		if eligibleOnly && !target.Eligible {
			continue
		}
		if originContains != "" && !strings.Contains(target.Origin, originContains) {
			continue
		}
		filtered = append(filtered, target)
	}
	return filtered
}

func targetDataList(targets []webmcp.Target) []targetData {
	result := make([]targetData, 0, len(targets))
	for _, target := range targets {
		result = append(result, targetData{
			BrowserID:         target.BrowserID,
			TargetID:          target.ID,
			Type:              target.Type,
			Title:             target.Title,
			URL:               target.URL,
			Origin:            target.Origin,
			Attached:          target.Attached,
			Eligible:          target.Eligible,
			EligibilityReason: target.EligibilityReason,
		})
	}
	return result
}
