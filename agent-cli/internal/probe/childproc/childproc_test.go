package childproc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestPipeAndCancellationClassification(t *testing.T) {
	if !IsPipeClosure(errors.New("write: broken pipe")) || !IsPipeClosure(io.ErrClosedPipe) || !IsPipeClosure(os.ErrClosed) {
		t.Fatal("closed input pipe errors were not recognized")
	}
	if IsPipeClosure(errors.New("permission denied")) || IsPipeClosure(nil) {
		t.Fatal("unrelated pipe error was recognized as closed input")
	}
	if !IsSignalShutdown(os.ErrClosed) || !IsSignalShutdown(io.ErrClosedPipe) || IsSignalShutdown(errors.New("other")) {
		t.Fatal("signal shutdown classification is wrong")
	}
	live := context.Background()
	var missing context.Context // exercises the nil-context guard
	if IsCancellation(live, context.Canceled) || IsCancellation(missing, context.Canceled) {
		t.Fatal("cancellation was recognized without a done context")
	}
	done, cancel := context.WithCancel(context.Background())
	cancel()
	if IsCancellation(done, nil) {
		t.Fatal("nil error was recognized as cancellation")
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, os.ErrClosed, io.ErrClosedPipe} {
		if !IsCancellation(done, err) {
			t.Fatalf("IsCancellation(%v) = false, want true", err)
		}
	}
	if IsCancellation(done, errors.New("other")) {
		t.Fatal("unrelated error was recognized as cancellation")
	}
}

type stallingWriter struct{ calls int }

func (w *stallingWriter) Write(data []byte) (int, error) {
	w.calls++
	if w.calls == 1 {
		return len(data) / 2, nil
	}
	return 0, nil
}

type failingWriter struct{}

func (failingWriter) Write(data []byte) (int, error) { return 1, io.ErrClosedPipe }

func TestWriteAllReportsProgressAndFailures(t *testing.T) {
	var buffer bytes.Buffer
	if err := WriteAll(&buffer, []byte("frame")); err != nil || buffer.String() != "frame" {
		t.Fatalf("WriteAll = %v, %q; want complete write", err, buffer.String())
	}
	if err := WriteAll(&stallingWriter{}, []byte("frame")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("stalled write = %v, want io.ErrShortWrite", err)
	}
	if err := WriteAll(failingWriter{}, []byte("frame")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("failed write = %v, want io.ErrClosedPipe", err)
	}
}

func TestWaitUntilHonorsTargetAndCancellation(t *testing.T) {
	if err := WaitUntil(context.Background(), time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("past target = %v, want nil", err)
	}
	if err := WaitUntil(context.Background(), time.Now().Add(time.Millisecond)); err != nil {
		t.Fatalf("near target = %v, want nil", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WaitUntil(ctx, time.Now().Add(time.Hour)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait = %v, want context.Canceled", err)
	}
}

func TestCaptureBoundsRetainedBytes(t *testing.T) {
	capture := NewCapture(4)
	capture.Append([]byte("ab"))
	if capture.Truncated() {
		t.Fatal("capture truncated below its limit")
	}
	capture.Append([]byte("cdef"))
	capture.Append(nil)
	if got := string(capture.Bytes()); got != "abcd" || !capture.Truncated() {
		t.Fatalf("capture = %q truncated=%t, want abcd truncated", got, capture.Truncated())
	}
	full := NewCapture(2)
	full.Append([]byte("ab"))
	full.Append([]byte("c"))
	if !full.Truncated() {
		t.Fatal("append past a full capture was not recorded as truncation")
	}
	disabled := NewCapture(-1)
	disabled.Append([]byte("ignored"))
	if len(disabled.Bytes()) != 0 || disabled.Truncated() {
		t.Fatal("negative-limit capture retained bytes")
	}
}

func TestFormatCommandAndExitCode(t *testing.T) {
	if got := FormatCommand("agent", []string{"session", "a b"}); got != `agent "session" "a b"` {
		t.Fatalf("FormatCommand = %q", got)
	}
	if got := ExitCode(nil, nil); got != -1 {
		t.Fatalf("ExitCode without state = %d, want -1", got)
	}
	if got := ExitCode(nil, errors.New("not an exit error")); got != -1 {
		t.Fatalf("ExitCode with unrelated error = %d, want -1", got)
	}
}

func TestProcessControlWithoutStartedChild(t *testing.T) {
	if err := Terminate(nil); err != nil {
		t.Fatalf("Terminate(nil) = %v", err)
	}
	unstarted := exec.Command(os.Args[0])
	if err := Terminate(unstarted); err != nil {
		t.Fatalf("Terminate(unstarted) = %v", err)
	}
	if DescendantsAlive(nil, false) || DescendantsAlive(unstarted, false) {
		t.Fatal("an unstarted child reported live descendants")
	}
}

const helperEnv = "CHILDPROC_TEST_HELPER"

// TestHelperProcess is re-executed as a long-lived child by the process
// control tests; it is a no-op in the normal test run.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	time.Sleep(time.Minute)
	os.Exit(0)
}

func TestProcessControlReapsStartedChild(t *testing.T) {
	child := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	child.Env = append(os.Environ(), helperEnv+"=1")
	Prepare(child)
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	if !DescendantsAlive(child, false) {
		t.Fatal("an unreaped child did not report live descendants")
	}
	if err := Terminate(child); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	waitErr := child.Wait()
	if DescendantsAlive(child, true) {
		t.Fatal("a reaped and terminated child reported live descendants")
	}
	if got := ExitCode(child, waitErr); got == 0 {
		t.Fatalf("ExitCode after termination = %d, want non-zero", got)
	}
	if err := Terminate(child); err != nil {
		t.Fatalf("Terminate after exit = %v, want nil", err)
	}
}
