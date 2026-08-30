//go:build live

// This is the one bounded, billed confirmation for the zero-flag live voice
// path. It is excluded from ordinary and hermetic test runs; opt in with
// AGENT_HARNESS_LIVE_BARE_SESSION=1 and provide OPENAI_API_KEY.
package integration

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	bareSessionLiveOptIn          = "AGENT_HARNESS_LIVE_BARE_SESSION"
	bareSessionLiveModel          = "gpt-realtime-2.1-mini"
	bareSessionLiveListeningBound = 10 * time.Second
	bareSessionLiveShutdownBound  = 5 * time.Second
)

// TestLiveBareSessionDefaultDevicesStartsAndStops is intentionally a
// process-boundary probe. The child receives only the positional "session"
// argument; all live-session defaults must therefore come from the shipped
// command's bare resolver and the host's default device registry.
func TestLiveBareSessionDefaultDevicesStartsAndStops(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY is not set; skipping the billed bare-session live probe")
	}
	if os.Getenv(bareSessionLiveOptIn) != "1" {
		t.Skip(bareSessionLiveOptIn + "!=1; this live test bills provider and opens host audio devices")
	}

	home := t.TempDir()
	cmd := exec.Command(buildAgentBinary(t), "session")
	cmd.Dir = agentCLIRoot(t)
	cmd.Env = bareSessionLiveEnvironment(home, apiKey)
	if len(cmd.Args) != 2 || cmd.Args[1] != "session" {
		t.Fatalf("bare probe argv = %q, want exactly [binary session]", cmd.Args)
	}

	var stdout, stderr syncBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start bare live session: %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	started := time.Now()
	if err := waitForBareSessionListening(t, &stdout, &stderr, wait, bareSessionLiveListeningBound); err != nil {
		t.Fatalf("bare live session did not reach listening: %v\nstdout=%q\nstderr=%q", err, stdout.String(), stderr.String())
	}
	listeningElapsed := time.Since(started)
	combined := stdout.String() + stderr.String()
	assertBareSessionLiveReadyOutput(t, combined)

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("send one SIGINT to bare live session: %v", err)
	}
	select {
	case err := <-wait:
		if err != nil {
			t.Fatalf("bare live session after one SIGINT: %v\nstdout=%q\nstderr=%q", err, stdout.String(), stderr.String())
		}
	case <-time.After(bareSessionLiveShutdownBound):
		_ = cmd.Process.Kill()
		<-wait
		t.Fatalf("bare live session did not finish within %s after one SIGINT", bareSessionLiveShutdownBound)
	}

	combined = stdout.String() + stderr.String()
	assertBareSessionLiveTerminal(t, combined)
	t.Logf("bare live startup probe: argv=session provider=openai model=%s transport=ws input-device=%s output-device=%s session.created=observed readiness=listening elapsed-to-listening=%s sigint=one terminal=clean", bareSessionLiveModel, bareSessionLiveField(combined, "input-device"), bareSessionLiveField(combined, "output-device"), listeningElapsed.Round(time.Millisecond))
}

func bareSessionLiveEnvironment(home, apiKey string) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || name == "HOME" || name == "OPENAI_API_KEY" || name == "OPENAI_API_KEY_FILE" || strings.HasPrefix(name, "AGENT_") || strings.Contains(strings.ToUpper(name), "API_KEY") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "HOME="+home, "OPENAI_API_KEY="+apiKey)
	return env
}

func waitForBareSessionListening(t *testing.T, stdout, stderr *syncBuffer, wait <-chan error, bound time.Duration) error {
	t.Helper()
	deadline := time.NewTimer(bound)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if strings.Contains(stdout.String()+stderr.String(), "Listening:") {
			return nil
		}
		select {
		case err := <-wait:
			if err == nil {
				return errors.New("process exited before listening")
			}
			return err
		case <-deadline.C:
			return errors.New("listening deadline exceeded")
		case <-ticker.C:
		}
	}
}

func assertBareSessionLiveReadyOutput(t *testing.T, output string) {
	t.Helper()
	if strings.Count(output, "Starting bare live session:") != 1 || strings.Count(output, "Listening:") != 1 {
		t.Fatalf("bare live readiness output = %q, want one startup and one listening banner", output)
	}
	for _, want := range []string{
		"provider=openai",
		"model=" + bareSessionLiveModel,
		"transport=ws",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("bare live readiness output missing %q: %q", want, output)
		}
	}
	for _, field := range []string{"input-device", "output-device"} {
		value := bareSessionLiveField(output, field)
		if value == "" || value == "unavailable" {
			t.Fatalf("bare live readiness %s = %q, want a host default device identity: %q", field, value, output)
		}
	}
	if strings.Contains(output, "OPENAI_API_KEY") || strings.Contains(output, "sk-") {
		t.Fatalf("bare live readiness output contains credential-shaped data: %q", output)
	}
}

func assertBareSessionLiveTerminal(t *testing.T, output string) {
	t.Helper()
	if strings.Count(output, "[session terminal:") != 1 {
		t.Fatalf("bare live terminal count = %d, want one: %q", strings.Count(output, "[session terminal:"), output)
	}
	want := "classification=user_cancelled terminal_reason=cancellation terminal_provenance=cli output_state=none"
	if !strings.Contains(output, want) {
		t.Fatalf("bare live terminal = %q, want clean one-signal cancellation containing %q", output, want)
	}
}

func bareSessionLiveField(output, name string) string {
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "Listening:") {
			continue
		}
		for _, field := range strings.Fields(line) {
			prefix := name + "="
			if strings.HasPrefix(field, prefix) {
				return strings.TrimPrefix(field, prefix)
			}
		}
	}
	return ""
}
