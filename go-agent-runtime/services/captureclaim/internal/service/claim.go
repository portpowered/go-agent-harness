package service

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/captureclaim"
)

type claim struct {
	service   *Service
	path      string
	lockPath  string
	file      captureclaim.File
	ownerInfo fs.FileInfo

	mu       sync.Mutex
	released bool
	closed   bool
}

var _ captureclaim.Claim = (*claim)(nil)

func (c *claim) Path() string {
	if c == nil {
		return ""
	}
	return c.path
}

func (c *claim) Publish(flush func(string) error) (publishErr error) {
	if c == nil {
		return captureclaim.ErrClaimLost
	}
	if flush == nil {
		return errors.New("session recording capture flush is nil")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ownsLocked(); err != nil {
		return err
	}
	tempPath, err := c.createTemporary(flush)
	if tempPath != "" {
		defer func() { publishErr = errors.Join(publishErr, c.cleanupTemporary(tempPath)) }()
	}
	if err != nil {
		return err
	}
	if err := c.makeTemporaryDurable(tempPath); err != nil {
		return err
	}
	if err := c.checkPublicationDestination(); err != nil {
		return err
	}
	return c.linkTemporary(tempPath)
}

func (c *claim) createTemporary(flush func(string) error) (string, error) {
	temp, err := c.service.fileSystem.CreateTemp(filepath.Dir(c.path), "."+filepath.Base(c.path)+".tmp-")
	if err != nil {
		return "", fmt.Errorf("create private capture artifact: %w", err)
	}
	tempPath := temp.Name()
	if err := temp.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		return tempPath, fmt.Errorf("close private capture artifact: %w", err)
	}
	if err := flush(tempPath); err != nil {
		return tempPath, err
	}
	return tempPath, nil
}

func (c *claim) cleanupTemporary(path string) error {
	if err := c.service.fileSystem.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove private capture artifact: %w", err)
	}
	return nil
}

func (c *claim) makeTemporaryDurable(path string) error {
	durable, err := c.service.fileSystem.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open private capture artifact: %w", err)
	}
	syncErr := durable.Sync()
	closeErr := durable.Close()
	if syncErr != nil || (closeErr != nil && !errors.Is(closeErr, os.ErrClosed)) {
		return fmt.Errorf("make private capture artifact durable: %w", errors.Join(syncErr, closeErr))
	}
	return nil
}

func (c *claim) checkPublicationDestination() error {
	if err := c.ownsLocked(); err != nil {
		return err
	}
	if _, err := c.service.fileSystem.Lstat(c.path); err == nil {
		return occupied(c.path, nil)
	} else if !errors.Is(err, os.ErrNotExist) {
		return unavailable(c.path, "inspect destination before publication", err)
	}
	return nil
}

func (c *claim) linkTemporary(path string) error {
	if err := c.service.fileSystem.Link(path, c.path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return occupied(c.path, nil)
		}
		return fmt.Errorf("publish session capture: %w", err)
	}
	return nil
}

func (c *claim) ownsLocked() error {
	if c.released || c.closed || c.file == nil || c.ownerInfo == nil {
		return lost(c.path, nil)
	}
	claimInfo, err := c.file.Stat()
	if err != nil {
		return lost(c.path, fmt.Errorf("inspect owner: %w", err))
	}
	pathInfo, err := c.service.fileSystem.Lstat(c.lockPath)
	if err != nil {
		return lost(c.path, fmt.Errorf("inspect claim: %w", err))
	}
	if !c.service.fileSystem.SameFile(c.ownerInfo, claimInfo) || !c.service.fileSystem.SameFile(c.ownerInfo, pathInfo) {
		return lost(c.path, nil)
	}
	return nil
}

func (c *claim) Release() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.released {
		return nil
	}
	closeErr := c.closeRetained()
	owned, terminal, inspectErr := c.releaseOwnershipLocked()
	if !owned {
		if terminal && closeErr == nil {
			c.released = true
		}
		return errors.Join(closeErr, inspectErr)
	}
	removeErr := c.removeClaim()
	if closeErr == nil && removeErr == nil {
		c.released = true
	}
	return errors.Join(closeErr, removeErr)
}

func (c *claim) closeRetained() error {
	if c.closed || c.file == nil {
		return nil
	}
	if err := c.file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		return fmt.Errorf("close claim: %w", err)
	}
	c.closed = true
	return nil
}

func (c *claim) removeClaim() error {
	if err := c.service.fileSystem.Remove(c.lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove claim: %w", err)
	}
	return nil
}

func (c *claim) releaseOwnershipLocked() (owned, terminal bool, err error) {
	if c.ownerInfo == nil {
		return false, false, lost(c.path, nil)
	}
	pathInfo, statErr := c.service.fileSystem.Lstat(c.lockPath)
	if errors.Is(statErr, os.ErrNotExist) {
		return false, true, nil
	}
	if statErr != nil {
		return false, false, lost(c.path, fmt.Errorf("inspect claim: %w", statErr))
	}
	if !c.service.fileSystem.SameFile(c.ownerInfo, pathInfo) {
		// The sidecar is present but belongs to a replacement. Never retry a
		// stale owner against state it no longer controls.
		return false, true, lost(c.path, nil)
	}
	return true, true, nil
}

func writeAll(file captureclaim.File, data []byte) error {
	for len(data) > 0 {
		written, err := file.Write(data)
		if written < 0 || written > len(data) {
			return fmt.Errorf("invalid claim metadata write count %d", written)
		}
		data = data[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
