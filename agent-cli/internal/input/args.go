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

// ParseAskArgs splits args into a prompt (text) and a list of attachment paths.
// Existing filesystem entries and path-shaped arguments are attachments. Keeping
// failed path-shaped arguments in filePaths is important: the caller can then
// report a missing, unreadable, or otherwise invalid attachment instead of
// silently changing the customer's request into prompt text.
//
// Arguments that contain whitespace are treated as prompt text unless they are
// already an existing filesystem entry. This preserves a quoted prompt such as
// "summarize this file" while still allowing quoted paths whose names contain
// spaces when the path exists.
func ParseAskArgs(args []string) (prompt string, filePaths []string) {
	var promptParts []string
	for _, a := range args {
		if a == "" {
			promptParts = append(promptParts, "")
			continue
		}
		if !isAskAttachmentIntent(a) {
			promptParts = append(promptParts, a)
			continue
		}
		// Retain the original spelling so diagnostics identify exactly what the
		// user supplied. os.Stat and os.ReadFile both accept relative paths.
		filePaths = append(filePaths, a)
	}
	prompt = strings.TrimSpace(strings.Join(promptParts, " "))
	return prompt, filePaths
}

// isAskAttachmentIntent reports whether an argument should be validated as a
// local attachment. A successful stat includes directories and special files;
// the attachment loader rejects those as non-regular files without attempting
// to read them.
func isAskAttachmentIntent(arg string) bool {
	if _, err := os.Stat(arg); err == nil {
		return true
	} else if !os.IsNotExist(err) {
		// Permission and other stat failures are still path intent. They must
		// not be demoted to prompt text.
		return true
	}

	return looksLikeLocalPath(arg)
}

func looksLikeLocalPath(arg string) bool {
	if strings.Contains(arg, "://") {
		// URLs are ordinary prompt text for ask; this parser only admits local
		// attachment paths.
		return false
	}
	if strings.ContainsAny(arg, " \t\r\n") {
		return false
	}
	if filepath.IsAbs(arg) || arg == "." || arg == ".." || strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "../") || strings.HasPrefix(arg, "~/") {
		return true
	}
	// Check both separators so a Windows-shaped path remains path intent when
	// it is passed through a Unix test or vice versa.
	if strings.ContainsAny(arg, `/\\`) {
		return true
	}
	// A filename extension is a useful local-path signal for a missing file
	// such as "photo.png" while ordinary multi-word prompts remain text.
	ext := filepath.Ext(arg)
	return ext != "" && ext != "."
}
