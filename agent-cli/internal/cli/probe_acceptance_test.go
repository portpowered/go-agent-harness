package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	acceptanceprobe "github.com/portpowered/go-agent-harness/agent-cli/internal/probe"
	loopprobe "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/spf13/cobra"
)

type acceptanceCommandRunnerFunc func(context.Context, loopprobe.AcceptanceInput) (loopprobe.AcceptanceVerdict, error)

func (f acceptanceCommandRunnerFunc) Run(ctx context.Context, input loopprobe.AcceptanceInput) (loopprobe.AcceptanceVerdict, error) {
	return f(ctx, input)
}

func newTestRootCommandWithAcceptance(runner AcceptanceProbeRunner, timeouts ...time.Duration) *cobra.Command {
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	loopFlags := flags.NewLoopFlags()
	chatFlags := flags.NewChatFlags()

	acceptanceCommand := NewProbeAcceptanceCommand(runner)
	if len(timeouts) > 0 {
		acceptanceCommand.Timeout = timeouts[0]
	}

	router := NewRouter(
		globalFlags,
		NewRootCommand(globalFlags),
		NewAskCommand(nil, askFlags, loopFlags, globalFlags),
		NewChatCommand(nil, askFlags, loopFlags, chatFlags, globalFlags),
		NewToolCommand(globalFlags),
		NewInteractionCommand(),
		NewInteractionReplayCommand(),
		NewProbeCommand(),
		NewProbeRunCommand(),
		NewProbeGateCommand(),
		NewProbeReportCommand(),
		NewSessionCommand(askFlags, globalFlags, nil, nil),
		NewSessionShowCommand(globalFlags),
		NewSessionListCommand(globalFlags),
		NewSessionDeleteCommand(globalFlags),
		NewConfigCommand(),
		NewConfigAddLocalCommand(globalFlags),
		acceptanceCommand,
	)
	return NewAgentCLI(router).Generate()
}

func TestProbeAcceptanceRunsThroughRouterWithActualArgv(t *testing.T) {
	var got loopprobe.AcceptanceInput
	runner := acceptanceCommandRunnerFunc(func(_ context.Context, input loopprobe.AcceptanceInput) (loopprobe.AcceptanceVerdict, error) {
		got = input
		return loopprobe.EvaluateAcceptance(
			input.Goal,
			loopprobe.AcceptanceAgentReport{SubjectiveRating: loopprobe.SubjectiveEasy, TerminalState: loopprobe.AcceptanceCompleted},
			loopprobe.ObjectiveEvidence{ArtifactPath: "result.txt", CheckedClaim: "done", Verified: true},
			loopprobe.AcceptanceTransportLive,
		), nil
	})
	root := newTestRootCommandWithAcceptance(runner)
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"probe", "acceptance", "probe-agent", "Create the result"})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got.BinaryPath != "probe-agent" || got.Goal != "Create the result" || got.WorkingDirectory != "" {
		t.Fatalf("runner input = %+v, want exactly binary and goal with deferred empty workdir", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var verdict loopprobe.AcceptanceVerdict
	if err := json.Unmarshal(stdout.Bytes(), &verdict); err != nil {
		t.Fatalf("decode verdict %q: %v", stdout.String(), err)
	}
	if !verdict.Pass || verdict.Goal != got.Goal || verdict.Name != "acceptance" {
		t.Fatalf("verdict = %+v, want machine-readable pass", verdict)
	}
}

func TestProbeAcceptanceFailurePrintsVerdictAndReturnsNonZero(t *testing.T) {
	runner := acceptanceCommandRunnerFunc(func(_ context.Context, input loopprobe.AcceptanceInput) (loopprobe.AcceptanceVerdict, error) {
		return loopprobe.EvaluateAcceptance(
			input.Goal,
			loopprobe.AcceptanceAgentReport{ClaimedSuccess: true, SubjectiveRating: loopprobe.SubjectiveEasy, TerminalState: loopprobe.AcceptanceCompleted},
			loopprobe.ObjectiveEvidence{ArtifactPath: "missing.txt", CheckedClaim: "done"},
			loopprobe.AcceptanceTransportReplay,
		), nil
	})
	root := newTestRootCommandWithAcceptance(runner)
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"probe", "acceptance", "probe-agent", "Create the result"})

	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("failure verdict returned nil error")
	}
	if stdout.Len() == 0 || !strings.Contains(stdout.String(), `"pass":false`) {
		t.Fatalf("stdout = %q, want failed verdict JSON", stdout.String())
	}
	if !strings.Contains(err.Error(), loopprobe.ErrObjectiveEvidenceAbsent.Error()) {
		t.Fatalf("error = %v, want objective-evidence reason", err)
	}
}

