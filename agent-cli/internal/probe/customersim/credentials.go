package customersim

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const homePrefix = "~/"

// readCredential resolves a key from the environment variable first and the
// secret file second. Line breaks are stripped and blank values ignored.
func (h Host) readCredential(envName, rawFile string) (string, error) {
	names := uniqueNonEmpty(envName)
	for _, name := range names {
		if value, ok := os.LookupEnv(name); ok {
			value = stripLineBreaks(value)
			if strings.TrimSpace(value) == "" {
				continue
			}
			return value, nil
		}
	}
	files := uniqueNonEmpty(rawFile)
	for _, rawPath := range files {
		value, err := h.readSecretFile(rawPath)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(value) != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("live customer simulation credentials are required; set %s or provide %s", strings.Join(names, " or "), strings.Join(files, " or "))
}

// readSecretFile returns the file's newline-stripped contents; a missing
// file yields an empty value so the next source is tried.
func (h Host) readSecretFile(rawPath string) (string, error) {
	path, err := h.expandHome(rawPath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read customer simulation secret file %q: %w", path, err)
	}
	return stripLineBreaks(string(data)), nil
}

func stripLineBreaks(value string) string {
	value = strings.ReplaceAll(value, "\n", "")
	return strings.ReplaceAll(value, "\r", "")
}

// clearEnvironment unsets every credential variable so a key never outlives
// the run in the process environment.
func clearEnvironment(names []string) {
	for _, name := range names {
		_ = os.Unsetenv(name) //nolint:errcheck // Unsetenv fails only for invalid names, which were never set.
	}
}

func (h Host) expandHome(raw string) (string, error) {
	if !strings.HasPrefix(raw, homePrefix) && raw != "~" {
		return raw, nil
	}
	if h.HomeDir == nil {
		return "", errors.New("resolve secret home: home directory lookup is not configured")
	}
	home, err := h.HomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve secret home: %w", err)
	}
	if raw == "~" {
		return home, nil
	}
	return filepath.Join(home, strings.TrimPrefix(raw, homePrefix)), nil
}
