package services

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

func TestRunSelfPlay_WritesPerAgentEvidenceAndManifest(t *testing.T) {
	pair := newSelfPlayEchoPair()
	outputDir := filepath.Join(t.TempDir(), "run")
	const secret = "sk-self-play-evidence-secret"

	result, err := RunSelfPlayWithResult(context.Background(), nil, SelfPlayRunOptions{
		APIKey:              secret,
		OutputDir:           outputDir,
		MaxDuration:         time.Second,
		MaxTurns:            2,
		CustomerInferencer:  pair.customer,
		AssistantInferencer: pair.assistant,
	})
	if err != nil {
		t.Fatalf("RunSelfPlayWithResult: %v", err)
	}
	if result.StopReason != SelfPlayStopTurnTarget || result.CustomerTurns != 2 || result.AssistantTurns != 2 {
		t.Fatalf("self-play result = %+v, want turn-target with two turns per side", result)
	}

	manifestBytes := readSelfPlayArtifact(t, filepath.Join(outputDir, SelfPlayManifestPath))
	if bytes.Contains(manifestBytes, []byte(secret)) || bytes.Contains(bytes.ToLower(manifestBytes), []byte("authorization")) {
		t.Fatalf("manifest contains credential material: %s", manifestBytes)
	}
	var manifest selfPlayManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.SchemaVersion != selfPlayManifestSchemaVersion {
		t.Fatalf("manifest schema version = %d, want %d", manifest.SchemaVersion, selfPlayManifestSchemaVersion)
	}
	if manifest.Provider != SelfPlayDefaultProvider || manifest.Model != SelfPlayDefaultModel {
		t.Fatalf("manifest provider/model = %q/%q, want %q/%q", manifest.Provider, manifest.Model, SelfPlayDefaultProvider, SelfPlayDefaultModel)
	}
	if manifest.OpeningSeed != SelfPlayOpeningSeed || manifest.Personas.Customer != SelfPlayCustomerPersona || manifest.Personas.Assistant != SelfPlayAssistantPersona {
		t.Fatalf("manifest fixed contract fields are incorrect: %+v", manifest)
	}
	if manifest.StopReason != SelfPlayStopTurnTarget || manifest.Bounds.MaxTurns != 2 || manifest.Bounds.MaxDuration != time.Second.String() {
		t.Fatalf("manifest stop/bounds = %q/%+v", manifest.StopReason, manifest.Bounds)
	}
	if manifest.Timing.StartedAt == "" || manifest.Timing.EndedAt == "" || manifest.Timing.Elapsed == "" || !strings.HasSuffix(manifest.Timing.StartedAt, "Z") || !strings.HasSuffix(manifest.Timing.EndedAt, "Z") {
		t.Fatalf("manifest timing is incomplete or not UTC: %+v", manifest.Timing)
	}
	if len(manifest.Agents) != 2 {
		t.Fatalf("manifest agents = %d, want 2", len(manifest.Agents))
	}

	wantAgents := map[string]struct {
		role        string
		turns       int
		wavPath     string
		diagnostics string
		streamPath  string
	}{
		"agent-a": {role: "customer", turns: 2, wavPath: SelfPlayAgentAWAVPath, diagnostics: SelfPlayAgentADiagnosticsPath, streamPath: SelfPlayAgentAStreamDeltasPath},
		"agent-b": {role: "assistant", turns: 2, wavPath: SelfPlayAgentBWAVPath, diagnostics: SelfPlayAgentBDiagnosticsPath, streamPath: SelfPlayAgentBStreamDeltasPath},
	}
	for id, want := range wantAgents {
		agent, ok := manifest.Agents[id]
		if !ok {
			t.Fatalf("manifest is missing %s", id)
		}
		if agent.Role != want.role || agent.CompletedTurns != want.turns || !agent.TerminalClean || agent.TerminalError != "" {
			t.Fatalf("manifest %s state = %+v", id, agent)
		}
		if agent.Artifacts.WAV != want.wavPath || agent.Artifacts.Diagnostics != want.diagnostics || agent.Artifacts.StreamDeltas != want.streamPath {
			t.Fatalf("manifest %s artifacts = %+v", id, agent.Artifacts)
		}

		wavData := readSelfPlayArtifact(t, filepath.Join(outputDir, agent.Artifacts.WAV))
		rate, samples, err := wavio.Read(bytes.NewReader(wavData))
		if err != nil {
			t.Fatalf("decode %s WAV: %v", id, err)
		}
		if rate != selfPlaySampleRate || len(samples) == 0 {
			t.Fatalf("%s WAV rate/samples = %d/%d, want %d/non-empty", id, rate, len(samples), selfPlaySampleRate)
		}

		diagnosticLines := readSelfPlayJSONLLines(t, filepath.Join(outputDir, agent.Artifacts.Diagnostics))
		turns := 0
		for _, line := range diagnosticLines {
			var diagnostic selfPlayDiagnosticLine
			if err := json.Unmarshal(line, &diagnostic); err != nil {
				t.Fatalf("decode %s diagnostic line: %v", id, err)
			}
			if diagnostic.Event == SessionDiagnosticEventTurn {
				turns++
			}
		}
		if turns != want.turns {
			t.Fatalf("%s diagnostic turn records = %d, want %d", id, turns, want.turns)
		}

		streamLines := readSelfPlayJSONLLines(t, filepath.Join(outputDir, agent.Artifacts.StreamDeltas))
		messageEnds := 0
		for _, line := range streamLines {
			msg, err := gwtesting.UnmarshalStreamMessage(line)
			if err != nil {
				t.Fatalf("decode %s stream delta: %v", id, err)
			}
			if msg.Type == messages.StreamTypeMessageEnd {
				messageEnds++
			}
		}
		if messageEnds < want.turns {
			t.Fatalf("%s stream MESSAGE.END records = %d, want at least %d", id, messageEnds, want.turns)
		}
	}

	for name, path := range manifest.Artifacts {
		if filepath.IsAbs(path) || filepath.Clean(path) != path || strings.HasPrefix(path, "..") {
			t.Fatalf("manifest artifact %q has unsafe path %q", name, path)
		}
		if _, err := os.Stat(filepath.Join(outputDir, path)); err != nil {
			t.Fatalf("manifest artifact %q (%s) is not readable: %v", name, path, err)
		}
	}
}

