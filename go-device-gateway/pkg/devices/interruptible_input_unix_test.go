//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package devices

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestOpenInterruptibleInputDuplicatesPipeAndHonorsDeadline(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, "read", read)
	defer closeForTest(t, "write", write)

	dup, err := OpenInterruptibleInput(read)
	if err != nil {
		t.Fatalf("OpenInterruptibleInput() = %v", err)
	}
	defer closeForTest(t, "dup", dup)
	if err := dup.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline() = %v", err)
	}
	var one [1]byte
	if _, err := dup.Read(one[:]); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Read() error = %v, want deadline exceeded", err)
	}
	if _, err := write.Write([]byte{'x'}); err != nil {
		t.Fatal(err)
	}
	if err := dup.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clear read deadline = %v", err)
	}
	if count, err := dup.Read(one[:]); err != nil || count != 1 || one[0] != 'x' {
		t.Fatalf("Read() after write = (%d, %v), byte %q; want one x", count, err, one[0])
	}
}

// This test exercises the descriptor shape produced by a real child process.
// Darwin can leave a direct os.Stdin pipe in a syscall.Read after its owner
// closes the file; the duplicated nonblocking descriptor must instead wake on
// its deadline and let the session cancellation path make progress.
func TestOpenInterruptibleInputInheritedPipeSubprocess(t *testing.T) {
	if os.Getenv("GO_AGENT_INTERRUPTIBLE_INPUT_CHILD") == "1" {
		if code := runInheritedPipeChild(t); code != 0 {
			os.Exit(code)
		}
		return
	}

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, "read", read)
	defer closeForTest(t, "write", write)
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run", "^TestOpenInterruptibleInputInheritedPipeSubprocess$")
	cmd.Env = append(os.Environ(), "GO_AGENT_INTERRUPTIBLE_INPUT_CHILD=1")
	cmd.ExtraFiles = []*os.File{read}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("inherited-pipe child = %v, output %s", err, output)
	}
}

// runInheritedPipeChild runs the child half and returns its exit code, so the
// deferred closes run before the caller exits the process.
func runInheritedPipeChild(t *testing.T) int {
	t.Helper()
	inherited := os.NewFile(uintptr(3), "inherited-pipe")
	if inherited == nil {
		return 2
	}
	defer closeForTest(t, "inherited", inherited)
	dup, err := OpenInterruptibleInput(inherited)
	if err != nil {
		return 3
	}
	defer closeForTest(t, "dup", dup)
	if err := dup.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		return 4
	}
	var one [1]byte
	if _, err := dup.Read(one[:]); !errors.Is(err, os.ErrDeadlineExceeded) {
		return 5
	}
	return 0
}

// processDescriptorDir lists the calling process's open descriptors: procfs
// on Linux and devfs on Darwin. Other platforms have no descriptor listing,
// so the S9 handle-release tests are built only where one exists.
func processDescriptorDir() string {
	if runtime.GOOS == "linux" {
		return "/proc/self/fd"
	}
	return "/dev/fd"
}

// processOpenHandleCount returns the number of open descriptors. Closing a
// handle releases its descriptor synchronously, so no settling is needed.
func processOpenHandleCount(t *testing.T) int {
	t.Helper()
	dir, err := os.Open(processDescriptorDir())
	if err != nil {
		t.Fatalf("open %s: %v", processDescriptorDir(), err)
	}
	defer closeForTest(t, "descriptor dir", dir)
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

func TestDeviceAdaptersS9LifecycleBaseline(t *testing.T) {
	beforeHandles := processOpenHandleCount(t)
	beforeGoroutines := runtime.NumGoroutine()
	r := adapterTestRegistry(t)
	const iterations = 16
	for range iterations {
		source, err := NewDeviceSource(r, "virtual:input")
		if err != nil {
			t.Fatal(err)
		}
		sink, err := NewDeviceSink(r, "virtual:output")
		if err != nil {
			closeForTest(t, "source", source)
			t.Fatal(err)
		}
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
		if err := sink.Close(); err != nil {
			t.Fatal(err)
		}
	}
	got := r.Observations()
	if got.OpenCount != iterations*2 || got.ReleaseCount != iterations*2 {
		t.Fatalf("lifecycle observations=%+v, want %d opens and releases", got, iterations*2)
	}
	assertHandleCountWithinTolerance(t, processOpenHandleCount(t), beforeHandles, "device source/sink lifecycle")
	deadline := time.Now().Add(500 * time.Millisecond)
	for current := runtime.NumGoroutine(); current > beforeGoroutines+2 && time.Now().Before(deadline); current = runtime.NumGoroutine() {
		runtime.Gosched()
	}
	if current := runtime.NumGoroutine(); current > beforeGoroutines+2 {
		t.Fatalf("goroutines after lifecycle=%d, want <= %d", current, beforeGoroutines+2)
	}
}
