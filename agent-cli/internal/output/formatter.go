package output

import (
	"fmt"
	"io"
	"os"
)

// StreamToStdout reads from reader and writes to stdout in real-time.
func StreamToStdout(reader io.Reader) error {
	_, err := io.Copy(os.Stdout, reader)
	if err != nil {
		return fmt.Errorf("failed to stream output: %w", err)
	}
	fmt.Println() // trailing newline
	return nil
}