func TestRunSelfPlay_PreservesFailedEvidenceAndRedactsErrors(t *testing.T) {
	outputDir := filepath.Join(t.TempDir(), "run")
	const secret = "sk-failure-evidence-secret"
	wantErr := fmt.Errorf("provider authorization: Bearer %s", secret)

	result, err := RunSelfPlayWithResult(context.Background(), nil, SelfPlayRunOptions{
		APIKey:              secret,
		OutputDir:           outputDir,
		MaxDuration:         time.Second,
		MaxTurns:            2,
		CustomerInferencer:  &selfPlayConnectFailInferencer{err: wantErr},
		AssistantInferencer: newSelfPlayBlockingInferencer(),
	})
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("failure result error = %v, want %v", err, wantErr)
	}
	if result.StopReason != SelfPlayStopFailure {
		t.Fatalf("failure result stop reason = %q, want %q", result.StopReason, SelfPlayStopFailure)
	}

	manifestBytes := readSelfPlayArtifact(t, filepath.Join(outputDir, SelfPlayManifestPath))
	if bytes.Contains(manifestBytes, []byte(secret)) {
		t.Fatalf("failed manifest contains API key: %s", manifestBytes)
	}
	var manifest selfPlayManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode failed manifest: %v", err)
	}
	if manifest.StopReason != SelfPlayStopFailure || !strings.Contains(manifest.Error, "REDACTED") {
		t.Fatalf("failed manifest outcome = %+v", manifest)
	}
	for _, id := range []string{"agent-a", "agent-b"} {
		agent, ok := manifest.Agents[id]
		if !ok {
			t.Fatalf("failed manifest is missing %s", id)
		}
		if agent.TerminalClean {
			t.Fatalf("failed manifest marks %s clean: %+v", id, agent)
		}
		if strings.Contains(agent.TerminalError, secret) {
			t.Fatalf("failed manifest %s terminal error contains API key: %q", id, agent.TerminalError)
		}
		for _, path := range []string{agent.Artifacts.WAV, agent.Artifacts.Diagnostics, agent.Artifacts.StreamDeltas} {
			readSelfPlayArtifact(t, filepath.Join(outputDir, path))
		}
	}
}

