// Package service owns the capture-claim filesystem policy. It is reachable
// only through services/captureclaim/wire.
package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/captureclaim"
)

// Service is inert until Acquire or ObserveHolder is called.
type Service struct {
	fileSystem          captureclaim.FileSystem
	clock               captureclaim.Clock
	host                captureclaim.Host
	process             captureclaim.Process
	observationAttempts int
	observationInterval time.Duration
}

const (
	claimParentMode fs.FileMode = 0o755
	claimFileMode   fs.FileMode = 0o600
)

// New constructs the private implementation from explicit host seams. Zero
// dependencies select the local OS-backed implementations without performing
// any I/O during construction.
func New(deps captureclaim.Dependencies) *Service {
	if deps.FileSystem == nil {
		deps.FileSystem = osFileSystem{}
	}
	if deps.Clock == nil {
		deps.Clock = wallClock{}
	}
	if deps.Host == nil {
		deps.Host = osHost{}
	}
	if deps.Process == nil {
		deps.Process = osProcess{}
	}
	attempts := deps.HolderObservationAttempts
	if attempts <= 0 {
		attempts = captureclaim.DefaultHolderObservationAttempts
	}
	interval := deps.HolderObservationInterval
	if interval < 0 {
		interval = captureclaim.DefaultHolderObservationInterval
	}
	return &Service{
		fileSystem:          deps.FileSystem,
		clock:               deps.Clock,
		host:                deps.Host,
		process:             deps.Process,
		observationAttempts: attempts,
		observationInterval: interval,
	}
}

var _ captureclaim.Service = (*Service)(nil)

// Acquire creates an exclusive sidecar, writes redacted holder metadata,
// and returns a claim whose inode is retained for later ownership checks.
func (s *Service) Acquire(rawPath string) (captureclaim.Claim, error) {
	if strings.TrimSpace(rawPath) == "" {
		return nil, &captureclaim.ClaimError{Kind: captureclaim.ErrInvalidDestination, Path: rawPath}
	}
	path := filepath.Clean(rawPath)
	lockPath := path + captureclaim.Suffix
	if err := s.prepare(path); err != nil {
		return nil, err
	}
	if err := s.checkAvailable(path, lockPath); err != nil {
		return nil, err
	}
	file, err := s.createSidecar(path, lockPath)
	if err != nil {
		return nil, err
	}
	ownerInfo, err := file.Stat()
	if err != nil {
		return nil, s.openFailure(path, lockPath, file, nil, "inspect claim owner", err)
	}
	if err := s.writeMetadata(path, file); err != nil {
		return nil, s.openFailure(path, lockPath, file, ownerInfo, "write claim metadata", err)
	}
	if err := s.recheckDestination(path, lockPath, ownerInfo); err != nil {
		return nil, err
	}
	return s.retain(path, lockPath, ownerInfo)
}

func (s *Service) prepare(path string) error {
	parent := filepath.Dir(path)
	if err := s.fileSystem.MkdirAll(parent, claimParentMode); err != nil {
		return unavailable(path, "prepare parent directory", err)
	}
	return nil
}

func (s *Service) checkAvailable(path, lockPath string) error {
	if _, err := s.fileSystem.Lstat(lockPath); err == nil {
		return s.claimed(path, lockPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return unavailable(path, "inspect claim", err)
	}
	if _, err := s.fileSystem.Lstat(path); err == nil {
		return occupied(path, nil)
	} else if !errors.Is(err, os.ErrNotExist) {
		return unavailable(path, "inspect destination", err)
	}
	return nil
}

func (s *Service) createSidecar(path, lockPath string) (captureclaim.File, error) {
	file, err := s.fileSystem.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, claimFileMode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, s.claimed(path, lockPath)
		}
		return nil, unavailable(path, "claim destination", err)
	}
	return file, nil
}

