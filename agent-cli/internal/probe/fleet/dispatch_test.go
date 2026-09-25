package fleet

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/replay"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	replaywire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
)

const (
	cliTestdata       = "../../transport/cli/testdata"
	happyScenarioPath = cliTestdata + "/probe-scenarios/s2s-v2a-audio-in-basic.scenario.json"
	silentScenario    = cliTestdata + "/probe-scenarios/s2s-v2a-audio-in-basic-no-response.scenario.json"
	happyFixture      = cliTestdata + "/probe-fixtures/s2s_v2a_audio_in_basic.session.json"
	happyScenarioID   = "s2s-v2a-audio-in-basic"
)

func testCorpus() replay.Corpus {
	return replay.Corpus{WorkingDir: func() (string, error) { return filepath.Abs(".") }}
}

func composedManifest(t *testing.T, transports []Transport, files ...string) Manifest {
	t.Helper()
	manifest, err := Compose(ComposeInput{ScenarioFiles: files, Transports: transports, RepeatCount: 1, Concurrency: 1})
	if err != nil {
		t.Fatalf("compose manifest: %v", err)
	}
	return manifest
}

func TestDefaultExecutorRunsReplayEntriesAndReportsFailures(t *testing.T) {
	manifest := composedManifest(t, []Transport{TransportReplay}, happyScenarioPath, silentScenario)
	executor, err := NewDefaultExecutor(t.Context(), manifest, DefaultExecutorConfig{
		ReplayPath: cliTestdata + "/probe-fixtures", Replay: replaywire.NewService(), Corpus: testCorpus(),
	})
	if err != nil {
		t.Fatalf("build executor: %v", err)
	}
	execution, err := Execute(t.Context(), manifest, executor)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, result := range execution.Results {
		wantPass := result.ScenarioID == happyScenarioID
		if result.Pass != wantPass || (!wantPass && !strings.Contains(result.Error, "transcript")) {
			t.Fatalf("entry %s = pass %t error %q, want pass %t", result.ID, result.Pass, result.Error, wantPass)
		}
	}
}

func TestDefaultExecutorRejectsIncompleteReplayConfiguration(t *testing.T) {
	manifest := composedManifest(t, []Transport{TransportReplay}, happyScenarioPath)
	if _, err := NewDefaultExecutor(t.Context(), manifest, DefaultExecutorConfig{}); err == nil || !strings.Contains(err.Error(), "--replay <fixture-path-or-dir> is required") {
		t.Fatalf("missing replay path error = %v", err)
	}
	if _, err := NewDefaultExecutor(t.Context(), manifest, DefaultExecutorConfig{ReplayPath: happyFixture}); err == nil || !strings.Contains(err.Error(), "fleet replay service is required") {
		t.Fatalf("missing replay service error = %v", err)
	}
	invalid := filepath.Join(t.TempDir(), "broken.session.json")
	if err := os.WriteFile(invalid, []byte("{"), 0o600); err != nil {
		t.Fatalf("write invalid fixture: %v", err)
	}
	if _, err := NewDefaultExecutor(t.Context(), manifest, DefaultExecutorConfig{ReplayPath: invalid, Replay: replaywire.NewService()}); err == nil || !strings.Contains(err.Error(), "invalid replay fixture") {
		t.Fatalf("invalid fixture error = %v", err)
	}
	dispatch, err := NewDefaultExecutor(t.Context(), Manifest{}, DefaultExecutorConfig{})
	if err != nil {
		t.Fatalf("empty manifest executor: %v", err)
	}
	if _, err := dispatch(t.Context(), Entry{ID: "x", Transport: TransportLive}); err == nil || !strings.Contains(err.Error(), "unsupported transport") {
		t.Fatalf("undeclared transport error = %v", err)
	}
}

func TestLiveExecutorRecordsSessionAndPassesCorpusAudio(t *testing.T) {
	manifest := composedManifest(t, []Transport{TransportLive}, happyScenarioPath)
	var gotInput serviceSession.AudioInput
	executor, err := NewDefaultExecutor(t.Context(), manifest, DefaultExecutorConfig{
		Replay: replaywire.NewService(), Corpus: testCorpus(),
		Live: LiveExecutor{Options: serviceSession.Request{Provider: "grok"}, Run: func(_ context.Context, _ io.Writer, request serviceSession.Request, input serviceSession.AudioInput) error {
			gotInput = input
			capture, err := os.ReadFile(happyFixture)
			if err != nil {
				return err
			}
			return os.WriteFile(request.RecordPath, capture, 0o600)
		}},
	})
	if err != nil {
		t.Fatalf("build executor: %v", err)
	}
	outcome, err := executor(t.Context(), manifest.Entries[0])
	if err != nil || !outcome.Pass {
		t.Fatalf("live outcome = %+v, %v", outcome, err)
	}
	if !gotInput.Present || filepath.Base(gotInput.Path) != "utt_short_16k.wav" {
		t.Fatalf("live audio input = %+v, want committed corpus", gotInput)
	}
}