func TestSelfPlayEvidence_FinalizeUsesOneTerminalSnapshot(t *testing.T) {
	const target = 2
	outputDir := filepath.Join(t.TempDir(), "run")
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatalf("create evidence directory: %v", err)
	}
	evidence, err := newSelfPlayEvidence(outputDir, SelfPlayRunOptions{
		Provider:    SelfPlayDefaultProvider,
		Model:       SelfPlayDefaultModel,
		MaxDuration: time.Second,
		MaxTurns:    target,
		OutputDir:   outputDir,
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("new self-play evidence: %v", err)
	}

	published := make(chan struct{}, 1)
	stop := newSelfPlayStopState(func() { published <- struct{}{} })
	if !stop.recordTurn(0, target) || !stop.recordTurn(0, target) || !stop.recordTurn(1, target-1) {
		t.Fatal("failed to establish pre-target state")
	}

	type finalizeResult struct {
		result SelfPlayResult
		err    error
	}
	finalized := make(chan finalizeResult, 1)
	finalizerReady := make(chan struct{})
	var finalizerWG sync.WaitGroup
	finalizerWG.Add(1)
	go func() {
		defer finalizerWG.Done()
		close(finalizerReady)
		<-published
		result, runErr := stop.snapshot()
		finalized <- finalizeResult{
			result: result,
			err:    evidence.finalize(result, runErr, time.Now().UTC()),
		}
	}()
	<-finalizerReady

	if !stop.recordTurn(1, target) {
		t.Fatal("final target turn was rejected")
	}

	var finalizedRun finalizeResult
	select {
	case finalizedRun = <-finalized:
	case <-time.After(time.Second):
		t.Fatal("evidence finalization did not cross the published stop boundary")
	}
	finalizerWG.Wait()
	if finalizedRun.err != nil {
		t.Fatalf("finalize: %v", finalizedRun.err)
	}
	if finalizedRun.result.StopReason != SelfPlayStopTurnTarget || finalizedRun.result.CustomerTurns != target || finalizedRun.result.AssistantTurns != target {
		t.Fatalf("finalized stop snapshot = %+v, want exact target", finalizedRun.result)
	}

	for range 8 {
		result, runErr := stop.snapshot()
		if result != finalizedRun.result || runErr != finalizedRun.err {
			t.Fatalf("terminal snapshot changed after finalization: %+v/%v vs %+v/%v", result, runErr, finalizedRun.result, finalizedRun.err)
		}
	}

	// A later caller cannot replace the already-written terminal evidence with
	// a fabricated reason or count, even if it tries to finalize again.
	if err := evidence.finalize(SelfPlayResult{
		StopReason:     SelfPlayStopFailure,
		CustomerTurns:  99,
		AssistantTurns: 99,
	}, errors.New("late contender"), time.Now().UTC()); err != nil {
		t.Fatalf("late finalize: %v", err)
	}

	manifestBytes := readSelfPlayArtifact(t, filepath.Join(outputDir, SelfPlayManifestPath))
	var manifest selfPlayManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode finalized manifest: %v", err)
	}
	if manifest.StopReason != SelfPlayStopTurnTarget {
		t.Fatalf("finalized manifest reason = %q, want %q", manifest.StopReason, SelfPlayStopTurnTarget)
	}
	for _, id := range []string{"agent-a", "agent-b"} {
		if manifest.Agents[id].CompletedTurns != target {
			t.Fatalf("finalized manifest %s turns = %d, want %d", id, manifest.Agents[id].CompletedTurns, target)
		}
	}
}

func readSelfPlayArtifact(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read self-play artifact %s: %v", path, err)
	}
	return data
}

func readSelfPlayJSONLLines(t *testing.T, path string) []json.RawMessage {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open JSONL artifact %s: %v", path, err)
	}
	defer func() { _ = file.Close() }()

	var lines []json.RawMessage
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || !json.Valid(line) {
			t.Fatalf("JSONL artifact %s contains an invalid line %q", path, line)
		}
		lines = append(lines, append(json.RawMessage(nil), line...))
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan JSONL artifact %s: %v", path, err)
	}
	if len(lines) == 0 {
		t.Fatalf("JSONL artifact %s is empty", path)
	}
	return lines
}