func (s *Service) writeMetadata(path string, file captureclaim.File) error {
	host, hostErr := s.host.Hostname()
	if hostErr != nil {
		host = "unknown"
	}
	holder := captureclaim.ClaimHolder{
		RequestedPath: path,
		PID:           s.process.PID(),
		Host:          host,
		StartedAtUTC:  s.clock.Now().UTC().Format(time.RFC3339Nano),
	}
	metadata, err := json.Marshal(holder)
	if err == nil {
		err = writeAll(file, metadata)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	} else if closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
		err = errors.Join(err, closeErr)
	}
	return err
}

func (s *Service) recheckDestination(path, lockPath string, ownerInfo fs.FileInfo) error {
	if _, err := s.fileSystem.Lstat(path); err == nil {
		cleanupErr := s.removeOwned(lockPath, ownerInfo)
		return occupied(path, cleanupErr)
	} else if !errors.Is(err, os.ErrNotExist) {
		cleanupErr := s.removeOwned(lockPath, ownerInfo)
		return unavailable(path, "recheck destination", errors.Join(err, cleanupErr))
	}
	return nil
}

func (s *Service) retain(path, lockPath string, ownerInfo fs.FileInfo) (captureclaim.Claim, error) {
	claimFile, err := s.fileSystem.OpenFile(lockPath, os.O_RDONLY, 0)
	if err != nil {
		cleanupErr := s.removeOwned(lockPath, ownerInfo)
		return nil, lost(path, errors.Join(fmt.Errorf("retain claim: %w", err), cleanupErr))
	}
	claimInfo, err := claimFile.Stat()
	if err != nil {
		closeErr := s.closeFile(claimFile)
		cleanupErr := s.removeOwned(lockPath, ownerInfo)
		return nil, lost(path, errors.Join(fmt.Errorf("inspect retained claim: %w", err), closeErr, cleanupErr))
	}
	if !s.fileSystem.SameFile(ownerInfo, claimInfo) {
		return nil, lost(path, s.closeFile(claimFile))
	}
	return &claim{
		service:   s,
		path:      path,
		lockPath:  lockPath,
		file:      claimFile,
		ownerInfo: ownerInfo,
	}, nil
}

func (s *Service) claimed(path, lockPath string) error {
	return &captureclaim.ClaimError{
		Kind:   captureclaim.ErrDestinationClaimed,
		Path:   path,
		Holder: s.ObserveHolder(lockPath),
	}
}

// ObserveHolder waits only through the configured finite observation window.
// Invalid or incomplete JSON is deliberately treated as unavailable identity.
func (s *Service) ObserveHolder(path string) *captureclaim.ClaimHolder {
	for attempt := 0; attempt < s.observationAttempts; attempt++ {
		data, err := s.fileSystem.ReadFile(path)
		if err == nil {
			var holder captureclaim.ClaimHolder
			if json.Unmarshal(data, &holder) == nil && holder.PID > 0 && strings.TrimSpace(holder.RequestedPath) != "" {
				return &holder
			}
		}
		if attempt+1 < s.observationAttempts && s.observationInterval > 0 {
			s.clock.Sleep(s.observationInterval)
		}
	}
	return nil
}

func (s *Service) openFailure(path, lockPath string, file captureclaim.File, ownerInfo fs.FileInfo, operation string, cause error) error {
	var statErr error
	if ownerInfo == nil && file != nil {
		ownerInfo, statErr = file.Stat()
	}
	cleanupErr := errors.Join(s.closeFile(file), s.removeOwned(lockPath, ownerInfo))
	return unavailable(path, operation, errors.Join(cause, statErr, cleanupErr))
}

func (s *Service) closeFile(file captureclaim.File) error {
	if file == nil {
		return nil
	}
	if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		return fmt.Errorf("close claim: %w", err)
	}
	return nil
}

func (s *Service) removeOwned(path string, expected fs.FileInfo) error {
	if expected == nil {
		return nil
	}
	info, err := s.fileSystem.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect claim cleanup: %w", err)
	}
	if !s.fileSystem.SameFile(expected, info) {
		return captureclaim.ErrClaimLost
	}
	if err := s.fileSystem.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove claim: %w", err)
	}
	return nil
}