func TestLiveInputsAcceptOnlyOneTurnTextOrAudio(t *testing.T) {
	executor := LiveExecutor{Corpus: testCorpus()}
	text := probe.Step{Type: probe.StepSendText, Text: "hi"}
	audio := probe.Step{Kind: probe.StepSendAudio, Corpus: probe.AudioCorpusReference{CorpusID: "utterance-hello-there"}}
	prompt, audioPath, err := executor.inputs(probe.Scenario{ID: "ok", Steps: []probe.Step{text, audio, {Type: probe.StepClose}}})
	if err != nil || prompt != "hi" || filepath.Base(audioPath) != "utt_short_16k.wav" {
		t.Fatalf("inputs = %q %q %v", prompt, audioPath, err)
	}
	for _, test := range []struct {
		steps []probe.Step
		want  string
	}{
		{[]probe.Step{audio, text}, "sends text after audio"},
		{[]probe.Step{text, text}, "more than one input"},
		{[]probe.Step{audio, audio}, "more than one audio input"},
		{[]probe.Step{{Type: probe.StepWait}}, `uses "wait"`},
		{[]probe.Step{{Type: probe.StepClose}}, "has no send_text or send_audio input"},
		{[]probe.Step{{Type: probe.StepSendAudio, CorpusID: "v3c-utterance-1"}}, "no committed WAV mapping"},
	} {
		if _, _, err := executor.inputs(probe.Scenario{ID: "bad", Steps: test.steps}); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("inputs(%v) error = %v, want %q", test.steps, err, test.want)
		}
	}
}

func TestLiveExecutorReportsSessionFailureAndScenarioMismatch(t *testing.T) {
	sessionErr := errors.New("provider unavailable")
	executor := LiveExecutor{Replay: replaywire.NewService(), Corpus: testCorpus(), Run: func(context.Context, io.Writer, serviceSession.Request, serviceSession.AudioInput) error {
		return sessionErr
	}}
	outcome, err := executor.Execute(t.Context(), Entry{ID: "e", ScenarioID: happyScenarioID, ScenarioPath: happyScenarioPath})
	if err != nil || outcome.Pass || outcome.Err == nil || !strings.Contains(outcome.Err.Error(), "provider unavailable") {
		t.Fatalf("session failure outcome = %+v, %v", outcome, err)
	}
	if _, err := executor.Execute(t.Context(), Entry{ID: "e", ScenarioID: "other", ScenarioPath: happyScenarioPath}); err == nil || !strings.Contains(err.Error(), "does not match manifest entry") {
		t.Fatalf("mismatch error = %v", err)
	}
	if _, err := executor.Execute(t.Context(), Entry{ID: "e", ScenarioPath: filepath.Join(t.TempDir(), "missing.json")}); err == nil || !strings.Contains(err.Error(), "load scenario") {
		t.Fatalf("missing scenario error = %v", err)
	}
}

func TestResultErrorAndDecodeExplainFailures(t *testing.T) {
	if err := resultError(probe.ScenarioResult{Error: "boom"}); err.Error() != "boom" {
		t.Fatalf("result error = %v", err)
	}
	outcomes := []probe.ScenarioExpectationOutcome{{Index: 0, Passed: true}, {Index: 1}}
	if err := resultError(probe.ScenarioResult{ScenarioExpectationOutcomes: outcomes}); err.Error() != "probe expectation 1 failed" {
		t.Fatalf("expectation error = %v", err)
	}
	if err := resultError(probe.ScenarioResult{}); err.Error() != "probe expectations failed" {
		t.Fatalf("fallback error = %v", err)
	}
	if _, err := decodeSingleResult([]byte("{\"total\":1}\n\n")); err == nil || !strings.Contains(err.Error(), "no scenario result") {
		t.Fatalf("empty decode error = %v", err)
	}
	if _, err := decodeSingleResult([]byte("{")); err == nil || !strings.Contains(err.Error(), "decode fleet probe result") {
		t.Fatalf("malformed decode error = %v", err)
	}
}
