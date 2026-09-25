package probe

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// duplexProcessBound is the hard upper bound for a duplex child that must
// finish on its own. Passing runs end as soon as the child exits; the bound
// only has to absorb the first exec of a freshly linked binary, which takes
// several seconds on a loaded macOS workstation.
const duplexProcessBound = 30 * time.Second

func TestDuplexRunnerStreamsFramesAndSanitizesCredentials(t *testing.T) {
	binary := buildDuplexTestChild(t)
	runDir := filepath.Join(t.TempDir(), "record")
	workDir := filepath.Join(t.TempDir(), "work")
	configDir := filepath.Join(t.TempDir(), "config")
	var output bytes.Buffer
	const secret = "sk-duplex-secret"

	result, err := RunDuplexSession(context.Background(), DuplexSessionConfig{
		BinaryPath:       binary,
		RecordDir:        runDir,
		WorkingDirectory: workDir,
		ConfigDir:        configDir,
		Provider:         "openai",
		Model:            "duplex-test-model",
		APIKey:           secret,
		MaxDuration:      duplexProcessBound,
		FrameDuration:    time.Millisecond,
		AdditionalArgs:   []string{"--wait-for-close"},
		Output:           &output,
		Segments: []DuplexAudioSegment{
			{ID: "first-speech", PCM16: duplexTestFrame(1)},
			{ID: "silence", SilenceFor: time.Millisecond, WaitForOutputBytes: 2},
			{ID: "second-speech", PCM16: duplexTestFrame(2), WaitForOutputBytes: 4},
		},
	})
	if err != nil {
		t.Fatalf("RunDuplexSession() error = %v", err)
	}

	if result.ExitCode != 0 || result.ExitClassification != duplexExitNormal || !result.ChildWaited || result.WaitCount != 1 || result.DescendantsAlive {
		t.Fatalf("process result = %+v, want a waited zero-exit child", result)
	}
	if !result.InputClosed || !result.InputFinished || !result.StdoutClosed || !result.StderrClosed {
		t.Fatalf("pipe result = %+v, want all boundaries closed and input complete", result)
	}
	if len(result.Input) != 3 {
		t.Fatalf("input events = %d, want one event per streamed frame", len(result.Input))
	}
	if got := []string{result.Input[0].SegmentID, result.Input[1].SegmentID, result.Input[2].SegmentID}; !equalStrings(got, []string{"first-speech", "silence", "second-speech"}) {
		t.Fatalf("input segment order = %v", got)
	}
	if len(result.Output) == 0 || result.Output[len(result.Output)-1].Total < 6 {
		t.Fatalf("output events = %+v, want at least three frame markers drained", result.Output)
	}
	if !bytes.Equal(output.Bytes(), result.Stdout) || len(result.Stdout) != 6 {
		t.Fatalf("stdout sink/capture = %x/%x, want six drained bytes", output.Bytes(), result.Stdout)
	}
	preThirdOutput := false
	for _, event := range result.Output {
		if event.Total >= 4 && event.At <= result.Input[2].At {
			preThirdOutput = true
			break
		}
	}
	if !preThirdOutput {
		t.Fatalf("output was not drained before gated third frame: output=%+v input=%+v", result.Output, result.Input)
	}
	if strings.Contains(result.Command, secret) || strings.Contains(strings.Join(result.SanitizedArgs, "\x00"), secret) {
		t.Fatalf("credential leaked into recorded command evidence: command=%q args=%q", result.Command, result.SanitizedArgs)
	}
	for _, want := range []string{"--audio-in", "--audio-out", "--record-dir", "--provider", "--model", "--max-duration"} {
		if !containsString(result.SanitizedArgs, want) {
			t.Fatalf("sanitized args = %v, missing %q", result.SanitizedArgs, want)
		}
	}
	if result.Input[0].SHA256 == "" || result.Input[0].Silent {
		t.Fatalf("first input evidence = %+v, want non-silent hashed PCM", result.Input[0])
	}
	if !result.Input[1].Silent {
		t.Fatalf("silence input evidence = %+v, want silent frame", result.Input[1])
	}
}

