package probe

import (
	"encoding/json"
	"fmt"
	"io"
)

// readInput accepts the document forms LoadScenarioV2 decodes.
func readInput(input any) ([]byte, error) {
	switch value := input.(type) {
	case []byte:
		return value, nil
	case json.RawMessage:
		return value, nil
	case string:
		return []byte(value), nil
	case io.Reader:
		return io.ReadAll(value)
	default:
		return nil, fmt.Errorf("unsupported input %T", input)
	}
}
