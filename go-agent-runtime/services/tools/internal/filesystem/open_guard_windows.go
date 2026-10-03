//go:build windows

package filesystem

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/windows"
)

// On Windows the guard checks every handle it opens or creates by the
// handle's final path (GetFinalPathNameByHandle) and by file identity. The
// kernel resolves the final path for the object the handle refers to, so a
// symlink or junction swapped in before the open cannot hide a protected
// location: read_file, read_image and list_dir are decided at open time.
//
// Writes are only partly decided at open time; the gaps are path-based
// because os.Root has no handle-relative mkdir or rename:
//   - Missing parent directories are created by root.MkdirAll, by path,
//     before any handle check. A junction swapped in before it can create
//     empty directories inside a protected location; the parent-directory
//     handle check that follows then refuses the write, but the directories
//     stay.
//   - The staged temporary file is checked by handle when it is created.
//     The rename window then spans writing the data, closing the file and
//     root.Rename resolving both paths again: a junction swapped in on the
//     destination's parent path anywhere in that window redirects the
//     rename into the protected location.
//   - A write through an existing symlink opens the target, checks its
//     handle, and only then truncates and writes, so it has no window.

func (g *openGuard) readFile(rel string) ([]byte, error) {
	file, err := g.openChecked(rel)
	if err != nil {
		return nil, err
	}
	defer closeGuardFile(file)
	return io.ReadAll(file)
}

func (g *openGuard) readDir(rel string) ([]os.DirEntry, error) {
	dir, err := g.openChecked(rel)
	if err != nil {
		return nil, err
	}
	defer closeGuardFile(dir)
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

func (g *openGuard) writeFile(rel string, data []byte) error {
	dir := filepath.Dir(rel)
	if err := makeSandboxParent(g.root, dir); err != nil {
		return err
	}
	g.protected.refresh()
	dirFile, err := g.openChecked(dir)
	if err != nil {
		return wrapWindowsWriteErr("failed to create parent directories", err)
	}
	dirInfo, err := dirFile.Stat()
	closeGuardFile(dirFile)
	if err != nil {
		return wrapWindowsWriteErr("failed to create parent directories", err)
	}
	if g.protected.isEntry(dirInfo, filepath.Base(rel)) {
		return g.denied()
	}
	handled, err := g.writeExistingSymlink(rel, data)
	if err != nil || handled {
		return err
	}
	if err := validateSandboxWriteTarget(g.root, rel); err != nil {
		return err
	}
	return g.writeAtomically(rel, dir, data)
}

func (g *openGuard) openChecked(rel string) (*os.File, error) {
	file, err := g.root.Open(rel)
	if err != nil {
		return nil, err
	}
	if err := g.checkHandle(file); err != nil {
		closeGuardFile(file)
		return nil, err
	}
	return file, nil
}

// checkHandle refuses a handle whose final path is, or is beneath, a
// protected root, or whose file is a protected root.
func (g *openGuard) checkHandle(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if g.protected.isRoot(info) {
		return g.denied()
	}
	path, err := finalHandlePath(file)
	if err != nil {
		return err
	}
	if g.isProtectedPath(path) {
		return g.denied()
	}
	return nil
}

func (g *openGuard) isProtectedPath(path string) bool {
	return slices.ContainsFunc(g.protectedRoots, func(root string) bool { return isWithinProtectedRoot(path, root) })
}

func (g *openGuard) writeExistingSymlink(rel string, data []byte) (bool, error) {
	lstat, err := g.root.Lstat(rel)
	if err != nil || lstat.Mode()&os.ModeSymlink == 0 {
		return false, nil
	}
	file, err := g.root.OpenFile(rel, os.O_WRONLY, sandboxFileMode)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, wrapWindowsWriteErr("failed to write file", err)
	}
	defer closeGuardFile(file)
	if err := g.checkHandle(file); err != nil {
		return true, err
	}
	if err := file.Truncate(0); err != nil {
		return true, wrapWindowsWriteErr("failed to write file", err)
	}
	if _, err := file.Write(data); err != nil {
		return true, wrapWindowsWriteErr("failed to write file", err)
	}
	return true, nil
}

func (g *openGuard) writeAtomically(rel, dir string, data []byte) error {
	tmpFile, tmpRel, err := createSandboxWriteTempFile(g.root, dir)
	if err != nil {
		return fmt.Errorf("failed to write to temp file: %w", err)
	}
	defer removeSandboxFileIfPresent(g.root, tmpRel)
	tmpPath, err := finalHandlePath(tmpFile)
	if err == nil && g.isProtectedPath(filepath.Join(filepath.Dir(tmpPath), filepath.Base(rel))) {
		err = g.denied()
	}
	if err != nil {
		closeGuardFile(tmpFile)
		return err
	}
	if err := writeAndCloseTempFile(tmpFile, data); err != nil {
		return fmt.Errorf("failed to write to temp file: %w", err)
	}
	if err := g.root.Rename(tmpRel, rel); err != nil {
		return wrapWindowsWriteErr("failed to rename temp file over target", err)
	}
	return nil
}

func wrapWindowsWriteErr(prefix string, err error) error {
	if errors.Is(err, ErrFilesystemAccessDenied) {
		return err
	}
	if isSandboxAccessDenied(err) {
		return fmt.Errorf("%s: access denied: %w", prefix, err)
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

// finalPathNormalizedDOS is FILE_NAME_NORMALIZED|VOLUME_NAME_DOS (both 0),
// which x/sys/windows does not name.
const finalPathNormalizedDOS = 0

// finalHandlePath is the normalized DOS path of the object a handle refers
// to, without the \\?\ prefix.
func finalHandlePath(file *os.File) (string, error) {
	conn, err := file.SyscallConn()
	if err != nil {
		return "", err
	}
	var path string
	var pathErr error
	if err := conn.Control(func(handle uintptr) {
		path, pathErr = finalPathOfHandle(windows.Handle(handle))
	}); err != nil {
		return "", err
	}
	return path, pathErr
}

func finalPathOfHandle(handle windows.Handle) (string, error) {
	buffer := make([]uint16, windows.MAX_PATH)
	for {
		size, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), finalPathNormalizedDOS)
		if err != nil {
			return "", err
		}
		if int(size) < len(buffer) {
			path := windows.UTF16ToString(buffer[:size])
			if rest, ok := strings.CutPrefix(path, `\\?\UNC\`); ok {
				return `\\` + rest, nil
			}
			return strings.TrimPrefix(path, `\\?\`), nil
		}
		buffer = make([]uint16, size)
	}
}

func makeSandboxParent(root *os.Root, dir string) error {
	if dir == "." {
		return nil
	}
	if err := root.MkdirAll(dir, sandboxDirectoryMode); err != nil {
		return wrapWindowsWriteErr("failed to create parent directories", err)
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
	for range writeFileTempCreateTries {
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

func removeSandboxFileIfPresent(root *os.Root, path string) {
	if err := root.Remove(path); err != nil && !os.IsNotExist(err) {
		return
	}
}
