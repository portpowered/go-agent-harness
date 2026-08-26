package probe

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	loopprobe "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

func TestRunnerProvidesBlindInputAndRecordsArtifacts(t *testing.T) {
	binary := acceptanceTestBinary(t)
	artifactParent := t.TempDir()
	var calls []loopprobe.AcceptanceInput
	var lastArtifacts ArtifactSet

	transport := LiveTransport{Launch: func(_ context.Context, input loopprobe.AcceptanceInput, artifacts ArtifactSet) (RunResult, error) {
		entries, err := os.ReadDir(input.WorkingDirectory)
		if err != nil {
			t.Fatalf("read fresh working directory: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("working directory is not fresh: %v", entries)
		}
		if err := os.WriteFile(filepath.Join(input.WorkingDirectory, "goal.txt"), []byte("the goal was attained"), 0o600); err != nil {
			t.Fatalf("write simulated goal artifact: %v", err)
		}
		calls = append(calls, input)
		lastArtifacts = artifacts
		return RunResult{
			ExitCode:   0,
			Stdout:     []byte("probe stdout\n"),
			Stderr:     []byte("probe stderr\n"),
			Transcript: []byte("{\"event\":\"completed\"}\n"),
			Report: loopprobe.AcceptanceAgentReport{
				ClaimedSuccess:        true,
				ObjectiveArtifactPath: "goal.txt",
				CheckedClaim:          "the goal was attained",
				SubjectiveRating:      loopprobe.SubjectiveEasy,
			},
		}, nil
	}}
	runner := NewRunner(transport, nil)
	runner.ArtifactRoot = artifactParent

	for range 2 {
		verdict, err := runner.Run(context.Background(), loopprobe.AcceptanceInput{
			BinaryPath: binary,
			Goal:       "Create the requested result",
		})
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if !verdict.Pass || !verdict.ObjectiveEvidence.Verified {
			t.Fatalf("verdict = %+v, want artifact-backed pass", verdict)
		}
		if verdict.TerminalState != loopprobe.AcceptanceCompleted {
			t.Fatalf("terminal state = %q, want completed", verdict.TerminalState)
		}
	}

	if len(calls) != 2 {
		t.Fatalf("launch calls = %d, want 2", len(calls))
	}
	if calls[0].BinaryPath != binary || calls[1].BinaryPath != binary {
		t.Fatalf("resolved binary inputs = %#v, want %q", calls, binary)
	}
	if calls[0].Goal != "Create the requested result" || calls[1].Goal != calls[0].Goal {
		t.Fatalf("goal inputs = %#v", calls)
	}
	if calls[0].WorkingDirectory == "" || calls[0].WorkingDirectory == calls[1].WorkingDirectory {
		t.Fatalf("working directories = %#v, want fresh distinct directories", calls)
	}

	for _, file := range []struct {
		name string
		want string
	}{
		{name: "goal.txt", want: "the goal was attained"},
		{name: "stdout.txt", want: "probe stdout\n"},
		{name: "stderr.txt", want: "probe stderr\n"},
		{name: "transcript.jsonl", want: "{\"event\":\"completed\"}\n"},
	} {
		data, err := os.ReadFile(filepath.Join(lastArtifacts.Root, file.name))
		if err != nil {
			t.Fatalf("read %s: %v", file.name, err)
		}
		if string(data) != file.want {
			t.Errorf("%s = %q, want %q", file.name, data, file.want)
		}
	}
	statusData, err := os.ReadFile(filepath.Join(lastArtifacts.Root, "exit-status.json"))
	if err != nil {
		t.Fatalf("read exit status: %v", err)
	}
	var status struct {
		ExitCode int `json:"exit_code"`
	}
	if err := json.Unmarshal(statusData, &status); err != nil {
		t.Fatalf("decode exit status: %v", err)
	}
	if status.ExitCode != 0 {
		t.Fatalf("recorded exit code = %d, want 0", status.ExitCode)
	}
	reportData, err := os.ReadFile(filepath.Join(lastArtifacts.Root, "agent-report.json"))
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if !strings.Contains(string(reportData), `"terminal_state":"completed"`) {
		t.Fatalf("report = %s, want derived completed state", reportData)
	}
}

func TestAcceptanceProbeNullAgentFailsAndPositiveAgentPasses(t *testing.T) {
	binary := acceptanceTestBinary(t)
	goal := "Create the requested result"

	t.Run("null agent", func(t *testing.T) {
		runner := NewRunner(TransportFunc(func(_ context.Context, _ loopprobe.AcceptanceInput, _ ArtifactSet) (RunResult, error) {
			return RunResult{
				ExitCode: 0,
				Stdout:   []byte("null agent responded\n"),
				Report: loopprobe.AcceptanceAgentReport{
					ClaimedSuccess:   true,
					SubjectiveRating: loopprobe.SubjectiveEasy,
				},
			}, nil
		}), nil)
		runner.ArtifactRoot = t.TempDir()

		verdict, err := runner.Run(context.Background(), loopprobe.AcceptanceInput{BinaryPath: binary, Goal: goal})
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if verdict.Pass {
			t.Fatal("null agent passed; absent objective evidence must fail")
		}
		if verdict.ObjectiveEvidence.Verified {
			t.Fatal("null agent produced verified objective evidence")
		}
		if !strings.Contains(verdict.Error, loopprobe.ErrObjectiveEvidenceAbsent.Error()) {
			t.Fatalf("verdict error = %q, want absent-evidence reason", verdict.Error)
		}
	})

	t.Run("positive agent", func(t *testing.T) {
		runner := NewRunner(TransportFunc(func(_ context.Context, _ loopprobe.AcceptanceInput, artifacts ArtifactSet) (RunResult, error) {
			if err := os.WriteFile(filepath.Join(artifacts.WorkingDirectory, "goal.txt"), []byte("the goal was attained"), 0o600); err != nil {
				t.Fatalf("write positive artifact: %v", err)
			}
			return RunResult{
				ExitCode: 0,
				Report: loopprobe.AcceptanceAgentReport{
					ClaimedSuccess:        true,
					ObjectiveArtifactPath: "goal.txt",
					CheckedClaim:          "the goal was attained",
					SubjectiveRating:      loopprobe.SubjectiveWorkable,
				},
			}, nil
		}), nil)
		runner.ArtifactRoot = t.TempDir()

		verdict, err := runner.Run(context.Background(), loopprobe.AcceptanceInput{BinaryPath: binary, Goal: goal})
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if !verdict.Pass || !verdict.ObjectiveEvidence.Verified {
			t.Fatalf("verdict = %+v, want positive artifact-backed pass", verdict)
		}
	})
}

func TestAcceptanceProbeSeparatesArtifactAndSubjectiveGates(t *testing.T) {
	binary := acceptanceTestBinary(t)
	tests := []struct {
		name         string
		write        bool
		rating       loopprobe.SubjectiveRating
		wantPass     bool
		wantReason   string
		wantVerified bool
	}{
		{name: "claimed success without artifact", write: false, rating: loopprobe.SubjectiveEasy, wantReason: loopprobe.ErrObjectiveEvidenceAbsent.Error()},
		{name: "confusing despite artifact", write: true, rating: loopprobe.SubjectiveConfusing, wantReason: loopprobe.ErrSubjectiveRatingConfusing.Error(), wantVerified: true},
		{name: "verified and clear", write: true, rating: loopprobe.SubjectiveEasy, wantPass: true, wantVerified: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := NewRunner(TransportFunc(func(_ context.Context, _ loopprobe.AcceptanceInput, artifacts ArtifactSet) (RunResult, error) {
				if tt.write {
					if err := os.WriteFile(filepath.Join(artifacts.WorkingDirectory, "result.txt"), []byte("checked claim"), 0o600); err != nil {
						t.Fatalf("write result artifact: %v", err)
					}
				}
				return RunResult{Report: loopprobe.AcceptanceAgentReport{
					ClaimedSuccess:        true,
					ObjectiveArtifactPath: "result.txt",
					CheckedClaim:          "checked claim",
					SubjectiveRating:      tt.rating,
				}}, nil
			}), nil)
			runner.ArtifactRoot = t.TempDir()

			verdict, err := runner.Run(context.Background(), loopprobe.AcceptanceInput{BinaryPath: binary, Goal: "Check the result"})
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if verdict.Pass != tt.wantPass || verdict.ObjectiveEvidence.Verified != tt.wantVerified {
				t.Fatalf("verdict = %+v, want pass=%t verified=%t", verdict, tt.wantPass, tt.wantVerified)
			}
			if tt.wantReason != "" && !strings.Contains(verdict.Error, tt.wantReason) {
				t.Fatalf("verdict error = %q, want %q", verdict.Error, tt.wantReason)
			}
		})
	}
}

