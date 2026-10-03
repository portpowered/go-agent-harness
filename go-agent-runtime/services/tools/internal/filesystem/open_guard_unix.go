//go:build unix

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
	"syscall"

	"golang.org/x/sys/unix"
)

// On Unix the guard walks file descriptors. os.Root opens a directory (it
// resolves intermediate components and refuses an escape), then the guard
// checks that directory and every ancestor, reached through "..", against the
// protected identities. The final component is opened with O_NOFOLLOW
// relative to that verified directory, so no path is resolved again after
// the check; a final symlink is followed by the guard itself, one verified
// hop at a time. Writes create, rename and make directories only relative to
// a verified directory descriptor.

// maxSymlinkHops bounds how many final-component symlinks one operation
// follows, matching the Linux MAXSYMLINKS limit.
const maxSymlinkHops = 40

// maxAncestorDepth bounds the parent walk from an opened directory to the
// filesystem root. PATH_MAX (4096 bytes) holds at most 2048 components.
const maxAncestorDepth = 2048

// errPathEscapesRoot is the os.Root wording for a path that leaves the root,
// so isSandboxAccessDenied classifies the guard's own escape errors the same
// way.
var errPathEscapesRoot = errors.New("path escapes from parent")

// splitRootRel splits a root-relative path into its directory and final
// name. The root itself has no final name.
func splitRootRel(rel string) (string, string) {
	rel = filepath.Clean(rel)
	if rel == "." {
		return ".", ""
	}
	return filepath.Dir(rel), filepath.Base(rel)
}

// followRootLink resolves a symlink found at dir/name to a root-relative
// path. Like os.Root, it refuses an absolute target and one that leaves the
// root.
func followRootLink(dir, name, target string) (string, error) {
	link := filepath.Join(dir, name)
	if filepath.IsAbs(target) || filepath.VolumeName(target) != "" {
		return "", &fs.PathError{Op: "openat", Path: link, Err: errPathEscapesRoot}
	}
	resolved := filepath.Join(dir, target)
	if !filepath.IsLocal(resolved) && resolved != "." {
		return "", &fs.PathError{Op: "openat", Path: link, Err: errPathEscapesRoot}
	}
	return resolved, nil
}

func tooManyLinks(rel string) error {
	return &fs.PathError{Op: "openat", Path: rel, Err: fmt.Errorf("too many levels of symbolic links (more than %d)", maxSymlinkHops)}
}

// readFile reads rel after an open-time protection check.
func (g *openGuard) readFile(rel string) ([]byte, error) {
	file, err := g.openLeaf(rel, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer closeGuardFile(file)
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, &fs.PathError{Op: "read", Path: rel, Err: underlyingErr(err)}
	}
	return data, nil
}

