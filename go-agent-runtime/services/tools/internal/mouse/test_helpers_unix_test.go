//go:build linux || darwin

package mouse

import (
	"bytes"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Helpers for the command-based desktop platforms (linux, darwin).

func loadPNGasRGBA(path string) (*image.RGBA, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("open screenshot: %w", err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("decode screenshot: %w", err)
	}
	result := image.NewRGBA(decoded.Bounds())
	for y := decoded.Bounds().Min.Y; y < decoded.Bounds().Max.Y; y++ {
		for x := decoded.Bounds().Min.X; x < decoded.Bounds().Max.X; x++ {
			result.Set(x, y, decoded.At(x, y))
		}
	}
	return result, nil
}

// fakeMouseProcess is the in-process MouseProcess used by the platform tests.
// It records each helper invocation as its space-joined arguments.
type fakeMouseProcess struct {
	names []string
	calls []string
	run   func(args []string) ([]byte, error)
}

func (p *fakeMouseProcess) Run(name string, args ...string) ([]byte, error) {
	p.names = append(p.names, name)
	p.calls = append(p.calls, strings.Join(args, " "))
	if p.run != nil {
		return p.run(args)
	}
	return nil, nil
}

// recordedSleeps records the pacing sleeps requested by a driver.
type recordedSleeps []time.Duration

func (s *recordedSleeps) sleep(duration time.Duration) { *s = append(*s, duration) }

func (s recordedSleeps) total() time.Duration {
	var total time.Duration
	for _, duration := range s {
		total += duration
	}
	return total
}

func newFakeMouseDriver(process *fakeMouseProcess, sleeps *recordedSleeps) mouseDriver {
	return newMouseDriver(MouseToolOptions{Process: process, Sleep: sleeps.sleep})
}

func helperNotFound(name string) error {
	return &exec.Error{Name: name, Err: exec.ErrNotFound}
}

func assertMouseCalls(t *testing.T, process *fakeMouseProcess, command string, want []string) {
	t.Helper()
	for _, name := range process.names {
		if name != command {
			t.Fatalf("mouse helper ran %q, want %q", name, command)
		}
	}
	if got, wantText := strings.Join(process.calls, "\n"), strings.Join(want, "\n"); got != wantText {
		t.Fatalf("%s calls = %q, want %q", command, got, wantText)
	}
}

// writeFakeCommand writes an executable shell script named name into dir.
func writeFakeCommand(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}
