package testing

import (
	"encoding/json"
	"net/http"
)

// CapturePair represents a captured HTTP request/response pair for record/replay.
type CapturePair struct {
	Request  CapturedRequest  `json:"request"`
	Response CapturedResponse `json:"response"`
}

// rawBody stores body as raw bytes; JSON unmarshals []byte as base64, so we use
// a wrapper that unmarshals from a plain string.
type rawBody []byte

func (b *rawBody) UnmarshalJSON(data []byte) error {
	if len(data) >= 2 && data[0] == '"' && data[len(data)-1] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*b = []byte(s)
		return nil
	}
	*b = nil
	return nil
}

func (b rawBody) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(b))
}

// CapturedRequest represents a captured HTTP request.
type CapturedRequest struct {
	Method  string      `json:"method"`
	URL     string      `json:"url"`
	Headers http.Header `json:"headers"`
	Body    rawBody     `json:"body,omitempty"`
}

// CapturedResponse represents a captured HTTP response.
type CapturedResponse struct {
	StatusCode int         `json:"status_code"`
	Status     string      `json:"status"`
	Headers    http.Header `json:"headers"`
	Body       rawBody     `json:"body,omitempty"`
}
