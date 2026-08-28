package probe

import (
	"os"
	"path/filepath"
	"strings"
)

func canonicalScenarioV2Dir(scenarioPath string) (string, error) {
	if strings.TrimSpace(scenarioPath) == "" {
		return "", newScenarioV2Error("scenario", "scenario path is required when fixture references are present")
	}
	absolute, err := filepath.Abs(scenarioPath)
	if err != nil {
		return "", newScenarioV2Error("scenario", "cannot make scenario path absolute")
	}
	directory := filepath.Dir(absolute)
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", newScenarioV2Error("scenario", "cannot canonicalize containing directory")
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", newScenarioV2Error("scenario", "cannot make containing directory absolute")
	}
	return filepath.Clean(canonical), nil
}

func resolveScenarioV2FixturePathFromRoot(root, reference string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", &ScenarioV2Error{Cause: ErrScenarioV2FixturePath}
	}
	if err := validateScenarioV2FixtureReference(reference); err != nil {
		return "", err
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", &ScenarioV2Error{Cause: ErrScenarioV2FixturePath}
	}
	root = filepath.Clean(root)
	candidate := filepath.Clean(filepath.Join(root, filepath.FromSlash(strings.ReplaceAll(reference, `\`, "/"))))
	if !scenarioV2PathContained(root, candidate) {
		return "", &ScenarioV2Error{Cause: ErrScenarioV2FixturePath}
	}

	resolved, resolveErr := filepath.EvalSymlinks(candidate)
	if resolveErr == nil {
		resolved, _ = filepath.Abs(resolved)
		if !scenarioV2PathContained(root, filepath.Clean(resolved)) {
			return "", &ScenarioV2Error{Cause: ErrScenarioV2FixturePath}
		}
		return filepath.Clean(resolved), nil
	}
	if !os.IsNotExist(resolveErr) {
		return "", &ScenarioV2Error{Cause: ErrScenarioV2FixturePath}
	}
	// The final file may be created after scenario validation. Resolve its
	// existing parent so a symlinked directory cannot hide an escape.
	parent := filepath.Dir(candidate)
	resolvedParent, parentErr := filepath.EvalSymlinks(parent)
	if parentErr == nil {
		resolvedParent, _ = filepath.Abs(resolvedParent)
		if !scenarioV2PathContained(root, filepath.Clean(resolvedParent)) {
			return "", &ScenarioV2Error{Cause: ErrScenarioV2FixturePath}
		}
		return filepath.Join(filepath.Clean(resolvedParent), filepath.Base(candidate)), nil
	}
	return candidate, nil
}

func validateScenarioV2FixtureReference(reference string) error {
	if strings.TrimSpace(reference) == "" {
		return &ScenarioV2Error{Cause: ErrScenarioV2FixturePath}
	}
	if strings.IndexByte(reference, 0) >= 0 {
		return &ScenarioV2Error{Cause: ErrScenarioV2FixturePath}
	}
	if filepath.IsAbs(reference) || filepath.VolumeName(reference) != "" {
		return &ScenarioV2Error{Cause: ErrScenarioV2FixturePath}
	}
	// filepath.VolumeName is empty for Windows-looking paths when tests run on
	// Unix, so reject drive-relative and UNC forms explicitly as well.
	if len(reference) >= 2 && ((reference[0] >= 'A' && reference[0] <= 'Z') || (reference[0] >= 'a' && reference[0] <= 'z')) && reference[1] == ':' {
		return &ScenarioV2Error{Cause: ErrScenarioV2FixturePath}
	}
	if strings.HasPrefix(reference, `\\`) || strings.HasPrefix(reference, "//") {
		return &ScenarioV2Error{Cause: ErrScenarioV2FixturePath}
	}
	if strings.Contains(reference, "$") || strings.HasPrefix(reference, "~") {
		return &ScenarioV2Error{Cause: ErrScenarioV2FixturePath}
	}
	// A URI scheme is never a local fixture reference. This also catches
	// file:, http:, and custom schemes without dereferencing them.
	if colon := strings.IndexByte(reference, ':'); colon > 0 {
		if colon == 1 || !strings.Contains(reference[:colon], "/") {
			return &ScenarioV2Error{Cause: ErrScenarioV2FixturePath}
		}
	}
	depth := 0
	for _, part := range strings.Split(strings.ReplaceAll(reference, `\`, "/"), "/") {
		switch part {
		case "", ".":
		case "..":
			if depth == 0 {
				return &ScenarioV2Error{Cause: ErrScenarioV2FixturePath}
			}
			depth--
		default:
			depth++
		}
	}
	return nil
}

func scenarioV2PathContained(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
