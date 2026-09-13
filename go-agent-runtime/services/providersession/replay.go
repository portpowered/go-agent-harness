package providersession

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ReplayTools exposes capture inspection without exposing provider or CLI
// configuration types.
type ReplayTools struct{}

// Names extracts the names from a captured initial session configuration.
func (ReplayTools) Names(path string, sequence int, session map[string]json.RawMessage) ([]string, bool, error) {
	raw, ok := session["tools"]
	if !ok {
		return []string{}, true, nil
	}
	var tools []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, true, fmt.Errorf("replay session capture %s: session.tools at sequence %d is invalid: %w", path, sequence, err)
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if name := strings.TrimSpace(tool.Name); name != "" {
			names = append(names, name)
		}
	}
	return names, true, nil
}
