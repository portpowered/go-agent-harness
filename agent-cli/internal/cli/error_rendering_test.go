package cli

import (
	"strings"
	"testing"
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
