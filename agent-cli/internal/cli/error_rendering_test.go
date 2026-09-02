package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestSessionAndProbeRunRenderFailuresOnce(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "session",
			args: []string{"session", "--transport", "quic"},
			want: `--transport must be one of "ws" or "webrtc", got "quic"`,
		},
		{
			name: "probe run",
			args: []string{"probe", "run", "missing-scenario.json", "--replay", "missing-fixture.session.json"},
			want: `replay fixture "missing-fixture.session.json" is missing or unreadable`,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			result := executeCLI(testCase.args...)
			if result.exitCode != 1 {
				t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", result.exitCode, result.stdout, result.stderr)
			}
			if result.stdout != "" {
				t.Fatalf("stdout = %q, want empty", result.stdout)
			}
			if got := strings.Count(result.stderr, "Error:"); got != 1 {
				t.Fatalf("customer-facing Error: count = %d, want 1; stderr=%q", got, result.stderr)
			}
			if got := strings.Count(result.stderr, testCase.want); got != 1 {
				t.Fatalf("failure text count = %d, want 1; stderr=%q", got, result.stderr)
			}
			if strings.Contains(result.stderr, "Usage:") {
				t.Fatalf("ordinary runtime failure unexpectedly included usage: %q", result.stderr)
			}
		})
	}
}

func TestSessionCLIRendersMissingCredentialOnceWithRemediation(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("AGENT_MODEL__OPENAI__API_KEY", "")
	result := executeCLI(
		"--config-dir", t.TempDir(),
		"session", "--prompt", "hello", "--provider", "openai",
	)

	if result.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", result.exitCode, result.stdout, result.stderr)
	}
	for _, want := range []string{"API key", "AGENT_MODEL__OPENAI__API_KEY"} {
		if got := strings.Count(result.stderr, want); got != 1 {
			t.Fatalf("missing-key stderr count for %q = %d, want 1; stderr=%q", want, got, result.stderr)
		}
	}
	if strings.Count(result.stderr, "Error:") != 1 || strings.Contains(result.stderr, "Usage:") {
		t.Fatalf("missing-key failure is not one concise CLI error: %q", result.stderr)
	}
}

func TestSessionCLIRendersNetworkFailureOnce(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	result := executeCLIWithSessionInferencer(
		&failingCLIInferencer{err: errors.New("dial tcp 127.0.0.1:1: connect: connection refused")},
		"--config-dir", t.TempDir(),
		"session", "--prompt", "hello", "--provider", "openai", "--api-key", "test-key",
	)

	if result.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", result.exitCode, result.stdout, result.stderr)
	}
	if strings.Count(result.stderr, "connection refused") != 1 || strings.Count(result.stderr, "Error:") != 1 {
		t.Fatalf("network failure was not rendered once: %q", result.stderr)
	}
	if strings.Contains(result.stderr, "Usage:") || strings.Contains(result.stderr, "test-key") {
		t.Fatalf("network failure included usage or a credential: %q", result.stderr)
	}
}

func TestSessionCLIReplayRendersCreditExhaustionAsRateLimited(t *testing.T) {
	path := writeCLIQuotaFailureCapture(t)
	result := executeCLI("session", "--replay", path)

	if result.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", result.exitCode, result.stdout, result.stderr)
	}
	for _, want := range []string{"classification=rate_limited", "no credits remaining"} {
		if got := strings.Count(result.stderr, want); got != 1 {
			t.Fatalf("quota stderr count for %q = %d, want 1; stderr=%q", want, got, result.stderr)
		}
	}
	if strings.Count(result.stderr, "Error:") != 1 || strings.Contains(result.stderr, "Usage:") {
		t.Fatalf("quota failure is not one concise CLI error: %q", result.stderr)
	}
}

type failingCLIInferencer struct {
	err error
}

func (i *failingCLIInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, i.err
}

func executeCLIWithSessionInferencer(inferencer messages.SessionInferencer, args ...string) cliExecution {
	root := newTestRootCommandWithProbeFleetCommand(NewProbeFleetCommand(), inferencer)
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	exitCode := 0
	if root.Execute() != nil {
		exitCode = 1
	}
	return cliExecution{exitCode: exitCode, stdout: stdout.String(), stderr: stderr.String()}
}

func writeCLIQuotaFailureCapture(t *testing.T) string {
	t.Helper()
	payloads := []struct {
		direction gatewaytesting.SessionEventDirection
		typeName  string
		payload   string
	}{
		{
			direction: gatewaytesting.DirectionClientToServer,
			typeName:  "session.update",
			payload:   `{"type":"session.update","session":{"type":"realtime","model":"gpt-realtime-2.1"}}`,
		},
		{
			direction: gatewaytesting.DirectionServerToClient,
			typeName:  "error",
			payload:   `{"type":"error","error":{"type":"insufficient_quota","code":"credit_balance_exhausted","message":"You have no credits remaining."}}`,
		},
	}
	records := make([]gatewaytesting.CapturedSessionEvent, 0, len(payloads))
	for index, payload := range payloads {
		records = append(records, gatewaytesting.CapturedSessionEvent{
			Sequence:    index + 1,
			Direction:   payload.direction,
			TimestampMs: int64(index),
			Type:        payload.typeName,
			PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
			Payload:     json.RawMessage(payload.payload),
		})
	}
	capture, err := gatewaytesting.SealSessionCapture(gatewaytesting.SessionCapture{
		Version:  gatewaytesting.SessionCaptureVersion,
		Provider: gatewaytesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime-2.1"},
		Session: gatewaytesting.SessionMetadata{
			ID:                "sess-quota-cli",
			StartedAtUTC:      time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
			FixtureProvenance: gatewaytesting.SessionFixtureProvenanceSyntheticFailure,
		},
		Records: records,
	})
	if err != nil {
		t.Fatalf("seal quota failure capture: %v", err)
	}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatalf("marshal quota failure capture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "quota.session.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write quota failure capture: %v", err)
	}
	return path
}