func TestDuplexRunnerSendsSIGINTAtOutputBoundary(t *testing.T) {
	binary := buildDuplexSIGINTChild(t)
	result, err := RunDuplexSession(context.Background(), DuplexSessionConfig{
		BinaryPath: binary,
		RecordDir:  filepath.Join(t.TempDir(), "record"),
		Provider:   "openai",
		Model:      "duplex-test-model",
		APIKey:     "sigint-secret",
		// Keep this budget generous enough for a busy full-suite scheduler while
		// retaining a hard upper bound for a child that fails to start or exit.
		MaxDuration:                 duplexProcessBound,
		FrameDuration:               2 * time.Millisecond,
		ShutdownGrace:               time.Second,
		Termination:                 TerminationSIGINT,
		TerminationAfterOutputBytes: 2,
		Segments: []DuplexAudioSegment{{
			ID: "active-speech", PCM16: duplexTestFrame(7), SilenceFor: 500 * time.Millisecond,
		}},
	})
	if err != nil {
		t.Fatalf("RunDuplexSession() error = %v; result = %+v", err, result)
	}
	if result.ExitClassification != duplexExitSIGINT || !result.SignalSent || result.Signal != duplexSIGINTName {
		t.Fatalf("SIGINT result = %+v, want recorded SIGINT classification", result)
	}
	if result.SignalAt <= 0 || result.SignalAt > result.Duration {
		t.Fatalf("SIGINT timing = signal_at:%s duration:%s, want signal during run", result.SignalAt, result.Duration)
	}
	if !result.ChildWaited || result.WaitCount != 1 || result.DescendantsAlive {
		t.Fatalf("SIGINT process lifecycle = %+v, want exactly one reap and no descendants", result)
	}
	if !result.InputClosed || result.InputFinished || !result.StdoutClosed || !result.StderrClosed {
		t.Fatalf("SIGINT pipe lifecycle = %+v, want closed input/output with interrupted input", result)
	}
}

func TestDuplexRunnerKillsChildAtDeadline(t *testing.T) {
	binary := buildDuplexTestChild(t)
	result, err := RunDuplexSession(context.Background(), DuplexSessionConfig{
		BinaryPath:     binary,
		RecordDir:      filepath.Join(t.TempDir(), "record"),
		Provider:       "openai",
		Model:          "duplex-test-model",
		APIKey:         "deadline-secret",
		MaxDuration:    50 * time.Millisecond,
		FrameDuration:  time.Millisecond,
		ShutdownGrace:  100 * time.Millisecond,
		AdditionalArgs: []string{"--duplex-hold"},
		Segments:       []DuplexAudioSegment{{PCM16: duplexTestFrame(1)}},
	})
	if !errors.Is(err, ErrDuplexDeadline) {
		t.Fatalf("RunDuplexSession() error = %v, want deadline error", err)
	}
	if !result.TimedOut || !result.ChildWaited || !result.InputClosed {
		t.Fatalf("deadline result = %+v, want timed out waited child with closed stdin", result)
	}
	if result.Duration >= time.Second {
		t.Fatalf("deadline run took %s, want bounded shutdown", result.Duration)
	}
}

func TestDuplexRunnerRejectsPrematureChildExit(t *testing.T) {
	// Test incomplete input after a normal child exit, independently of the
	// deadline test above. Allow process startup under concurrent coverage load.
	binary := buildDuplexTestChild(t)
	result, err := RunDuplexSession(context.Background(), DuplexSessionConfig{
		BinaryPath:     binary,
		RecordDir:      filepath.Join(t.TempDir(), "record"),
		Provider:       "openai",
		Model:          "duplex-test-model",
		MaxDuration:    duplexProcessBound,
		FrameDuration:  time.Millisecond,
		AdditionalArgs: []string{"--duplex-exit-immediately"},
		Segments:       []DuplexAudioSegment{{PCM16: make([]byte, 1<<20)}},
	})
	if !errors.Is(err, ErrDuplexInputIncomplete) {
		t.Fatalf("RunDuplexSession() error = %v, want premature-exit error; result = %+v", err, result)
	}
	if result.ExitCode != 0 || result.InputFinished || !result.ChildWaited {
		t.Fatalf("premature-exit result = %+v, want zero-exit child with incomplete input", result)
	}
}

