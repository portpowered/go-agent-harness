//go:build linux || darwin

package devices

import (
	"os"
	"runtime"
	"testing"
	"time"
)

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
