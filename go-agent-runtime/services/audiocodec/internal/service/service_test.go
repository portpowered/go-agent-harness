package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiocodec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

type fakeRunner struct {
	result runResult
	err    error
	path   string
	input  []byte
	calls  int
}

func (f *fakeRunner) run(_ context.Context, inputPath string, _ audiocodec.Limits) (runResult, error) {
	f.calls++
	f.path = inputPath
	content, err := os.ReadFile(inputPath)
	if err != nil {
		return runResult{}, err
	}
	f.input = content
	return f.result, f.err
}

func TestConvertUsesExplicitFormatAndCleansInputWithoutAliasing(t *testing.T) {
	input := []byte("RIFF-source")
	original := append([]byte(nil), input...)
	output := []byte{0, 0, 1, 0}
	runner := &fakeRunner{result: runResult{stdout: output}}
	service := newWithRunner(runner)
	limits := defaultLimits()
	got, err := service.Convert(context.Background(), audiocodec.Request{Input: input, FormatHint: "audio/wav", Limits: limits})
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	if !bytes.Equal(input, original) {
		t.Fatalf("Convert mutated input: got %x, want %x", input, original)
	}
	if !bytes.Equal(got.PCM16, output) || got.SampleRate != audiocodec.PCM16SampleRate || got.Channels != audiocodec.PCM16Channels || got.Encoding != audiocodec.PCM16Encoding || got.InputFormat != audiocodec.FormatWAV {
		t.Fatalf("Convert() result = %#v", got)
	}
	got.PCM16[0] = 9
	if output[0] != 0 {
		t.Fatal("Convert returned runner-owned output")
	}
	if _, err := os.Stat(runner.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary input stat error = %v, want os.ErrNotExist", err)
	}
}

func TestConvertDetectsSupportedHeaders(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		format audiocodec.InputFormat
	}{
		{name: "wav", input: []byte("RIFF----WAVE"), format: audiocodec.FormatWAV},
		{name: "flac", input: []byte("fLaC"), format: audiocodec.FormatFLAC},
		{name: "ogg", input: []byte("OggS"), format: audiocodec.FormatOGG},
		{name: "opus", input: []byte("OggS----OpusHead"), format: audiocodec.FormatOpus},
		{name: "m4a", input: []byte("....ftyp"), format: audiocodec.FormatM4A},
		{name: "webm", input: []byte{0x1a, 0x45, 0xdf, 0xa3}, format: audiocodec.FormatWebM},
		{name: "mp3", input: []byte("ID3"), format: audiocodec.FormatMP3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{result: runResult{stdout: []byte{0, 0}}}
			result, err := newWithRunner(runner).Convert(context.Background(), audiocodec.Request{Input: test.input, Limits: defaultLimits()})
			if err != nil {
				t.Fatalf("Convert() error = %v", err)
			}
			if result.InputFormat != test.format {
				t.Fatalf("InputFormat = %q, want %q", result.InputFormat, test.format)
			}
		})
	}
}

func TestConvertRejectsUnsupportedAndInputOverflowBeforeProcess(t *testing.T) {
	runner := &fakeRunner{result: runResult{stdout: []byte{0, 0}}}
	limits := defaultLimits()
	limits.MaxInputBytes = 3
	if _, err := newWithRunner(runner).Convert(context.Background(), audiocodec.Request{Input: []byte("RIFF"), Limits: limits}); !errors.Is(err, audiocodec.ErrInputTooLarge) {
		t.Fatalf("input overflow error = %v", err)
	}
	if runner.calls != 0 {
		t.Fatalf("runner calls after input overflow = %d, want 0", runner.calls)
	}
	limits.MaxInputBytes = 16
	if _, err := newWithRunner(runner).Convert(context.Background(), audiocodec.Request{Input: []byte("unknown"), Limits: limits}); !errors.Is(err, audiocodec.ErrUnsupportedFormat) {
		t.Fatalf("unsupported format error = %v", err)
	}
	if runner.calls != 0 {
		t.Fatalf("runner calls after format rejection = %d, want 0", runner.calls)
	}
	limits.MaxInputBytes = 16
	if _, err := newWithRunner(runner).Convert(context.Background(), audiocodec.Request{Input: []byte("RIFF"), FormatHint: "application/octet-stream", Limits: limits}); !errors.Is(err, audiocodec.ErrUnsupportedFormat) {
		t.Fatalf("unsupported hint error = %v", err)
	}
}

func TestConvertRejectsInvalidRequestsBeforeResources(t *testing.T) {
	runner := &fakeRunner{result: runResult{stdout: []byte{0, 0}}}
	var nilContext context.Context
	if _, err := newWithRunner(runner).Convert(nilContext, audiocodec.Request{Limits: defaultLimits()}); !errors.Is(err, audiocodec.ErrInvalidRequest) {
		t.Fatalf("nil context error = %v", err)
	}
	var nilService *Service
	if _, err := nilService.Convert(context.Background(), audiocodec.Request{Limits: defaultLimits()}); !errors.Is(err, audiocodec.ErrInvalidRequest) {
		t.Fatalf("nil service error = %v", err)
	}
	limits := defaultLimits()
	limits.MaxStderrBytes = 0
	if _, err := newWithRunner(runner).Convert(context.Background(), audiocodec.Request{Limits: limits}); !errors.Is(err, audiocodec.ErrInvalidRequest) {
		t.Fatalf("invalid request limits error = %v", err)
	}
}

