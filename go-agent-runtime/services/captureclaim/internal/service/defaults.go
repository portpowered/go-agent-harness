package service

import (
	"io/fs"
	"os"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/captureclaim"
)

type osFileSystem struct{}

func (osFileSystem) MkdirAll(path string, mode fs.FileMode) error { return os.MkdirAll(path, mode) }
func (osFileSystem) Lstat(path string) (fs.FileInfo, error)       { return os.Lstat(path) }
func (osFileSystem) OpenFile(path string, flag int, mode fs.FileMode) (captureclaim.File, error) {
	return os.OpenFile(path, flag, mode)
}
func (osFileSystem) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
func (osFileSystem) CreateTemp(dir, pattern string) (captureclaim.File, error) {
	return os.CreateTemp(dir, pattern)
}
func (osFileSystem) Link(oldPath, newPath string) error { return os.Link(oldPath, newPath) }
func (osFileSystem) Remove(path string) error           { return os.Remove(path) }
func (osFileSystem) SameFile(left, right fs.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right)
}

type wallClock struct{}

func (wallClock) Now() time.Time               { return time.Now() }
func (wallClock) Sleep(duration time.Duration) { time.Sleep(duration) }

type osHost struct{}

func (osHost) Hostname() (string, error) { return os.Hostname() }

type osProcess struct{}

func (osProcess) PID() int { return os.Getpid() }
