package output

import (
	"fmt"
	"io"
)

// StreamTo copies reader to w in real time and finishes with a trailing
// newline. The caller owns w (for example the command's stdout).
func StreamTo(w io.Writer, reader io.Reader) error {
	if _, err := io.Copy(w, reader); err != nil {
		return fmt.Errorf("failed to stream output: %w", err)
	}
	if _, err := io.WriteString(w, "\n"); err != nil {
		return fmt.Errorf("failed to stream output: %w", err)
	}
	return nil
}
