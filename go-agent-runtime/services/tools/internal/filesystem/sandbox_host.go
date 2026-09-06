package filesystem

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

type fileSystem interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte) error
	ReadDir(path string) ([]os.DirEntry, error)
}

func newLegacyFileSystem(workspace string, restrict bool) fileSystem {
	if restrict {
		return &sandboxFs{
			workspace:          workspace,
			protectedReadRoots: normalizeProtectedReadRoots(platformProtectedReadRoots()),
		}
	}
	return &hostFs{}
}

// hostFs is an unrestricted fileReadWriter that operates directly on the host filesystem.
type hostFs struct{}

func (h *hostFs) ReadFile(path string) ([]byte, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to read file: file not found: %w", err)
		}
		if os.IsPermission(err) {
			return nil, fmt.Errorf("failed to read file: access denied: %w", err)
		}
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	return content, nil
}

func (h *hostFs) ReadDir(path string) ([]os.DirEntry, error) {
	return os.ReadDir(path)
}

func (h *hostFs) WriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, sandboxDirectoryMode); err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}

	// Preserve the existing invalid-target error classification before creating
	// a temporary file. A NUL byte, for example, is rejected by the OS only
	// when the path is used, and should not leave a staging artifact behind.
	if _, err := os.Lstat(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	// Write to a short, unique file in the destination directory, then rename
	// it over the target. Keeping the temporary name independent of path means
	// the staging write does not consume any of the target's filename budget.
	tmpFile, tmpPath, err := createHostWriteTempFile(dir)
	if err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	defer removeFileIfPresent(tmpPath) // clean up on write/close/rename failure

	if err := writeAndCloseTempFile(tmpFile, data); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to replace original file: %w", err)
	}
	return nil
}

func newWriteFileTempName() (string, error) {
	var token [writeFileTempRandomBytes]byte
	if _, err := cryptorand.Read(token[:]); err != nil {
		return "", fmt.Errorf("generate temporary filename: %w", err)
	}
	return writeFileTempPrefix + hex.EncodeToString(token[:]), nil
}

func createHostWriteTempFile(dir string) (*os.File, string, error) {
	for attempt := 0; attempt < writeFileTempCreateTries; attempt++ {
		name, err := newWriteFileTempName()
		if err != nil {
			return nil, "", err
		}
		path := filepath.Join(dir, name)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, sandboxFileMode)
		if err == nil {
			return file, path, nil
		}
		if os.IsExist(err) {
			continue
		}
		return nil, "", err
	}
	return nil, "", fmt.Errorf("could not allocate a unique temporary filename after %d attempts", writeFileTempCreateTries)
}

func writeAndCloseTempFile(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			return fmt.Errorf("write: %w (close temp file: %w)", err, closeErr)
		}
		return fmt.Errorf("write: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return nil
}

func validateSandboxWriteTarget(root *os.Root, path string) error {
	if _, err := root.Lstat(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to write to temp file: %w", err)
	}
	return nil
}

func createSandboxWriteTempFile(root *os.Root, dir string) (*os.File, string, error) {
	for attempt := 0; attempt < writeFileTempCreateTries; attempt++ {
		name, err := newWriteFileTempName()
		if err != nil {
			return nil, "", err
		}
		relPath := filepath.Join(dir, name)
		file, err := root.OpenFile(relPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, sandboxFileMode)
		if err == nil {
			return file, relPath, nil
		}
		if os.IsExist(err) {
			continue
		}
		return nil, "", err
	}
	return nil, "", fmt.Errorf("could not allocate a unique temporary filename after %d attempts", writeFileTempCreateTries)
}

// sandboxFs is a sandboxed fileSystem that operates within a strictly defined workspace using os.Root.
type sandboxFs struct {
	workspace            string
	additionalWorkspaces []string
	protectedReadRoots   []string
	enforceCanonical     bool
}

func (r *sandboxFs) filesystemWorkDir() string {
	if r == nil {
		return ""
	}
	return r.workspace
}

func (r *sandboxFs) execute(path string, fn func(root *os.Root, relPath string) error) error {
	rootPath, relPath, err := r.resolve(path)
	if err != nil {
		return err
	}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return fmt.Errorf("failed to open workspace: %w", err)
	}
	defer closeSandboxRoot(root)

	return fn(root, relPath)
}

func removeFileIfPresent(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return
	}
}

func closeSandboxRoot(root *os.Root) {
	if root == nil {
		return
	}
	if err := root.Close(); err != nil {
		return
	}
}

func newSandboxFs(policy *FilesystemPolicy) *sandboxFs {
	if policy == nil {
		return &sandboxFs{enforceCanonical: true}
	}
	roots := policy.WritableRoots()
	if len(roots) == 0 {
		return &sandboxFs{enforceCanonical: true}
	}
	return &sandboxFs{
		workspace:            roots[0],
		additionalWorkspaces: append([]string(nil), roots[1:]...),
		protectedReadRoots:   policy.ProtectedReadRoots(),
		enforceCanonical:     true,
	}
}

func resolveCanonicalRoot(roots []string, candidate string) (string, string, bool, error) {
	for _, rootPath := range roots {
		relPath, err := filepath.Rel(rootPath, candidate)
		if err != nil {
			return "", "", false, fmt.Errorf("failed to calculate relative path: %w", err)
		}
		if filepath.IsLocal(relPath) {
			return rootPath, relPath, true, nil
		}
	}
	return "", "", false, nil
}

func resolveMissingRoot(roots []string, candidate string) (string, string, bool, error) {
	for _, rootPath := range roots {
		if _, statErr := os.Stat(rootPath); statErr == nil || !os.IsNotExist(statErr) {
			continue
		}
		relPath, relErr := filepath.Rel(rootPath, candidate)
		if relErr != nil {
			return "", "", false, fmt.Errorf("failed to calculate relative path: %w", relErr)
		}
		if filepath.IsLocal(relPath) {
			return rootPath, relPath, true, nil
		}
	}
	return "", "", false, nil
}

func resolveLexicalRoot(roots []string, candidate string) (string, string, bool, error) {
	for _, rootPath := range roots {
		relPath, relErr := filepath.Rel(rootPath, candidate)
		if relErr != nil {
			return "", "", false, fmt.Errorf("failed to calculate relative path: %w", relErr)
		}
		if filepath.IsLocal(relPath) {
			return rootPath, relPath, true, nil
		}
	}
	return "", "", false, nil
}