func TestProbeAcceptanceTimeoutReturnsStuckVerdict(t *testing.T) {
	runner := acceptanceCommandRunnerFunc(func(ctx context.Context, input loopprobe.AcceptanceInput) (loopprobe.AcceptanceVerdict, error) {
		<-ctx.Done()
		verdict := loopprobe.EvaluateAcceptance(
			input.Goal,
			loopprobe.AcceptanceAgentReport{SubjectiveRating: loopprobe.SubjectiveEasy, TerminalState: loopprobe.AcceptanceStuckPendingDownstream},
			loopprobe.ObjectiveEvidence{},
			loopprobe.AcceptanceTransportLive,
		)
		return verdict, &acceptanceprobe.ExecutionError{Kind: acceptanceprobe.ErrProbeAgentStuck, Cause: ctx.Err()}
	})
	root := newTestRootCommandWithAcceptance(runner, 20*time.Millisecond)
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetArgs([]string{"probe", "acceptance", "probe-agent", "wait forever"})

	started := time.Now()
	err := root.ExecuteContext(context.Background())
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timeout command took %s", elapsed)
	}
	if err == nil || !errors.Is(err, acceptanceprobe.ErrProbeAgentStuck) {
		t.Fatalf("error = %v, want stuck error", err)
	}
	var verdict loopprobe.AcceptanceVerdict
	if decodeErr := json.Unmarshal(stdout.Bytes(), &verdict); decodeErr != nil {
		t.Fatalf("decode verdict %q: %v", stdout.String(), decodeErr)
	}
	if verdict.Pass || verdict.TerminalState != loopprobe.AcceptanceStuckPendingDownstream || verdict.TerminalReason != "stuck" {
		t.Fatalf("verdict = %+v, want non-passing stuck verdict", verdict)
	}
}

func TestProbeAcceptanceLiveTimeoutStopsHangingBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep executable fixture is POSIX-specific")
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skipf("sleep executable is unavailable: %v", err)
	}

	runner := acceptanceprobe.NewLiveRunner(nil)
	runner.ArtifactRoot = t.TempDir()
	root := newTestRootCommandWithAcceptance(runner, 40*time.Millisecond)
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetArgs([]string{"probe", "acceptance", "sleep", "60"})

	started := time.Now()
	err := root.ExecuteContext(context.Background())
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("hanging binary took %s to stop", elapsed)
	}
	if err == nil || !errors.Is(err, acceptanceprobe.ErrProbeAgentStuck) {
		t.Fatalf("error = %v, want stuck error", err)
	}
	var verdict loopprobe.AcceptanceVerdict
	if decodeErr := json.Unmarshal(stdout.Bytes(), &verdict); decodeErr != nil {
		t.Fatalf("decode verdict %q: %v", stdout.String(), decodeErr)
	}
	if verdict.Pass || verdict.TerminalState != loopprobe.AcceptanceStuckPendingDownstream {
		t.Fatalf("verdict = %+v, want non-passing stuck verdict", verdict)
	}
}

