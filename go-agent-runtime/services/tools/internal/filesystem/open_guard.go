package filesystem

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The path pre-check (isProtected and resolve) runs before the os.Root
// operation, so on its own it cannot stop a symlink that is swapped in
// between the check and the open. With a broad scope root ("/" or a parent of
// $HOME) os.Root contains the credential stores, and such a swap would
// redirect a read or a write into one. openGuard makes the decision at open
// time instead: every directory and file a filesystem tool opens, creates or
// renames into is compared by identity (device and inode, or the Windows file
// index) with the protected roots. The pre-check stays for its clear refusal
// messages; the guard is authoritative.

// protectedEntry is an existing directory that holds a protected root, and the
// protected root's name in it. It lets the guard refuse to create a protected
// root (~/.ssh, ~/.netrc) that does not exist yet.
type protectedEntry struct {
	parent os.FileInfo
	name   string
}

// protectedIdentities is a snapshot, by file identity, of the protected roots
// that exist and, built on first use, of the directories that would hold the
// ones that do not.
type protectedIdentities struct {
	paths   []string
	roots   []os.FileInfo
	entries []protectedEntry
	built   bool
	// pending lists protected roots whose parent directory was missing when
	// the entries were built; refresh re-checks only these.
	pending []string
}

func snapshotProtectedIdentities(protectedRoots []string) *protectedIdentities {
	snapshot := &protectedIdentities{paths: protectedRoots}
	for _, path := range protectedRoots {
		snapshot.addRoot(path)
	}
	return snapshot
}

func (p *protectedIdentities) addRoot(path string) {
	if info, err := os.Stat(path); err == nil && !p.isRoot(info) {
		p.roots = append(p.roots, info)
	}
}

func (p *protectedIdentities) addEntries(paths []string) {
	parents := make(map[string]os.FileInfo, len(paths))
	for _, path := range paths {
		parentPath := filepath.Dir(path)
		parent, seen := parents[parentPath]
		if !seen {
			if info, err := os.Stat(parentPath); err == nil {
				parent = info
			}
			parents[parentPath] = parent
		}
		if parent == nil {
			p.pending = append(p.pending, path)
			continue
		}
		p.entries = append(p.entries, protectedEntry{parent: parent, name: filepath.Base(path)})
	}
}

// refresh records protected roots whose parent directory now exists. The
// guard calls it after creating a directory, so a write that first creates
// ~/.config cannot then create ~/.config/gh.
func (p *protectedIdentities) refresh() {
	if !p.built || len(p.pending) == 0 {
		return
	}
	pending := p.pending
	p.pending = nil
	for _, path := range pending {
		p.addRoot(path)
	}
	p.addEntries(pending)
}

func (p *protectedIdentities) isRoot(info os.FileInfo) bool {
	return slices.ContainsFunc(p.roots, func(root os.FileInfo) bool { return os.SameFile(root, info) })
}

// isEntry reports whether name in the directory parent is a protected root.
// Names fold case on case-insensitive platforms, where ~/.SSH is ~/.ssh.
func (p *protectedIdentities) isEntry(parent os.FileInfo, name string) bool {
	if !p.built {
		p.built = true
		p.addEntries(p.paths)
	}
	return slices.ContainsFunc(p.entries, func(entry protectedEntry) bool {
		if entry.name != name && (!caseInsensitivePaths() || !strings.EqualFold(entry.name, name)) {
			return false
		}
		return os.SameFile(entry.parent, parent)
	})
}

// containsAncestorOf reports whether path or an existing ancestor of it is a
// protected root. It is the pre-check's path-based form of the guard's
// identity walk.
func (p *protectedIdentities) containsAncestorOf(path string) bool {
	if len(p.roots) == 0 {
		return false
	}
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		if info, err := os.Stat(current); err == nil && p.isRoot(info) {
			return true
		}
		if filepath.Dir(current) == current {
			return false
		}
	}
}

// openGuard performs one filesystem operation's opens beneath an os.Root and
// refuses, at open time, any that reach a protected root.
type openGuard struct {
	root      *os.Root
	protected *protectedIdentities
	// protectedRoots are the protected paths, for the platforms that check
	// an opened handle's final path.
	protectedRoots []string
	// denied builds the operation's refusal (a read or a write refusal with
	// the same classification as the pre-check).
	denied func() error
}

func newOpenGuard(root *os.Root, protected *protectedIdentities, protectedRoots []string, denied func() error) *openGuard {
	return &openGuard{root: root, protected: protected, protectedRoots: protectedRoots, denied: denied}
}

func closeGuardFile(file *os.File) {
	if file == nil {
		return
	}
	if err := file.Close(); err != nil {
		return
	}
}
