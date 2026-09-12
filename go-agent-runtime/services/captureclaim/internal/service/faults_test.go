package service

import (
	"fmt"
	"io/fs"
	"os"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/captureclaim"
)

type faultFileSystem struct {
	base           osFileSystem
	mu             sync.Mutex
	lstatPath      string
	lstatErr       error
	lstatFailed    bool
	openReadOnly   int
	failReadOnlyAt int
	openErr        error
	failWrite      error
	writeFailed    bool
	syncCalls      int
	failSyncAt     int
	syncErr        error
	closeCalls     int
	failCloseAt    int
	closeErr       error
	createTempErr  error
	linkErr        error
	removeErr      error
	removeFailed   bool
	events         []string
}

type overwriteOnLinkFileSystem struct{ faultFileSystem }

func (f *overwriteOnLinkFileSystem) Link(oldPath, newPath string) error {
	f.record("overwrite-link:" + oldPath + ":" + newPath)
	if _, err := os.Stat(newPath); err == nil {
		data, readErr := os.ReadFile(oldPath)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(newPath, data, 0o600)
	}
	return f.base.Link(oldPath, newPath)
}

func (f *faultFileSystem) record(event string) {
	f.mu.Lock()
	f.events = append(f.events, event)
	f.mu.Unlock()
}

func (f *faultFileSystem) MkdirAll(path string, mode fs.FileMode) error {
	f.record("mkdir:" + path)
	return f.base.MkdirAll(path, mode)
}

func (f *faultFileSystem) Lstat(path string) (fs.FileInfo, error) {
	f.record("lstat:" + path)
	f.mu.Lock()
	inject := f.lstatErr != nil && !f.lstatFailed && (f.lstatPath == "" || f.lstatPath == path)
	statErr := f.lstatErr
	if inject {
		f.lstatFailed = true
	}
	f.mu.Unlock()
	if inject {
		return nil, statErr
	}
	return f.base.Lstat(path)
}

func (f *faultFileSystem) OpenFile(path string, flag int, mode fs.FileMode) (captureclaim.File, error) {
	f.record(fmt.Sprintf("open:%s:%d", path, flag))
	if flag == os.O_RDONLY {
		f.mu.Lock()
		f.openReadOnly++
		call := f.openReadOnly
		openErr := f.openErr
		fail := f.failReadOnlyAt == call
		f.mu.Unlock()
		if fail {
			return nil, openErr
		}
	}
	file, err := f.base.OpenFile(path, flag, mode)
	if err != nil {
		return nil, err
	}
	return &faultFile{parent: f, File: file}, nil
}

func (f *faultFileSystem) ReadFile(path string) ([]byte, error) { return f.base.ReadFile(path) }

func (f *faultFileSystem) CreateTemp(dir, pattern string) (captureclaim.File, error) {
	f.record("temp:" + dir + "/" + pattern)
	if f.createTempErr != nil {
		return nil, f.createTempErr
	}
	file, err := f.base.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	return &faultFile{parent: f, File: file}, nil
}

func (f *faultFileSystem) Link(oldPath, newPath string) error {
	f.record("link:" + oldPath + ":" + newPath)
	if f.linkErr != nil {
		return f.linkErr
	}
	return f.base.Link(oldPath, newPath)
}

func (f *faultFileSystem) Remove(path string) error {
	f.record("remove:" + path)
	if f.removeErr != nil && !f.removeFailed {
		f.removeFailed = true
		return f.removeErr
	}
	return f.base.Remove(path)
}

func (f *faultFileSystem) SameFile(left, right fs.FileInfo) bool { return f.base.SameFile(left, right) }

type faultFile struct {
	captureclaim.File
	parent *faultFileSystem
}

func (f *faultFile) Write(data []byte) (int, error) {
	if f.parent.failWrite != nil && !f.parent.writeFailed {
		f.parent.writeFailed = true
		return 0, f.parent.failWrite
	}
	return f.File.Write(data)
}

func (f *faultFile) Sync() error {
	f.parent.record("sync:" + f.Name())
	f.parent.mu.Lock()
	f.parent.syncCalls++
	call := f.parent.syncCalls
	syncErr := f.parent.syncErr
	fail := f.parent.failSyncAt == call
	f.parent.mu.Unlock()
	if fail {
		return syncErr
	}
	return f.File.Sync()
}

func (f *faultFile) Close() error {
	f.parent.record("close:" + f.Name())
	f.parent.mu.Lock()
	f.parent.closeCalls++
	call := f.parent.closeCalls
	closeErr := f.parent.closeErr
	fail := f.parent.failCloseAt == call
	f.parent.mu.Unlock()
	if fail {
		return closeErr
	}
	return f.File.Close()
}

func (f *faultFile) Stat() (fs.FileInfo, error) { return f.File.Stat() }
