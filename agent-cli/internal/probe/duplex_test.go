package probe

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
		MaxDuration:      2 * time.Second,
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

	if result.ExitCode != 0 || result.ExitClassification != "normal" || !result.ChildWaited || result.WaitCount != 1 || result.DescendantsAlive {
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
		BinaryPath:                  binary,
		RecordDir:                   filepath.Join(t.TempDir(), "record"),
		Provider:                    "openai",
		Model:                       "duplex-test-model",
		APIKey:                      "sigint-secret",
		MaxDuration:                 time.Second,
		FrameDuration:               time.Millisecond,
		ShutdownGrace:               200 * time.Millisecond,
		Termination:                 TerminationSIGINT,
		TerminationAfterOutputBytes: 2,
		Segments: []DuplexAudioSegment{{
			ID: "active-speech", PCM16: duplexTestFrame(7), SilenceFor: 500 * time.Millisecond,
		}},
	})
	if err != nil {
		t.Fatalf("RunDuplexSession() error = %v; result = %+v", err, result)
	}
	if result.ExitClassification != "sigint" || !result.SignalSent || result.Signal != duplexSIGINTName {
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
	binary := buildDuplexTestChild(t)
	result, err := RunDuplexSession(context.Background(), DuplexSessionConfig{
		BinaryPath:     binary,
		RecordDir:      filepath.Join(t.TempDir(), "record"),
		Provider:       "openai",
		Model:          "duplex-test-model",
		MaxDuration:    time.Second,
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

func buildDuplexTestChild(t *testing.T) string {
	t.Helper()
	source := filepath.Join(t.TempDir(), "duplex-child.go")
	binary := filepath.Join(t.TempDir(), "duplex-child")
	const program = `package main

import (
	"io"
	"os"
	"time"
)

func main() {
	hold := false
	exitImmediately := false
	for _, arg := range os.Args[1:] {
		if arg == "--duplex-hold" {
			hold = true
		}
		if arg == "--duplex-exit-immediately" {
			exitImmediately = true
		}
	}
	if exitImmediately {
		return
	}
	frame := make([]byte, 960)
	frames := 0
	for {
		n, err := io.ReadFull(os.Stdin, frame)
		if n > 0 {
			frames++
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
`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatalf("write child source: %v", err)
	}
	command := exec.Command("go", "build", "-o", binary, source)
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build child: %v\n%s", err, output)
	}
	return binary
}

func buildDuplexSIGINTChild(t *testing.T) string {
	t.Helper()
	source := filepath.Join(t.TempDir(), "duplex-sigint-child.go")
	binary := filepath.Join(t.TempDir(), "duplex-sigint-child")
	const program = `package main

import (
	"io"
	"os"
	"os/signal"
)

func main() {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	frame := make([]byte, 960)
	if n, err := io.ReadFull(os.Stdin, frame); n > 0 {
		_, _ = os.Stdout.Write([]byte{0xa1, 0xb2})
		if err != nil {
			return
		}
	}
	<-interrupt
}
`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatalf("write SIGINT child source: %v", err)
	}
	command := exec.Command("go", "build", "-o", binary, source)
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build SIGINT child: %v\n%s", err, output)
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
