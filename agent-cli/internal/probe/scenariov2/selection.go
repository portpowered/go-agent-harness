package scenariov2

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

// recordingRootPermission is the mode of a created evidence root directory.
const recordingRootPermission = 0o755

// NoSelectionMessage is the operator guidance for a run without scenarios.
const NoSelectionMessage = "no probe scenarios selected; pass scenario paths as arguments or repeat --scenario"

// Selection is one requested scenario document. Err is set when the document
// carried a v2 envelope but failed to load, so the run still reports a
// failed result line for it.
type Selection struct {
	Scenario  probe.ScenarioV2
	Selection string
	Err       error
}

// LoadSelections loads every selected path as a probe.scenario.v2 document,
// deduplicating by scenario identity. Mixing v2 with legacy or registered
// scenarios is rejected.
func LoadSelections(selections []string, lookup probe.CorpusLookup) ([]Selection, error) {
	if len(selections) == 0 {
		return nil, errors.New(NoSelectionMessage)
	}
	result := make([]Selection, 0, len(selections))
	seen := make(map[string]struct{}, len(selections))
	for _, selection := range selections {
		isV2, err := FileIsV2(selection)
		if err != nil {
			return nil, err
		}
		if !isV2 {
			return nil, fmt.Errorf("probe.scenario.v2 execution cannot mix selection %q with a legacy or registered scenario", selection)
		}
		scenario, loadErr := probe.LoadScenarioV2File(selection, lookup)
		if loadErr != nil {
			// Keep a failed result line for a selected document whose envelope was
			// recognized but whose fixtures or typed values are invalid.
			result = append(result, Selection{Selection: selection, Err: fmt.Errorf("load probe scenario %q: %w", selection, loadErr)})
			continue
		}
		key := scenario.ID + "\x00" + scenario.Name
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, Selection{Selection: selection, Scenario: scenario})
	}
	return result, nil
}

// FileIsV2 reports whether path is an existing file with a v2 envelope. A
// missing path is not v2 so registered scenario names still resolve.
func FileIsV2(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("load probe scenario %q: %w", path, err)
	}
	if info.IsDir() {
		return false, fmt.Errorf("load probe scenario %q: path is a directory", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read probe scenario %q: %w", path, err)
	}
	return HasEnvelope(data), nil
}

// HasEnvelope reports whether data is a JSON object declaring schema_version.
func HasEnvelope(data []byte) bool {
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil || envelope == nil {
		return false
	}
	_, present := envelope["schema_version"]
	return present
}

// PrepareRecordingRoot resolves the parent directory for v2 evidence bundles,
// creating a fresh temporary root when none is configured.
func PrepareRecordingRoot(configured string, count int) (string, error) {
	root := strings.TrimSpace(configured)
	if root == "" {
		created, err := os.MkdirTemp("", "go-agent-probe-v2-evidence-")
		if err != nil {
			return "", fmt.Errorf("create v2 evidence root for %d scenarios: %w", count, err)
		}
		return created, nil
	}
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve v2 evidence root %q: %w", configured, err)
	}
	if err := os.MkdirAll(root, recordingRootPermission); err != nil {
		return "", fmt.Errorf("create v2 evidence root %q: %w", root, err)
	}
	return root, nil
}

// RecordingDirectory names the run-scoped bundle directory for one entry.
func RecordingDirectory(root string, index int, entry Selection) string {
	if strings.TrimSpace(root) == "" {
		return ""
	}
	name := entry.Scenario.ID
	if name == "" {
		name = entry.Selection
	}
	name = pathSlug(name)
	if name == "" {
		name = "scenario"
	}
	return filepath.Join(root, fmt.Sprintf("%03d-%s", index+1, name))
}

func pathSlug(value string) string {
	var builder strings.Builder
	for _, character := range value {
		if isSlugRune(character) {
			builder.WriteRune(character)
			continue
		}
		builder.WriteByte('_')
	}
	return strings.Trim(builder.String(), ".")
}

func isSlugRune(character rune) bool {
	return (character >= 'a' && character <= 'z') ||
		(character >= 'A' && character <= 'Z') ||
		(character >= '0' && character <= '9') ||
		character == '-' || character == '_' || character == '.'
}
