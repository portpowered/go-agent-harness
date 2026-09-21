package recording

import (
	"encoding/json"
	"net/http"
)

// HTTPBody preserves the portable capture format's plain-string body encoding
// while retaining the byte-oriented contract used by HTTP callers.
type HTTPBody []byte

func (b HTTPBody) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(b))
}

func (b *HTTPBody) UnmarshalJSON(data []byte) error {
	if len(data) >= 2 && data[0] == '"' && data[len(data)-1] == '"' {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*b = HTTPBody(value)
		return nil
	}
	*b = nil
	return nil
}

// HTTPCapturePair is the stable request/response representation used by
// provider HTTP recording and replay.
type HTTPCapturePair struct {
	Request  HTTPCapturedRequest  `json:"request"`
	Response HTTPCapturedResponse `json:"response"`
}

type HTTPCapturedRequest struct {
	Method  string      `json:"method"`
	URL     string      `json:"url"`
	Headers http.Header `json:"headers"`
	Body    HTTPBody    `json:"body,omitempty"`
}

type HTTPCapturedResponse struct {
	StatusCode int         `json:"status_code"`
	Status     string      `json:"status"`
	Headers    http.Header `json:"headers"`
	Body       HTTPBody    `json:"body,omitempty"`
}

// HTTPRecorder is the recording-owned HTTP transport. It observes response
// consumption and publishes only after every request and response is complete.
type HTTPRecorder interface {
	http.RoundTripper
	Writer
	Captures() []HTTPCapturePair
}

// HTTPRecordingService is an optional extension of Service for providers that
// record ordinary HTTP request/response traffic. Keeping it separate avoids
// enlarging the required embeddable recording contract for custom services.
type HTTPRecordingService interface {
	OpenHTTPRecorder(http.RoundTripper) (HTTPRecorder, error)
}