func TestReplayTransportUsesTheAcceptancePipeline(t *testing.T) {
	fixturePath := filepath.Join(t.TempDir(), "positive.acceptance.json")
	fixture := ReplayFixture{
		Input:      &loopprobe.AcceptanceInput{Goal: "Replay this goal"},
		Stdout:     "replay goal attained\n",
		Transcript: "{\"event\":\"completed\"}\n",
		Report: loopprobe.AcceptanceAgentReport{
			ClaimedSuccess:        true,
			ObjectiveArtifactPath: "stdout.txt",
			CheckedClaim:          "replay goal attained",
			SubjectiveRating:      loopprobe.SubjectiveEasy,
		},
	}
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(fixturePath, data, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	runner, err := NewReplayRunner(fixturePath, nil)
	if err != nil {
		t.Fatalf("NewReplayRunner() error = %v", err)
	}
	runner.ArtifactRoot = t.TempDir()
	verdict, err := runner.Run(context.Background(), loopprobe.AcceptanceInput{
		BinaryPath: acceptanceTestBinary(t),
		Goal:       "Replay this goal",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !verdict.Pass || verdict.Transport != loopprobe.AcceptanceTransportReplay || !verdict.ObjectiveEvidence.Verified {
		t.Fatalf("replay verdict = %+v, want replay pass with verified evidence", verdict)
	}
}

func TestAcceptanceProbeTypedErrorPaths(t *testing.T) {
	binary := acceptanceTestBinary(t)
	nonExecutable := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(nonExecutable, []byte("not executable"), 0o600); err != nil {
		t.Fatalf("write non-executable: %v", err)
	}
	nonEmptyDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(nonEmptyDir, "README"), []byte("not available to the probe"), 0o600); err != nil {
		t.Fatalf("write non-empty directory marker: %v", err)
	}

	tests := []struct {
		name  string
		input loopprobe.AcceptanceInput
		want  error
	}{
		{name: "missing goal", input: loopprobe.AcceptanceInput{BinaryPath: binary}, want: ErrGoalMissing},
		{name: "missing binary", input: loopprobe.AcceptanceInput{BinaryPath: filepath.Join(t.TempDir(), "missing"), Goal: "run"}, want: ErrBinaryMissing},
		{name: "non executable binary", input: loopprobe.AcceptanceInput{BinaryPath: nonExecutable, Goal: "run"}, want: ErrBinaryNotExecutable},
		{name: "non empty working directory", input: loopprobe.AcceptanceInput{BinaryPath: binary, Goal: "run", WorkingDirectory: nonEmptyDir}, want: ErrWorkingDirectoryNotEmpty},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := NewRunner(TransportFunc(func(context.Context, loopprobe.AcceptanceInput, ArtifactSet) (RunResult, error) {
				t.Fatal("transport ran for invalid input")
				return RunResult{}, nil
			}), nil)
			_, err := runner.Run(context.Background(), tt.input)
			if err == nil || !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, tt.want)
			}
		})
	}

	t.Run("probe crash", func(t *testing.T) {
		runner := NewRunner(TransportFunc(func(context.Context, loopprobe.AcceptanceInput, ArtifactSet) (RunResult, error) {
			return RunResult{}, errors.New("simulated crash")
		}), nil)
		runner.ArtifactRoot = t.TempDir()
		verdict, err := runner.Run(context.Background(), loopprobe.AcceptanceInput{BinaryPath: binary, Goal: "run"})
		if err == nil || !errors.Is(err, ErrProbeAgentCrashed) {
			t.Fatalf("error = %v, want probe crash", err)
		}
		if verdict.TerminalState != loopprobe.AcceptanceErrored {
			t.Fatalf("terminal state = %q, want errored", verdict.TerminalState)
		}
	})

	t.Run("nonzero process exit cannot pass a claimed report", func(t *testing.T) {
		runner := NewRunner(TransportFunc(func(_ context.Context, _ loopprobe.AcceptanceInput, artifacts ArtifactSet) (RunResult, error) {
			if err := os.WriteFile(filepath.Join(artifacts.WorkingDirectory, "result.txt"), []byte("done"), 0o600); err != nil {
				t.Fatalf("write result artifact: %v", err)
			}
			return RunResult{ExitCode: 7, Report: loopprobe.AcceptanceAgentReport{
				ClaimedSuccess:        true,
				ObjectiveArtifactPath: "result.txt",
				CheckedClaim:          "done",
				SubjectiveRating:      loopprobe.SubjectiveEasy,
				TerminalState:         loopprobe.AcceptanceCompleted,
			}}, nil
		}), nil)
		runner.ArtifactRoot = t.TempDir()
		verdict, err := runner.Run(context.Background(), loopprobe.AcceptanceInput{BinaryPath: binary, Goal: "run"})
		if err != nil {
			t.Fatalf("Run() error = %v, want normal failed verdict", err)
		}
		if verdict.Pass || verdict.TerminalState != loopprobe.AcceptanceErrored {
			t.Fatalf("verdict = %+v, want errored failure", verdict)
		}
	})

	t.Run("unknown goal from validator", func(t *testing.T) {
		runner := NewRunner(TransportFunc(func(context.Context, loopprobe.AcceptanceInput, ArtifactSet) (RunResult, error) {
			t.Fatal("transport ran for an unknown goal")
			return RunResult{}, nil
		}), nil)
		runner.ValidateGoal = func(string) error { return errors.New("not in the configured goal catalog") }
		_, err := runner.Run(context.Background(), loopprobe.AcceptanceInput{BinaryPath: binary, Goal: "unknown"})
		if err == nil || !errors.Is(err, ErrUnknownGoal) {
			t.Fatalf("error = %v, want unknown-goal identity", err)
		}
		if !strings.Contains(err.Error(), "goal") {
			t.Fatalf("error = %v, want actionable goal context", err)
		}
	})

	t.Run("deadline is stuck", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		runner := NewRunner(TransportFunc(func(ctx context.Context, _ loopprobe.AcceptanceInput, _ ArtifactSet) (RunResult, error) {
			return RunResult{}, ctx.Err()
		}), nil)
		runner.ArtifactRoot = t.TempDir()
		verdict, err := runner.Run(ctx, loopprobe.AcceptanceInput{BinaryPath: binary, Goal: "run"})
		if err == nil || !errors.Is(err, ErrProbeAgentStuck) {
			t.Fatalf("error = %v, want stuck probe", err)
		}
		if verdict.TerminalState != loopprobe.AcceptanceStuckPendingDownstream {
			t.Fatalf("terminal state = %q, want stuck-pending-downstream", verdict.TerminalState)
		}
	})
}

func TestLiveTransportCapturesARealProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell executable fixture is POSIX-specific")
	}
	path := filepath.Join(t.TempDir(), "probe-agent.sh")
	script := "#!/bin/sh\nprintf 'live goal attained\\n' > goal.txt\nprintf '%s\\n' '{\"claimed_success\":true,\"objective_artifact_path\":\"goal.txt\",\"checked_claim\":\"live goal attained\",\"subjective_rating\":\"easy\"}'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write process fixture: %v", err)
	}

	runner := NewLiveRunner(nil)
	runner.ArtifactRoot = t.TempDir()
	verdict, err := runner.Run(context.Background(), loopprobe.AcceptanceInput{BinaryPath: path, Goal: "Make the live result"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !verdict.Pass || !verdict.ObjectiveEvidence.Verified {
		t.Fatalf("live verdict = %+v, want artifact-backed pass", verdict)
	}
}

func TestLiveTransportDoesNotForwardParentWorkspaceEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell executable fixture is POSIX-specific")
	}
	workspaceSentinel := filepath.Join(t.TempDir(), "parent-workspace-sentinel")
	t.Setenv("GITHUB_WORKSPACE", workspaceSentinel)
	t.Setenv("OLDPWD", workspaceSentinel)

	path := filepath.Join(t.TempDir(), "blind-environment-agent.sh")
	script := `#!/bin/sh
if [ -n "${GITHUB_WORKSPACE:-}" ] || [ -n "${OLDPWD:-}" ]; then
  printf '%s\n' 'parent workspace environment leaked' >&2
  exit 7
fi
printf '%s\n' 'blind environment attained' > result.txt
printf '%s\n' '{"claimed_success":true,"objective_artifact_path":"result.txt","checked_claim":"blind environment attained","subjective_rating":"easy"}'
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write process fixture: %v", err)
	}

	runner := NewLiveRunner(nil)
	runner.ArtifactRoot = t.TempDir()
	verdict, err := runner.Run(context.Background(), loopprobe.AcceptanceInput{BinaryPath: path, Goal: "Run without parent workspace context"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !verdict.Pass || !verdict.ObjectiveEvidence.Verified {
		t.Fatalf("live verdict = %+v, want pass without leaked parent environment", verdict)
	}
	if data, readErr := os.ReadFile(filepath.Join(verdict.RunDirectory, "stderr.txt")); readErr != nil {
		t.Fatalf("read stderr artifact: %v", readErr)
	} else if strings.Contains(string(data), "parent workspace environment leaked") {
		t.Fatalf("child observed parent workspace environment: %q", data)
	}
}

func acceptanceTestBinary(t *testing.T) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	return binary
}
