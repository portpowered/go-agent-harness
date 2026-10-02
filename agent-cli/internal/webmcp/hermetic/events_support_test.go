package hermetic

import (
	"fmt"
	"io"
)

// LoadEvents reads and validates a browser JSONL artifact from a stream.
func LoadEvents(reader io.Reader) ([]Event, error) {
	if reader == nil {
		return nil, newEventValidationError(0, "stream", "reader is nil")
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read browser event stream: %w", err)
	}
	return ValidateEventStream(data)
}