func TestDuplexRunnerRejectsUnsafeOrInvalidConfiguration(t *testing.T) {
	binary := buildDuplexTestChild(t)
	base := DuplexSessionConfig{
		BinaryPath:  binary,
		RecordDir:   filepath.Join(t.TempDir(), "record"),
		Provider:    "openai",
		Model:       "duplex-test-model",
		MaxDuration: time.Second,
		Segments:    []DuplexAudioSegment{{PCM16: duplexTestFrame(1)}},
	}
	for _, test := range []struct {
		name string
		edit func(*DuplexSessionConfig)
		want error
	}{
		{name: "non standard sample rate", edit: func(config *DuplexSessionConfig) { config.SampleRate = 8000 }, want: ErrDuplexConfigInvalid},
		{name: "owned boundary flag", edit: func(config *DuplexSessionConfig) { config.AdditionalArgs = []string{"--audio-in", "other.raw"} }, want: ErrDuplexConfigInvalid},
		{name: "credential flag", edit: func(config *DuplexSessionConfig) { config.AdditionalArgs = []string{"--api-key=leaked"} }, want: ErrDuplexConfigInvalid},
		{name: "odd PCM16", edit: func(config *DuplexSessionConfig) { config.Segments = []DuplexAudioSegment{{PCM16: []byte{1}}} }, want: ErrDuplexInputInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := base
			test.edit(&config)
			_, err := NewDuplexRunner().Run(context.Background(), config)
			if !errors.Is(err, test.want) {
				t.Fatalf("Run() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestSanitizeDuplexArgsRedactsFlagValuesAndSecrets(t *testing.T) {
	args := []string{"session", "--api-key", "flag-secret", "--token=inline-secret", "literal-secret", "--model", "test"}
	got := SanitizeDuplexArgs(args, "literal-secret")
	want := []string{"session", "--api-key", "<redacted>", "--token=<redacted>", "<redacted>", "--model", "test"}
	if !equalStrings(got, want) {
		t.Fatalf("SanitizeDuplexArgs() = %v, want %v", got, want)
	}
}

// The duplex children are this test binary re-executed under a linked name:
// init dispatches on that name before the testing flags are parsed, so no
// child program is compiled or linked per test.
const (
	duplexChildName       = "duplex-child"
	duplexSIGINTChildName = "duplex-sigint-child"
)

func init() {
	switch strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") {
	case duplexChildName:
		runDuplexTestChild(os.Args[1:])
		os.Exit(0)
	case duplexSIGINTChildName:
		runDuplexSIGINTChild()
		os.Exit(0)
	}
}

// runDuplexTestChild echoes a two-byte marker per 960-byte stdin frame until
// stdin closes; --duplex-hold blocks after the first frame and
// --duplex-exit-immediately exits before reading.
func runDuplexTestChild(args []string) {
	hold := false
	for _, arg := range args {
		switch arg {
		case "--duplex-hold":
			hold = true
		case "--duplex-exit-immediately":
			return
		}
	}
	frame := make([]byte, 960)
	for {
		n, err := io.ReadFull(os.Stdin, frame)
		if n > 0 {
			_, _ = os.Stdout.Write([]byte{0xa1, 0xb2})
			_ = os.Stdout.Sync()
		}
		if hold && n > 0 {
			time.Sleep(time.Hour)
			return
		}
		if err != nil {
			return
		}
	}
}

// runDuplexSIGINTChild echoes one marker for its first frame and exits on
// SIGINT.
func runDuplexSIGINTChild() {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	frame := make([]byte, 960)
	if n, err := io.ReadFull(os.Stdin, frame); n > 0 {
		_, _ = os.Stdout.Write([]byte{0xa1, 0xb2})
		_ = os.Stdout.Sync()
		if err != nil {
			return
		}
	}
	<-interrupt
}

func buildDuplexTestChild(t *testing.T) string {
	t.Helper()
	return linkDuplexChild(t, duplexChildName)
}

func buildDuplexSIGINTChild(t *testing.T) string {
	t.Helper()
	return linkDuplexChild(t, duplexSIGINTChildName)
}

// linkDuplexChild links the running test binary under name: a hard link, or
// a symbolic link when the temporary directory is on another device.
func linkDuplexChild(t *testing.T, name string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	binary := filepath.Join(t.TempDir(), name)
	if linkErr := os.Link(executable, binary); linkErr != nil {
		if err := os.Symlink(executable, binary); err != nil {
			t.Fatalf("link duplex child: %v; symlink: %v", linkErr, err)
		}
	}
	return binary
}

func duplexTestFrame(seed byte) []byte {
	frame := make([]byte, DefaultDuplexFrameSamples*2)
	for index := range frame {
		frame[index] = seed
	}
	return frame
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestDuplexProgressWaitForOutputSequenceMatchesAcrossReads(t *testing.T) {
	state := newDuplexProgressState()
	progress := &DuplexProgress{state: state}
	want := []byte{1, 0x42, 0x52, 0x42}
	done := make(chan error, 1)
	go func() {
		done <- progress.WaitForOutputSequence(context.Background(), want)
	}()

	state.noteOutput(DuplexOutputEvent{Bytes: 3}, []byte{9, 1, 0x42})
	select {
	case err := <-done:
		t.Fatalf("partial sequence returned early: %v", err)
	default:
	}
	state.noteOutput(DuplexOutputEvent{Bytes: 2}, []byte{0x52, 0x42})
	if err := <-done; err != nil {
		t.Fatalf("WaitForOutputSequence: %v", err)
	}
}

func TestDuplexProgressWaitForOutputSequenceMatchesRecentOutput(t *testing.T) {
	state := newDuplexProgressState()
	state.noteOutput(DuplexOutputEvent{Bytes: 6}, []byte("prefix"))
	state.noteOutput(DuplexOutputEvent{Bytes: 6}, []byte("marker"))

	err := (&DuplexProgress{state: state}).WaitForOutputSequence(context.Background(), []byte("marker"))
	if err != nil {
		t.Fatalf("WaitForOutputSequence: %v", err)
	}
}

func TestDuplexProgressWaitForOutputSequenceHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (&DuplexProgress{state: newDuplexProgressState()}).WaitForOutputSequence(ctx, []byte("missing"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitForOutputSequence error = %v, want context.Canceled", err)
	}
}

func TestDuplexProgressWaitForOutputSequenceReportsClosedOutput(t *testing.T) {
	state := newDuplexProgressState()
	state.noteOutputClosed()
	err := (&DuplexProgress{state: state}).WaitForOutputSequence(context.Background(), []byte("missing"))
	if !errors.Is(err, ErrDuplexPipe) {
		t.Fatalf("WaitForOutputSequence error = %v, want ErrDuplexPipe", err)
	}
}

func TestDuplexProgressWaitForOutputSequenceRejectsOversizedSequence(t *testing.T) {
	err := (&DuplexProgress{state: newDuplexProgressState()}).WaitForOutputSequence(
		context.Background(),
		make([]byte, duplexProgressOutputWindow+1),
	)
	if !errors.Is(err, ErrDuplexConfigInvalid) {
		t.Fatalf("WaitForOutputSequence error = %v, want ErrDuplexConfigInvalid", err)
	}
}