func TestConvertRejectsOddAndOverlargeOutputWithCanonicalErrors(t *testing.T) {
	limits := defaultLimits()
	limits.MaxOutputBytes = 2
	odd := &fakeRunner{result: runResult{stdout: []byte{1}}}
	if _, err := newWithRunner(odd).Convert(context.Background(), audiocodec.Request{Input: []byte("RIFF"), Limits: limits}); !errors.Is(err, audiocodec.ErrInvalidPCM16) || !errors.Is(err, codec.ErrPCM16OddLength) {
		t.Fatalf("odd output error = %v", err)
	}
	large := &fakeRunner{result: runResult{stdout: []byte{0, 0, 1, 0}}}
	if _, err := newWithRunner(large).Convert(context.Background(), audiocodec.Request{Input: []byte("RIFF"), Limits: limits}); !errors.Is(err, audiocodec.ErrOutputTooLarge) || !errors.Is(err, codec.ErrPayloadTooLarge) {
		t.Fatalf("large output error = %v", err)
	}
}

func TestConvertPreservesCancellationIdentity(t *testing.T) {
	runner := &blockingRunner{started: make(chan struct{})}
	limits := defaultLimits()
	limits.MaxDuration = 20 * time.Millisecond
	_, err := newWithRunner(runner).Convert(context.Background(), audiocodec.Request{Input: []byte("RIFF"), Limits: limits})
	if !errors.Is(err, audiocodec.ErrCanceled) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestConvertPreservesTypedRunnerErrorsAndMapsOtherFailures(t *testing.T) {
	typed := &audiocodec.Error{Kind: audiocodec.ErrorDecode, Detail: "typed"}
	if _, err := newWithRunner(&fakeRunner{err: typed}).Convert(context.Background(), audiocodec.Request{Input: []byte("RIFF"), Limits: defaultLimits()}); !errors.Is(err, audiocodec.ErrDecode) {
		t.Fatalf("typed runner error = %v", err)
	}
	if _, err := newWithRunner(&fakeRunner{err: errors.New("runner failed")}).Convert(context.Background(), audiocodec.Request{Input: []byte("RIFF"), Limits: defaultLimits()}); !errors.Is(err, audiocodec.ErrProcessWait) {
		t.Fatalf("untyped runner error = %v", err)
	}
}

type blockingRunner struct {
	started chan struct{}
}

func (r *blockingRunner) run(ctx context.Context, _ string, _ audiocodec.Limits) (runResult, error) {
	close(r.started)
	<-ctx.Done()
	return runResult{}, ctx.Err()
}

func TestProcessRunnerClassifiesLookupStartWaitDecodeAndBounds(t *testing.T) {
	input := writeRunnerInput(t)
	t.Run("lookup", func(t *testing.T) {
		runner := newProcessRunner("missing")
		runner.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
		if _, err := runner.run(context.Background(), input, defaultLimits()); !errors.Is(err, audiocodec.ErrExecutableLookup) || !errors.Is(err, exec.ErrNotFound) {
			t.Fatalf("lookup error = %v", err)
		}
	})
	t.Run("canceled before lookup", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		runner := newProcessRunner("fake")
		if _, err := runner.run(ctx, input, defaultLimits()); !errors.Is(err, audiocodec.ErrCanceled) || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled process error = %v", err)
		}
	})
	t.Run("input", func(t *testing.T) {
		runner := newProcessRunner("fake")
		runner.lookPath = func(string) (string, error) { return "fake", nil }
		runner.command = func(context.Context, string, ...string) command { return &testCommand{} }
		if _, err := runner.run(context.Background(), t.TempDir()+"/missing", defaultLimits()); !errors.Is(err, audiocodec.ErrInputFile) {
			t.Fatalf("input error = %v", err)
		}
	})
	t.Run("start", func(t *testing.T) {
		runner := testProcessRunner(&testCommand{startErr: errors.New("start")})
		if _, err := runner.run(context.Background(), input, defaultLimits()); !errors.Is(err, audiocodec.ErrProcessStart) {
			t.Fatalf("start error = %v", err)
		}
	})
	t.Run("wait", func(t *testing.T) {
		runner := testProcessRunner(&testCommand{waitErr: errors.New("wait")})
		if _, err := runner.run(context.Background(), input, defaultLimits()); !errors.Is(err, audiocodec.ErrProcessWait) {
			t.Fatalf("wait error = %v", err)
		}
	})
	t.Run("decode", func(t *testing.T) {
		runner := testProcessRunner(&testCommand{waitErr: commandExitError(t)})
		if _, err := runner.run(context.Background(), input, defaultLimits()); !errors.Is(err, audiocodec.ErrDecode) {
			t.Fatalf("decode error = %v", err)
		}
	})
	t.Run("stdout bound", func(t *testing.T) {
		limits := defaultLimits()
		limits.MaxOutputBytes = 2
		runner := testProcessRunner(&testCommand{stdoutData: []byte{0, 0, 1, 0}})
		if _, err := runner.run(context.Background(), input, limits); !errors.Is(err, audiocodec.ErrOutputTooLarge) {
			t.Fatalf("stdout bound error = %v", err)
		}
	})
	t.Run("stderr bound", func(t *testing.T) {
		limits := defaultLimits()
		limits.MaxStderrBytes = 2
		runner := testProcessRunner(&testCommand{stderrData: []byte("too much")})
		if _, err := runner.run(context.Background(), input, limits); !errors.Is(err, audiocodec.ErrStderrTooLarge) {
			t.Fatalf("stderr bound error = %v", err)
		}
	})
}

