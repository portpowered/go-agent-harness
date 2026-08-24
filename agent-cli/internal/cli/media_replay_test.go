package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

func replaySessionFixturePath(t *testing.T) string {
	t.Helper()
	return gatewaytesting.SharedSessionFixturePath("session_healthy_multiturn_audio.session.json")
}

func TestMediaProbeCommandReplayOptionCompletesObservationCycle(t *testing.T) {
	fixture := replaySessionFixturePath(t)
	command := NewMediaProbeCommandWithOptions(WithReplayFixture(fixture))
	var out bytes.Buffer
	if err := command.Run(context.Background(), &out, "go2rtc://unused-when-replaying"); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"Mode: replay",
		"Source: " + fixture,
		"Provider: grok",
		"Model: grok-4-healthy-multiturn",
		"Provenance: synthetic",
		"Inbound frames: 10",
		"Outbound ticks: 1",
		"Observation: 1 client_to_server session.update",
		"Observation: 2 server_to_client session.created",
		"Observation: 4 server_to_client response.audio.delta",
		"Observation: 11 server_to_client session.closed",
	}, "\n")
	for _, line := range strings.Split(want, "\n") {
		if !strings.Contains(out.String(), line+"\n") {
			t.Fatalf("replay report missing %q; report:\n%s", line, out.String())
		}
	}
}

func TestMediaProbeCommandReplayReportIsDeterministicAcrossRuns(t *testing.T) {
	fixture := replaySessionFixturePath(t)
	var first, second bytes.Buffer
	for _, out := range []*bytes.Buffer{&first, &second} {
		command := NewMediaProbeCommandWithOptions(WithReplayFixture(fixture))
		command.Timeout = time.Second
		if err := command.Run(context.Background(), out, "go2rtc://unused-when-replaying"); err != nil {
			t.Fatal(err)
		}
	}
	if first.String() != second.String() || first.Len() == 0 {
		t.Fatalf("replay reports diverged or empty:\n%s\n---\n%s", first.String(), second.String())
	}
}

func TestMediaProbeCommandReplayRejectsInvalidFixtureWithClearError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.session.json")
	invalid := `{"version":1,"provider":{"name":"grok","model":"m"},"records":[]}`
	if err := os.WriteFile(path, []byte(invalid), 0o644); err != nil {
		t.Fatal(err)
	}
	command := NewMediaProbeCommandWithOptions(WithReplayFixture(path))
	err := command.Run(context.Background(), &bytes.Buffer{}, "go2rtc://unused-when-replaying")
	if err == nil || !strings.Contains(err.Error(), "session fixture validation failed before any probe observation") {
		t.Fatalf("error = %v, want clear fixture validation failure", err)
	}
}

func TestMediaProbeCommandWithoutReplayOptionUsesLiveProbe(t *testing.T) {
	liveCalls := 0
	command := NewMediaProbeCommand(func(context.Context, string) (rtc.MediaCapabilities, error) {
		liveCalls++
		return rtc.MediaCapabilities{Source: "rtsp://camera:<redacted>@host/main", AudioCodec: "PCMU", SampleRate: 8000, Channels: 1}, nil
	})
	if command.ReplayFixture != "" {
		t.Fatalf("default ReplayFixture = %q, want empty (live default)", command.ReplayFixture)
	}
	var out bytes.Buffer
	if err := command.Run(context.Background(), &out, "rtsp://camera@host/main"); err != nil {
		t.Fatal(err)
	}
	if liveCalls != 1 || strings.Contains(out.String(), "Mode: replay") {
		t.Fatalf("live calls = %d, output = %q", liveCalls, out.String())
	}
}

func TestMediaProbeCLIReplayFlagProducesDeterministicReport(t *testing.T) {
	fixture := replaySessionFixturePath(t)
	var first, second bytes.Buffer
	for _, out := range []*bytes.Buffer{&first, &second} {
		command := NewMediaProbeCommand().Generate()
		command.SetOut(out)
		command.SetArgs([]string{"--replay-fixture", fixture, "go2rtc://unused-when-replaying"})
		if err := command.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if first.String() != second.String() || !strings.Contains(first.String(), "Mode: replay\n") || !strings.Contains(first.String(), "Inbound frames: 10\n") {
		t.Fatalf("CLI replay report not deterministic/complete:\n%s\n---\n%s", first.String(), second.String())
	}
}

func TestMediaProbeCLIDefaultInvocationUsesLivePath(t *testing.T) {
	command := NewMediaProbeCommand(func(context.Context, string) (rtc.MediaCapabilities, error) {
		return rtc.MediaCapabilities{Source: "stub-src", AudioCodec: "PCMU", SampleRate: 8000, Channels: 1}, nil
	}).Generate()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{"stub"})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Mode: replay") {
		t.Fatalf("default invocation used replay path: %q", out.String())
	}
}
