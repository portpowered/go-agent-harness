// This file owns @-file and @-directory attachment parsing, file autocomplete, media typing, and filesystem suggestions.

package services

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// excludedDirs contains directory names to exclude from file autocomplete suggestions.
var excludedDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"__pycache__":  true,
}

// updateFileAutocomplete checks the current input for an @ prefix and activates
// file autocomplete suggestions if appropriate.
func (m *ChatModel) updateFileAutocomplete() {
	prefix := extractAtPrefix(m.input.Value())
	if prefix == "" && !strings.HasSuffix(m.input.Value(), "@") {
		// No @ context — deactivate.
		if m.fileAutocomplete.IsActive() {
			m.fileAutocomplete.Reset()
		}
		return
	}
	// Lazy-load file suggestions on first activation.
	if m.fileSuggestions == nil {
		workDir, err := os.Getwd()
		if err != nil {
			return
		}
		m.fileSuggestions = scanFileSuggestions(workDir)
		m.fileAutocomplete.SetSuggestions(m.fileSuggestions)
	}
	m.fileAutocomplete.SetFilter(prefix)
}

// completeAtSuggestion replaces the @prefix in the input with the completed suggestion.
func (m *ChatModel) completeAtSuggestion(selected string) {
	val := m.input.Value()
	// Find the last @ token start position.
	idx := -1
	for i := len(val) - 1; i >= 0; i-- {
		if val[i] == '@' {
			if i == 0 || val[i-1] == ' ' {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		return
	}
	// Replace from @ to end with @selected + trailing space.
	newVal := val[:idx] + "@" + selected + " "
	m.input.SetValue(newVal)
	m.input.SetCursor(len(newVal))
}

// parseAtReferences extracts @path tokens from the input, reads each referenced file,
// and returns the cleaned prompt text (with @tokens removed), content parts for the LLM,
// and an error message string (empty on success). If any referenced file does not exist
// or cannot be read, an error is returned and no content parts are produced.
func parseAtReferences(input string) (cleanedText string, parts []messages.ContentPart, errMsg string) {
	words := strings.Fields(input)
	if len(words) == 0 {
		return input, nil, ""
	}

	workDir, err := os.Getwd()
	if err != nil {
		return input, nil, ""
	}

	var textWords []string
	var errors []string
	for _, word := range words {
		if !strings.HasPrefix(word, "@") || len(word) == 1 {
			textWords = append(textWords, word)
			continue
		}
		refPath := word[1:]
		absPath := refPath
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(workDir, absPath)
		}
		info, statErr := os.Stat(absPath)
		if statErr != nil {
			errors = append(errors, "File not found: "+refPath)
			continue
		}
		if info.IsDir() {
			entries, dirErr := os.ReadDir(absPath)
			if dirErr != nil {
				errors = append(errors, "Cannot read directory: "+refPath)
				continue
			}
			var listing []string
			for _, entry := range entries {
				name := entry.Name()
				if entry.IsDir() {
					name += "/"
				}
				listing = append(listing, name)
			}
			content := fmt.Sprintf("[Directory: %s]\n%s", refPath, strings.Join(listing, "\n"))
			parts = append(parts, messages.TextPart{Text: content})
			continue
		}
		// Check if image by file extension before reading.
		if isImageExtension(absPath) {
			data, readErr := os.ReadFile(absPath)
			if readErr != nil {
				errors = append(errors, "Cannot read file: "+refPath)
				continue
			}
			parts = append(parts, messages.ImagePart{
				Bytes:     data,
				MediaType: imageMediaType(absPath),
			})
			continue
		}
		data, readErr := os.ReadFile(absPath)
		if readErr != nil {
			errors = append(errors, "Cannot read file: "+refPath)
			continue
		}
		mediaType := http.DetectContentType(data)
		if strings.HasPrefix(mediaType, "text/") || mediaType == "application/octet-stream" {
			// Include text files as TextPart with filename context.
			content := fmt.Sprintf("[File: %s]\n%s", filepath.Base(absPath), string(data))
			parts = append(parts, messages.TextPart{Text: content})
		} else {
			// Unknown binary file — include as text reference.
			content := fmt.Sprintf("[Binary file: %s (%d bytes)]", filepath.Base(absPath), len(data))
			parts = append(parts, messages.TextPart{Text: content})
		}
	}

	if len(errors) > 0 {
		return "", nil, strings.Join(errors, "\n")
	}

	return strings.Join(textWords, " "), parts, ""
}

// imageExtensions maps file extensions to MIME media types for image files.
var imageExtensions = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".svg":  "image/svg+xml",
}

// isImageExtension returns true if the file path has a recognized image extension.
func isImageExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	_, ok := imageExtensions[ext]
	return ok
}

// imageMediaType returns the MIME type for a recognized image extension.
func imageMediaType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if mt, ok := imageExtensions[ext]; ok {
		return mt
	}
	return "application/octet-stream"
}

// scanFileSuggestions walks the working directory and returns Suggestion entries
// for all files and directories, excluding hidden/ignored directories. Directories
// have a trailing "/" in their label. Paths are relative to workDir.
func scanFileSuggestions(workDir string) []Suggestion {
	var suggestions []Suggestion
	_ = filepath.WalkDir(workDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		rel, relErr := filepath.Rel(workDir, path)
		if relErr != nil || rel == "." {
			return nil
		}
		// Use forward slashes for consistent display.
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if excludedDirs[d.Name()] {
				return filepath.SkipDir
			}
			suggestions = append(suggestions, Suggestion{Label: rel + "/"})
			return nil
		}
		suggestions = append(suggestions, Suggestion{Label: rel})
		return nil
	})
	return suggestions
}

// extractAtPrefix returns the prefix typed after the last @ token in the input,
// or empty string if the cursor is not in an @ context. For example:
//
//	"@src/ma"      → "src/ma"
//	"hello @sr"    → "sr"
//	"hello world"  → ""
//	"@"            → ""
func extractAtPrefix(input string) string {
	// Find the last @ that starts a token (preceded by space or at position 0).
	idx := -1
	for i := len(input) - 1; i >= 0; i-- {
		if input[i] == '@' {
			if i == 0 || input[i-1] == ' ' {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		return ""
	}
	// The prefix is everything after @ until end of input (no space after @).
	after := input[idx+1:]
	if strings.Contains(after, " ") {
		return "" // cursor moved past the @ token
	}
	return after
}
