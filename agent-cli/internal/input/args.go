package input

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ReadStdinText reads all available text from r and returns it trimmed of leading/trailing whitespace.
func ReadStdinText(r io.Reader) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// ParseAskArgs splits args into a prompt (text) and a list of file paths.
// Any argument that refers to an existing regular file is treated as a file path;
// all other arguments are joined with spaces to form the prompt.
// Order is preserved: prompt segments and file paths appear in the same order as args.
func ParseAskArgs(args []string) (prompt string, filePaths []string) {
	var promptParts []string
	for _, a := range args {
		path := a
		if path == "" {
			promptParts = append(promptParts, "")
			continue
		}
		// Resolve relative paths so we can stat correctly
		if !filepath.IsAbs(path) {
			if abs, err := filepath.Abs(path); err == nil {
				path = abs
			}
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			promptParts = append(promptParts, a)
			continue
		}
		filePaths = append(filePaths, path)
	}
	prompt = strings.TrimSpace(strings.Join(promptParts, " "))
	return prompt, filePaths
}