func TestProbeAcceptanceCLIControlsUseRecordedArtifacts(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	for _, positive := range []bool{false, true} {
		name := "null agent"
		if positive {
			name = "positive agent"
		}
		t.Run(name, func(t *testing.T) {
			verifier := acceptanceprobe.RecordedArtifactVerifier{
				VerifyGoal: func(context.Context, loopprobe.AcceptanceInput, []byte) error { return nil },
			}
			runner := acceptanceprobe.NewRunner(acceptanceprobe.TransportFunc(func(_ context.Context, _ loopprobe.AcceptanceInput, artifacts acceptanceprobe.ArtifactSet) (acceptanceprobe.RunResult, error) {
				report := loopprobe.AcceptanceAgentReport{
					ClaimedSuccess:   true,
					SubjectiveRating: loopprobe.SubjectiveEasy,
				}
				if positive {
					if err := os.WriteFile(filepath.Join(artifacts.WorkingDirectory, "result.txt"), []byte("goal complete"), 0o600); err != nil {
						return acceptanceprobe.RunResult{}, err
					}
					report.ObjectiveArtifactPath = "result.txt"
					report.CheckedClaim = "goal complete"
				}
				return acceptanceprobe.RunResult{Report: report}, nil
			}), verifier)
			runner.ArtifactRoot = t.TempDir()
			root := newTestRootCommandWithAcceptance(runner)
			var stdout bytes.Buffer
			root.SetOut(&stdout)
			root.SetArgs([]string{"probe", "acceptance", binary, "complete the goal"})

			err := root.ExecuteContext(context.Background())
			var verdict loopprobe.AcceptanceVerdict
			if decodeErr := json.Unmarshal(stdout.Bytes(), &verdict); decodeErr != nil {
				t.Fatalf("decode verdict %q: %v", stdout.String(), decodeErr)
			}
			if positive {
				if err != nil || !verdict.Pass || !verdict.ObjectiveEvidence.Verified {
					t.Fatalf("positive control: err=%v verdict=%+v", err, verdict)
				}
				return
			}
			if err == nil || verdict.Pass || verdict.ObjectiveEvidence.Verified || !strings.Contains(verdict.Error, loopprobe.ErrObjectiveEvidenceAbsent.Error()) {
				t.Fatalf("null control: err=%v verdict=%+v", err, verdict)
			}
		})
	}
}

func TestProbeAcceptanceHelpDoesNotLeakFixtureOrInternalHints(t *testing.T) {
	root := newTestRootCommandWithAcceptance(acceptanceCommandRunnerFunc(func(context.Context, loopprobe.AcceptanceInput) (loopprobe.AcceptanceVerdict, error) {
		t.Fatal("help invoked the runner")
		return loopprobe.AcceptanceVerdict{}, nil
	}))
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"probe", "acceptance", "--help"})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("help error = %v", err)
	}
	help := stdout.String()
	if !strings.Contains(help, "plain-English goal") || !strings.Contains(help, "fresh empty working directory") {
		t.Fatalf("help = %q, want blind-input description", help)
	}
	for _, leaked := range []string{"--scenario", "--replay", "ObjectiveVerifier", "fixture"} {
		if strings.Contains(help, leaked) {
			t.Fatalf("help leaks internal hint %q: %q", leaked, help)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("help stderr = %q, want empty", stderr.String())
	}
}

func TestProbeAcceptanceCLIErrorIdentityTable(t *testing.T) {
	tests := []struct {
		name string
		run  AcceptanceProbeRunner
		want error
	}{
		{
			name: "probe crash",
			run: acceptanceCommandRunnerFunc(func(context.Context, loopprobe.AcceptanceInput) (loopprobe.AcceptanceVerdict, error) {
				return loopprobe.AcceptanceVerdict{}, &acceptanceprobe.ExecutionError{Kind: acceptanceprobe.ErrProbeAgentCrashed, Cause: errors.New("child exited unexpectedly")}
			}),
			want: acceptanceprobe.ErrProbeAgentCrashed,
		},
		{
			name: "unknown goal",
			run: acceptanceCommandRunnerFunc(func(context.Context, loopprobe.AcceptanceInput) (loopprobe.AcceptanceVerdict, error) {
				return loopprobe.AcceptanceVerdict{}, &acceptanceprobe.InputError{Field: "goal", Kind: acceptanceprobe.ErrUnknownGoal, Cause: errors.New("not in the configured goal catalog")}
			}),
			want: acceptanceprobe.ErrUnknownGoal,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newTestRootCommandWithAcceptance(tt.run)
			root.SetArgs([]string{"probe", "acceptance", "probe-agent", "goal"})
			err := root.ExecuteContext(context.Background())
			if err == nil || !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, tt.want)
			}
		})
	}
}

func TestDefaultProbeAcceptanceCLIReportsMissingBinary(t *testing.T) {
	root := newTestRootCommand()
	root.SetArgs([]string{"probe", "acceptance", "/path/that/does/not/exist", "run the goal"})
	err := root.ExecuteContext(context.Background())
	if err == nil || !errors.Is(err, acceptanceprobe.ErrBinaryMissing) {
		t.Fatalf("error = %v, want missing-binary identity", err)
	}
	if !strings.Contains(err.Error(), "binary") {
		t.Fatalf("error = %v, want actionable binary context", err)
	}
}
