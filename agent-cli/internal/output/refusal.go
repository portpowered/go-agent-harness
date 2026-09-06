package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/term"
)

// refusalEventJSON is the structured JSON event emitted to stderr when
// --output-json is set and a refusal is received.
type refusalEventJSON struct {
	Type      string `json:"type"`
	Message   string `json:"message"`
	Model     string `json:"model,omitempty"`
	Timestamp string `json:"timestamp"`
}

// WriteRefusal writes a refusal message to w with a [REFUSAL] prefix.
// When w is os.Stderr and the file descriptor is a terminal, the output
// is wrapped in yellow ANSI escape codes for visual distinction.
func WriteRefusal(w io.Writer, refusalText string) error {
	if refusalText == "" {
		return nil
	}
	msg := fmt.Sprintf("[REFUSAL] %s\n", refusalText)
	if isStderrTTY(w) {
		// Yellow: \033[33m ... \033[0m
		msg = "\033[33m" + msg + "\033[0m"
	}
	_, err := fmt.Fprint(w, msg)
	return err
}

// WriteRefusalJSON writes a structured JSON refusal event to w.
// The event includes the refusal text, model identifier, and an ISO 8601 timestamp.
func WriteRefusalJSON(w io.Writer, refusalText, model string) error {
	if refusalText == "" {
		return nil
	}
	evt := refusalEventJSON{
		Type:      "refusal",
		Message:   refusalText,
		Model:     model,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal refusal event: %w", err)
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

// isStderrTTY returns true when w is os.Stderr and stderr is a terminal.
func isStderrTTY(w io.Writer) bool {
	if f, ok := w.(*os.File); ok {
		return term.IsTerminal(int(f.Fd()))
	}
	return false
}