// readDir lists rel after an open-time protection check, sorted by name like
// fs.ReadDir.
func (g *openGuard) readDir(rel string) ([]os.DirEntry, error) {
	dir, err := g.openLeaf(rel, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer closeGuardFile(dir)
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, &fs.PathError{Op: "readdirent", Path: rel, Err: underlyingErr(err)}
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

// writeFile replaces rel with data. A final symlink to an existing file is
// written through in place, and a dangling one is replaced, as before the
// guard; every other write stages a temporary file in the verified parent
// directory and renames it over the target.
func (g *openGuard) writeFile(rel string, data []byte) error {
	dirRel, name := splitRootRel(rel)
	if name == "" {
		return fmt.Errorf("failed to rename temp file over target: %w", &fs.PathError{Op: "rename", Path: rel, Err: unix.EISDIR})
	}
	dir, err := g.ensureDir(dirRel)
	if err != nil {
		return g.wrapWriteErr("failed to create parent directories", err)
	}
	defer closeGuardFile(dir)
	handled, err := g.writeThroughLink(dir, dirRel, name, data)
	if err != nil || handled {
		return err
	}
	return g.replaceAtomically(dir, name, data)
}

func (g *openGuard) wrapWriteErr(prefix string, err error) error {
	if errors.Is(err, ErrFilesystemAccessDenied) {
		return err
	}
	if isSandboxAccessDenied(err) {
		return fmt.Errorf("%s: access denied: %w", prefix, err)
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

// openVerifiedDir opens the directory rel through os.Root and checks it and
// all its ancestors.
func (g *openGuard) openVerifiedDir(rel string) (*os.File, error) {
	dir, err := g.root.Open(rel)
	if err != nil {
		return nil, err
	}
	info, err := dir.Stat()
	if err == nil && !info.IsDir() {
		err = &fs.PathError{Op: "openat", Path: rel, Err: unix.ENOTDIR}
	}
	if err == nil {
		err = g.verifyAncestors(dir)
	}
	if err != nil {
		closeGuardFile(dir)
		return nil, err
	}
	return dir, nil
}

// verifyAncestors refuses a directory that is, or is beneath, a protected
// root, walking up to the scope root. ".." is resolved by the kernel from the
// open descriptor, so a symlink cannot redirect the walk, and the work is one
// openat and fstat per component below the scope root. Those components are
// the ones os.Root has just opened, so they can be opened again. Above the
// scope root nothing can be swapped by a filesystem tool, and the pre-check
// has refused any scope root inside a protected root.
func (g *openGuard) verifyAncestors(dir *os.File) error {
	scope, err := g.root.Stat(".")
	if err != nil {
		return err
	}
	scopeStat, ok := scope.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("scope root has no device and inode: %T", scope.Sys())
	}
	return withDirFd(dir, func(fd int) error {
		var current unix.Stat_t
		if err := retryEINTR(func() error { return unix.Fstat(fd, &current) }); err != nil {
			return err
		}
		base := fd
		defer func() { closeOwnedFd(base, fd) }()
		for range maxAncestorDepth {
			if g.protected.isRootStat(&current) {
				return g.denied()
			}
			if current.Dev == scopeStat.Dev && current.Ino == scopeStat.Ino {
				return nil
			}
			var parentFd int
			err := retryEINTR(func() error {
				var openErr error
				parentFd, openErr = unix.Openat(base, "..", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
				return openErr
			})
			if err != nil {
				return err
			}
			closeOwnedFd(base, fd)
			base = parentFd
			var parent unix.Stat_t
			if err := retryEINTR(func() error { return unix.Fstat(base, &parent) }); err != nil {
				return err
			}
			if parent.Dev == current.Dev && parent.Ino == current.Ino {
				return nil // the filesystem root: a directory moved out of the scope
			}
			if g.isProtectedEntryAt(base, &parent, &current) {
				return g.denied()
			}
			current = parent
		}
		return g.denied()
	})
}

// isProtectedEntryAt reports whether child is, by name in parent, a
// protected root. It catches a protected root created after the snapshot (a
// new ~/.ssh swapped in mid-operation), which identity alone would miss; only
// directories that hold protected roots are looked up, by fstatat.
func (g *openGuard) isProtectedEntryAt(parentFd int, parent, child *unix.Stat_t) bool {
	for _, name := range g.protected.entryNamesUnder(parent) {
		var entry unix.Stat_t
		err := retryEINTR(func() error { return unix.Fstatat(parentFd, name, &entry, 0) })
		if err == nil && entry.Dev == child.Dev && entry.Ino == child.Ino {
			return true
		}
	}
	return false
}

// entryNamesUnder lists the names of the protected roots in the directory
// parent that did not exist at the snapshot; the ones that did are already
// matched by identity.
func (p *protectedIdentities) entryNamesUnder(parent *unix.Stat_t) []string {
	p.buildEntries()
	var names []string
	for _, entry := range p.entries {
		if entryStat, ok := entry.parent.Sys().(*syscall.Stat_t); ok && !entry.existed && entryStat.Dev == parent.Dev && entryStat.Ino == parent.Ino {
			names = append(names, entry.name)
		}
	}
	return names
}

// isRootStat is isRoot for a raw stat result.
func (p *protectedIdentities) isRootStat(stat *unix.Stat_t) bool {
	return slices.ContainsFunc(p.roots, func(root os.FileInfo) bool {
		rootStat, ok := root.Sys().(*syscall.Stat_t)
		return ok && rootStat.Dev == stat.Dev && rootStat.Ino == stat.Ino
	})
}

func closeOwnedFd(fd, borrowed int) {
	if fd == borrowed {
		return
	}
	if err := unix.Close(fd); err != nil {
		return
	}
}

// openLeaf opens rel without following a final symlink, after verifying its
// directory. A final symlink is resolved by the guard and opened again from
// its own verified directory. The opened file itself must not be a
// protected root.
func (g *openGuard) openLeaf(rel string, flag int) (*os.File, error) {
	for range maxSymlinkHops + 1 {
		dirRel, name := splitRootRel(rel)
		if name == "" {
			return g.openVerifiedDir(rel)
		}
		dir, err := g.openVerifiedDir(dirRel)
		if err != nil {
			return nil, err
		}
		// A protected root created after the snapshot (~/.netrc) is refused
		// by its name in the verified directory.
		if err := g.refuseProtectedEntry(dir, name); err != nil {
			closeGuardFile(dir)
			return nil, err
		}
		file, err := openAt(dir, name, flag|unix.O_NOFOLLOW)
		if isSymlinkOpenErr(err) {
			rel, err = followLinkAt(dir, dirRel, name)
			closeGuardFile(dir)
			if err != nil {
				return nil, err
			}
			continue
		}
		closeGuardFile(dir)
		if err != nil {
			return nil, &fs.PathError{Op: "openat", Path: rel, Err: underlyingErr(err)}
		}
		if err := g.checkOpened(file); err != nil {
			closeGuardFile(file)
			return nil, err
		}
		return file, nil
	}
	return nil, tooManyLinks(rel)
}

func (g *openGuard) checkOpened(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if g.protected.isRoot(info) {
		return g.denied()
	}
	return nil
}

// ensureDir opens the directory rel, creating missing components one at a
// time in their verified parent. A protected root cannot be created, and the
// protected identities are refreshed after each new directory.
func (g *openGuard) ensureDir(rel string) (*os.File, error) {
	dir, err := g.openVerifiedDir(rel)
	if err == nil || rel == "." || !errors.Is(err, fs.ErrNotExist) {
		return dir, err
	}
	parent, err := g.ensureDir(filepath.Dir(rel))
	if err != nil {
		return nil, err
	}
	defer closeGuardFile(parent)
	name := filepath.Base(rel)
	if err := g.refuseProtectedEntry(parent, name); err != nil {
		return nil, err
	}
	if err := mkdirAt(parent, name, sandboxDirectoryMode); err != nil && !errors.Is(err, unix.EEXIST) {
		return nil, &fs.PathError{Op: "mkdirat", Path: rel, Err: underlyingErr(err)}
	}
	g.protected.refresh()
	child, err := openAt(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW)
	if err != nil {
		// A symlink or file raced into place: verify it the long way.
		return g.openVerifiedDir(rel)
	}
	if err := g.checkOpened(child); err != nil {
		closeGuardFile(child)
		return nil, err
	}
	return child, nil
}

func (g *openGuard) refuseProtectedEntry(dir *os.File, name string) error {
	info, err := dir.Stat()
	if err != nil {
		return err
	}
	if g.protected.isEntry(info, name) {
		return g.denied()
	}
	return nil
}

// writeThroughLink writes data through a final symlink whose target exists,
// in place, as os.Root.WriteFile did. It reports false for a target that is
// not a symlink or whose link chain ends at a missing file.
func (g *openGuard) writeThroughLink(dir *os.File, dirRel, name string, data []byte) (bool, error) {
	if !isSymlinkAt(dir, name) {
		return false, nil
	}
	rel, err := followLinkAt(dir, dirRel, name)
	if err != nil {
		return true, g.wrapWriteErr("failed to authorize file", err)
	}
	file, err := g.openLeaf(rel, unix.O_WRONLY)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, g.wrapWriteErr("failed to write file", err)
	}
	defer closeGuardFile(file)
	// Truncate only after openLeaf checked the file's identity: O_TRUNC
	// would change a protected file before the check.
	if err := file.Truncate(0); err != nil {
		return true, g.wrapWriteErr("failed to write file", err)
	}
	if _, err := file.Write(data); err != nil {
		return true, g.wrapWriteErr("failed to write file", err)
	}
	return true, nil
}

func (g *openGuard) replaceAtomically(dir *os.File, name string, data []byte) error {
	if err := g.refuseProtectedEntry(dir, name); err != nil {
		return err
	}
	if err := lstatAt(dir, name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("failed to write to temp file: %w", err)
	}
	tmpFile, tmpName, err := createTempAt(dir)
	if err != nil {
		return fmt.Errorf("failed to write to temp file: %w", err)
	}
	defer removeAt(dir, tmpName)
	if err := writeAndCloseTempFile(tmpFile, data); err != nil {
		return fmt.Errorf("failed to write to temp file: %w", err)
	}
	if err := renameAt(dir, tmpName, name); err != nil {
		return g.wrapWriteErr("failed to rename temp file over target", &fs.PathError{Op: "renameat", Path: name, Err: err})
	}
	return nil
}

func createTempAt(dir *os.File) (*os.File, string, error) {
	for range writeFileTempCreateTries {
		name, err := newWriteFileTempName()
		if err != nil {
			return nil, "", err
		}
		file, err := openAt(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW)
		if err == nil {
			return file, name, nil
		}
		if !errors.Is(err, unix.EEXIST) {
			return nil, "", &fs.PathError{Op: "openat", Path: name, Err: err}
		}
	}
	return nil, "", fmt.Errorf("could not allocate a unique temporary filename after %d attempts", writeFileTempCreateTries)
}

func followLinkAt(dir *os.File, dirRel, name string) (string, error) {
	target, err := readlinkAt(dir, name)
	if err != nil {
		return "", &fs.PathError{Op: "readlinkat", Path: filepath.Join(dirRel, name), Err: err}
	}
	return followRootLink(dirRel, name, target)
}

// isSymlinkOpenErr reports the O_NOFOLLOW refusal of a final symlink (ELOOP;
// EMLINK on FreeBSD).
func isSymlinkOpenErr(err error) bool {
	return errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EMLINK)
}

func underlyingErr(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

// The *at helpers run one system call on a directory's descriptor through
// SyscallConn, which keeps the descriptor alive and leaves its mode alone.

func withDirFd(dir *os.File, call func(fd int) error) error {
	conn, err := dir.SyscallConn()
	if err != nil {
		return err
	}
	var callErr error
	if err := conn.Control(func(fd uintptr) { callErr = call(int(fd)) }); err != nil {
		return err
	}
	return callErr
}

func openAt(dir *os.File, name string, flag int) (*os.File, error) {
	var opened int
	err := withDirFd(dir, func(fd int) error {
		return retryEINTR(func() error {
			var openErr error
			opened, openErr = unix.Openat(fd, name, flag|unix.O_CLOEXEC, sandboxFileMode)
			return openErr
		})
	})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(opened), filepath.Join(dir.Name(), name)), nil
}

func lstatAt(dir *os.File, name string) error {
	var stat unix.Stat_t
	err := withDirFd(dir, func(fd int) error {
		return retryEINTR(func() error { return unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW) })
	})
	if err != nil {
		return &fs.PathError{Op: "fstatat", Path: name, Err: err}
	}
	return nil
}

func isSymlinkAt(dir *os.File, name string) bool {
	var stat unix.Stat_t
	err := withDirFd(dir, func(fd int) error {
		return retryEINTR(func() error { return unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW) })
	})
	return err == nil && stat.Mode&unix.S_IFMT == unix.S_IFLNK
}

func readlinkAt(dir *os.File, name string) (string, error) {
	buffer := make([]byte, unix.PathMax)
	var size int
	err := withDirFd(dir, func(fd int) error {
		return retryEINTR(func() error {
			var readErr error
			size, readErr = unix.Readlinkat(fd, name, buffer)
			return readErr
		})
	})
	if err != nil {
		return "", err
	}
	return string(buffer[:size]), nil
}

func mkdirAt(dir *os.File, name string, mode uint32) error {
	return withDirFd(dir, func(fd int) error {
		return retryEINTR(func() error { return unix.Mkdirat(fd, name, mode) })
	})
}

func renameAt(dir *os.File, from, to string) error {
	return withDirFd(dir, func(fd int) error {
		return retryEINTR(func() error { return unix.Renameat(fd, from, fd, to) })
	})
}

func removeAt(dir *os.File, name string) {
	if err := withDirFd(dir, func(fd int) error {
		return retryEINTR(func() error { return unix.Unlinkat(fd, name, 0) })
	}); err != nil {
		return
	}
}

// retryEINTR repeats a system call interrupted by a signal, as the os
// package does for its own calls.
func retryEINTR(call func() error) error {
	for {
		if err := call(); !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
