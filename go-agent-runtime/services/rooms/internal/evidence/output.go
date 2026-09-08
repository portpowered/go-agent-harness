package evidence

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

// ValidateOutput admits an empty writable directory and rejects destinations
// inside the immutable replay source.
func ValidateOutput(plan rooms.RoomReplayPlan, destination string) error {
	raw := strings.TrimSpace(destination)
	if raw == "" {
		return errors.New("room replay output directory is required")
	}
	source, err := filepath.Abs(filepath.Clean(plan.BundlePath))
	if err != nil {
		return fmt.Errorf("resolve room replay bundle path: %w", err)
	}
	output, err := filepath.Abs(filepath.Clean(raw))
	if err != nil {
		return fmt.Errorf("resolve room replay output path: %w", err)
	}
	relative, err := filepath.Rel(source, output)
	if err != nil {
		return fmt.Errorf("compare room replay source and output paths: %w", err)
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return fmt.Errorf("room replay output directory %q must be outside source bundle %q", destination, plan.BundlePath)
	}
	return validateEmptyWritableDirectory(output)
}

// ValidateEvidenceOutput validates a fresh evidence destination before any
// session or device side effect occurs.
func ValidateEvidenceOutput(destination string) error {
	raw := strings.TrimSpace(destination)
	if raw == "" || filepath.Clean(raw) == "." {
		return errors.New("room evidence output directory is required")
	}
	return validateEmptyWritableDirectory(filepath.Clean(raw))
}

func validateEmptyWritableDirectory(destination string) error {
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, evidenceDirectoryMode); err != nil {
		return fmt.Errorf("prepare room evidence output parent %q: %w", destination, err)
	}
	info, err := os.Lstat(destination)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("room evidence output target %q must be a non-symlink directory", destination)
		}
		entries, readErr := os.ReadDir(destination)
		if readErr != nil {
			return fmt.Errorf("inspect room evidence output directory %q: %w", destination, readErr)
		}
		if len(entries) != 0 {
			return fmt.Errorf("room evidence output directory %q is not safe: it must be empty", destination)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect room evidence output target %q: %w", destination, err)
	}
	probe, err := os.CreateTemp(parent, ".room-evidence-probe-")
	if err != nil {
		return fmt.Errorf("probe room evidence output target %q: %w", destination, err)
	}
	probePath := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(probePath)
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

func CreateFreshRunDirectory(configDir string) (string, error) {
	if strings.TrimSpace(configDir) == "" {
		return "", errors.New("room config directory is required")
	}
	if err := os.MkdirAll(configDir, evidenceDirectoryMode); err != nil {
		return "", fmt.Errorf("create room config directory %q: %w", configDir, err)
	}
	directory, err := os.MkdirTemp(configDir, "room-run-")
	if err != nil {
		return "", fmt.Errorf("create fresh room run directory under %q: %w", configDir, err)
	}
	return filepath.Clean(directory), nil
}
