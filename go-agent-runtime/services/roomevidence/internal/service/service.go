package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
)

// Service is the private implementation behind the public room evidence
// contract. It is intentionally stateless; each Open call creates an
// independent recorder with no process-wide ownership or mutable globals.
type Service struct{}

func New() *Service { return &Service{} }

func (s *Service) ValidateOutput(path string) error {
	destination := filepath.Clean(strings.TrimSpace(path))
	if strings.TrimSpace(path) == "" || destination == "." {
		return fmt.Errorf("%w: directory is required", roomevidence.ErrInvalidOutput)
	}
	return validateOutputTarget(destination)
}

func (s *Service) PrepareOutput(path string) (string, error) {
	destination := filepath.Clean(strings.TrimSpace(path))
	if strings.TrimSpace(path) == "" || destination == "." {
		return "", fmt.Errorf("%w: directory is required", roomevidence.ErrInvalidOutput)
	}
	if err := s.ValidateOutput(destination); err != nil {
		return "", err
	}
	if err := os.MkdirAll(destination, evidenceDirectoryMode); err != nil {
		return "", fmt.Errorf("create room evidence output directory %q: %w", destination, err)
	}
	return destination, nil
}

func (s *Service) Open(options roomevidence.Options) (roomevidence.Recorder, error) {
	return newRecorder(options)
}

func validateOutputTarget(destination string) error {
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, evidenceDirectoryMode); err != nil {
		return fmt.Errorf("prepare room evidence output parent %q: %w", destination, err)
	}
	info, err := os.Lstat(destination)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: target %q must be a non-symlink directory", roomevidence.ErrInvalidOutput, destination)
		}
		entries, readErr := os.ReadDir(destination)
		if readErr != nil {
			return fmt.Errorf("inspect room evidence output directory %q: %w", destination, readErr)
		}
		if len(entries) != 0 {
			return fmt.Errorf("%w: %q", roomevidence.ErrOutputNotEmpty, destination)
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
		return fmt.Errorf("close room evidence output probe %q: %w", destination, closeErr)
	}
	if removeErr != nil {
		return fmt.Errorf("remove room evidence output probe %q: %w", destination, removeErr)
	}
	return nil
}