func TestBoundedBufferRejectsWritesAfterLimit(t *testing.T) {
	buffer := newBoundedBuffer(2)
	if n, err := buffer.Write([]byte{1, 2}); err != nil || n != 2 {
		t.Fatalf("initial bounded write = %d, %v", n, err)
	}
	if n, err := buffer.Write([]byte{3}); !errors.Is(err, io.ErrShortBuffer) || n != 0 || !buffer.exceeded {
		t.Fatalf("overflow bounded write = %d, %v, exceeded=%v", n, err, buffer.exceeded)
	}
}

func TestRemoveTempFileHandlesExistingAndMissingPaths(t *testing.T) {
	path := t.TempDir() + "/input"
	if err := os.WriteFile(path, []byte("input"), 0o600); err != nil {
		t.Fatal(err)
	}
	removeTempFile(path)
	removeTempFile(path)
}

func commandExitError(t *testing.T) error {
	t.Helper()
	return exec.Command("false").Run()
}

func writeRunnerInput(t *testing.T) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "runner-input-")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("RIFF")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return file.Name()
}

func testProcessRunner(process *testCommand) *processRunner {
	runner := newProcessRunner("fake")
	runner.lookPath = func(string) (string, error) { return "fake", nil }
	runner.command = func(context.Context, string, ...string) command { return process }
	return runner
}

type testCommand struct {
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	startErr   error
	waitErr    error
	stdoutData []byte
	stderrData []byte
}

func (c *testCommand) setStdin(reader io.Reader)  { c.stdin = reader }
func (c *testCommand) setStdout(writer io.Writer) { c.stdout = writer }
func (c *testCommand) setStderr(writer io.Writer) { c.stderr = writer }
func (c *testCommand) start() error               { return c.startErr }
func (c *testCommand) wait() error {
	if len(c.stdoutData) != 0 {
		_, _ = c.stdout.Write(c.stdoutData)
	}
	if len(c.stderrData) != 0 {
		_, _ = c.stderr.Write(c.stderrData)
	}
	return c.waitErr
}

func TestProcessRunnerProducesCanonicalPCMFromWAV(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	var wav bytes.Buffer
	samples := []int16{-32768, -1, 0, 1, 32767}
	if err := wavio.Write(&wav, audiocodec.PCM16SampleRate, samples); err != nil {
		t.Fatal(err)
	}
	service := New()
	got, err := service.Convert(context.Background(), audiocodec.Request{Input: wav.Bytes(), Limits: defaultLimits()})
	if err != nil {
		t.Fatalf("Convert(WAV) error = %v", err)
	}
	if err := codec.ValidatePCM16(got.PCM16, audiocodec.DefaultMaxOutputBytes); err != nil {
		t.Fatalf("ValidatePCM16(output) = %v", err)
	}
	if len(got.PCM16) == 0 {
		t.Fatal("Convert(WAV) returned empty PCM")
	}
	decoded, err := codec.DecodePCM16(got.PCM16)
	if err != nil || len(decoded) != len(samples) {
		t.Fatalf("decoded output = %d samples, %v; want %d", len(decoded), err, len(samples))
	}
	for index, sample := range samples {
		if decoded[index] != sample {
			t.Fatalf("sample %d = %d, want %d", index, decoded[index], sample)
		}
	}
}

func TestWAVFixtureHasCanonicalPCM16Header(t *testing.T) {
	var wav bytes.Buffer
	if err := wavio.Write(&wav, audiocodec.PCM16SampleRate, []int16{1}); err != nil {
		t.Fatal(err)
	}
	if string(wav.Bytes()[:4]) != "RIFF" || string(wav.Bytes()[8:12]) != "WAVE" {
		t.Fatalf("WAV header = %x", wav.Bytes()[:12])
	}
	if binary.LittleEndian.Uint16(wav.Bytes()[34:36]) != 16 {
		t.Fatalf("WAV bits per sample = %d, want 16", binary.LittleEndian.Uint16(wav.Bytes()[34:36]))
	}
}
