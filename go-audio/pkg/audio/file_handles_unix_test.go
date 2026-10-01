//go:build linux || darwin

package audio

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// processDescriptorDir lists the calling process's open descriptors: procfs
// on Linux and devfs on Darwin. Other platforms have no descriptor listing,
// so the S9 handle-release tests (and the Unix mode-bit test) are built only
// on Linux and Darwin.
func processDescriptorDir() string {
	if runtime.GOOS == "linux" {
		return "/proc/self/fd"
	}
	return "/dev/fd"
}

// processOpenHandleCount returns the number of open descriptors. Closing a
// file releases its descriptor synchronously, so no settling is needed.
func processOpenHandleCount(t *testing.T) int {
	t.Helper()
	dir, err := os.Open(processDescriptorDir())
	if err != nil {
		t.Fatalf("open %s: %v", processDescriptorDir(), err)
	}
	defer closeForTest(t, dir)
	// Names only: a stat of each entry would race with descriptors that
	// close while the directory is listed.
	names, err := dir.Readdirnames(-1)
	if err != nil {
		t.Fatalf("list %s: %v", processDescriptorDir(), err)
	}
	return len(names)
}

// processHandleCountSettleTolerance absorbs one runtime-owned descriptor
// (for example the poller) appearing between two counts.
const processHandleCountSettleTolerance = 1

func assertHandleCountWithinTolerance(t *testing.T, got, want int, operation string) {
	t.Helper()
	if !withinHandleCountTolerance(got, want) {
		t.Fatalf("open-handle count after %s = %d, want %d +/- %d", operation, got, want, processHandleCountSettleTolerance)
	}
}

func withinHandleCountTolerance(got, want int) bool {
	delta := got - want
	if delta < 0 {
		delta = -delta
	}
	return delta <= processHandleCountSettleTolerance
}

func TestFileSourceOwnedHandleRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned.raw")
	if err := os.WriteFile(path, pcmBytes(make([]int16, FrameSize)), 0o600); err != nil {
		t.Fatal(err)
	}

	before := processOpenHandleCount(t)
	source, err := NewFileSource(path, nil)
	if err != nil {
		t.Fatalf("NewFileSource() error = %v", err)
	}
	t.Cleanup(func() { closeForTest(t, source) })
	opened := processOpenHandleCount(t)
	if opened <= before {
		t.Fatalf("open-handle count after source open = %d, before = %d; owned handle was not observed", opened, before)
	}

	if err := source.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	afterFirst := processOpenHandleCount(t)
	assertHandleCountWithinTolerance(t, afterFirst, before, "source first close")
	if afterFirst >= opened {
		t.Fatalf("open-handle count after source first close = %d, opened = %d; owned handle was not released", afterFirst, opened)
	}

	if err := source.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	afterSecond := processOpenHandleCount(t)
	if afterSecond != afterFirst {
		t.Fatalf("open-handle count after source second close = %d, first close = %d; idempotent close changed the count", afterSecond, afterFirst)
	}

	if err := source.ReadFrame(context.Background(), make([]int16, FrameSize)); !errors.Is(err, ErrClosed) {
		t.Fatalf("ReadFrame after Close() = %v, want ErrClosed", err)
	}
}

func TestFileSinkOwnedHandleRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned.raw")
	before := processOpenHandleCount(t)
	sink, err := NewFileSink(path, nil)
	if err != nil {
		t.Fatalf("NewFileSink() error = %v", err)
	}
	t.Cleanup(func() { closeForTest(t, sink) })
	opened := processOpenHandleCount(t)
	if opened <= before {
		t.Fatalf("open-handle count after sink open = %d, before = %d; owned handle was not observed", opened, before)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	afterFirst := processOpenHandleCount(t)
	assertHandleCountWithinTolerance(t, afterFirst, before, "sink first close")
	if afterFirst >= opened {
		t.Fatalf("open-handle count after sink first close = %d, opened = %d; owned handle was not released", afterFirst, opened)
	}

	if err := sink.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	afterSecond := processOpenHandleCount(t)
	if afterSecond != afterFirst {
		t.Fatalf("open-handle count after sink second close = %d, first close = %d; idempotent close changed the count", afterSecond, afterFirst)
	}
	if err := sink.WriteFrame(context.Background(), make([]int16, FrameSize)); !errors.Is(err, ErrClosed) {
		t.Fatalf("WriteFrame after Close() = %v, want ErrClosed", err)
	}
}

// Mode bits gate opens only on Unix; Windows ignores them, so this test is
// built with the Unix descriptor tests above. The superuser bypasses mode bits, and the source must
// then honour the operating system's decision and open the file.
func TestFileSourceModeZeroFileOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "permission-denied.raw")
	if err := os.WriteFile(path, pcmBytes([]int16{1}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}

	source, err := NewFileSource(path, nil)
	if os.Geteuid() == 0 {
		if err != nil {
			t.Fatalf("NewFileSource() as superuser = %v, want the OS to allow the open", err)
		}
		closeForTest(t, source)
		return
	}
	assertSourceStreamError(t, err, "open", path)
}
