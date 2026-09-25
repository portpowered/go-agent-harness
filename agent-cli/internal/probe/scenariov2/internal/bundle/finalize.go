package bundle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenariov2/internal/objective"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

// Input is everything one executed scenario contributes to its bundle.
type Input struct {
	Destination    string
	Scenario       probe.ScenarioV2
	Replay         runtimeReplay.Service
	ProviderPath   string
	ProviderReport *runtimeReplay.CaptureProbeObservation
	// PageState is the raw page-state oracle value; JSON null when absent.
	PageState json.RawMessage
	// BrowserEvents is the raw recorder output; empty when no browser ran.
	BrowserEvents []byte
	Transport     string
	ClockBase     string
}

// Output describes a finalized, independently re-verified bundle.
type Output struct {
	Summary    Summary
	Objective  probe.ObjectiveEvidence
	Divergence *objective.Divergence
}

type preparedArtifacts struct {
	pageState       []byte
	workspace       []byte
	providerCapture []byte
	events          []testkit.Event
	browser         *transcript.BrowserArtifact
}

// Finalize writes the v2 recording bundle to in.Destination and then verifies
// the committed layout from disk. A bundle that fails post-commit
// verification is removed so it cannot be mistaken for acceptance evidence.
func Finalize(ctx context.Context, in Input) (Output, error) {
	if strings.TrimSpace(in.Destination) == "" {
		return Output{}, errors.New("v2 evidence destination is empty")
	}
	prepared, err := prepareArtifacts(ctx, in)
	if err != nil {
		return Output{}, err
	}
	objectiveData, err := objectiveBytes(in, prepared)
	if err != nil {
		return Output{}, fmt.Errorf("encode objective evidence: %w", err)
	}
	if err := transcript.WriteRecordingBundle(recordingConfig(in, prepared, objectiveData)); err != nil {
		return Output{}, fmt.Errorf("finalize v2 evidence: %w", err)
	}
	post, err := Verify(ctx, in.Replay, in.Destination, in.Scenario)
	if err != nil {
		// The destination is a fresh run-scoped path. Remove it if the
		// post-commit verifier rejects the complete layout so callers cannot
		// mistake a structurally unverified bundle for acceptance evidence.
		return Output{}, fmt.Errorf("verify finalized v2 evidence: %w", errors.Join(err, os.RemoveAll(in.Destination)))
	}
	return Output{
		Summary: summaryFor(in.Destination, in.Scenario.ID, prepared.browser),
		Objective: probe.ObjectiveEvidence{
			ArtifactPath: objective.ObjectiveArtifactPath,
			CheckedClaim: post.CheckedClaim,
			Verified:     post.Verified,
		},
		Divergence: post.Divergence,
	}, nil
}

func prepareArtifacts(ctx context.Context, in Input) (preparedArtifacts, error) {
	var prepared preparedArtifacts
	var err error
	if prepared.pageState, err = pageStateBytes(in.PageState); err != nil {
		return preparedArtifacts{}, err
	}
	if prepared.workspace, err = workspaceBytes(in.Scenario); err != nil {
		return preparedArtifacts{}, fmt.Errorf("encode workspace snapshot: %w", err)
	}
	if prepared.providerCapture, err = providerCaptureBytes(ctx, in); err != nil {
		return preparedArtifacts{}, fmt.Errorf("encode provider capture: %w", err)
	}
	if prepared.events, prepared.browser, err = browserEvidence(in); err != nil {
		return preparedArtifacts{}, err
	}
	return prepared, nil
}

func pageStateBytes(state json.RawMessage) ([]byte, error) {
	normalized, err := testkit.JSONValue(state)
	if err != nil {
		return nil, fmt.Errorf("encode page-state oracle snapshot: %w", err)
	}
	return append(normalized, '\n'), nil
}

func workspaceBytes(scenario probe.ScenarioV2) ([]byte, error) {
	return jsonLine(workspaceSnapshot{Version: workspaceVersion, ScenarioID: scenario.ID, Scenario: scenario})
}

func providerCaptureBytes(ctx context.Context, in Input) ([]byte, error) {
	if in.Replay == nil {
		return nil, errors.New("replay service is required to write provider capture evidence")
	}
	var output bytes.Buffer
	request := runtimeReplay.CaptureDocumentRequest{SourcePath: in.ProviderPath}
	if in.ProviderPath == "" {
		request.SyntheticSessionID = in.Scenario.ID
	}
	if err := in.Replay.WriteCaptureDocument(ctx, request, &output); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func browserEvidence(in Input) ([]testkit.Event, *transcript.BrowserArtifact, error) {
	if len(in.BrowserEvents) == 0 {
		if in.Scenario.BrowserFixture != "" {
			return nil, nil, errors.New("browser fixture completed without browser event evidence")
		}
		return nil, nil, nil
	}
	events, err := testkit.ValidateEventStream(in.BrowserEvents)
	if err != nil {
		return nil, nil, fmt.Errorf("validate browser evidence before finalization: %w", err)
	}
	canonical, err := testkit.MarshalEvents(events)
	if err != nil {
		return nil, nil, fmt.Errorf("canonicalize browser evidence: %w", err)
	}
	return events, browserRecordingArtifact(canonical), nil
}

func objectiveBytes(in Input, prepared preparedArtifacts) ([]byte, error) {
	var report runtimeReplay.CaptureProbeObservation
	if in.ProviderReport != nil {
		report = *in.ProviderReport
	}
	verification := objective.VerifyEvidenceData(in.Scenario, prepared.events, bytes.TrimSpace(prepared.pageState), report, prepared.browser != nil)
	return jsonLine(objectiveArtifact{
		Version:               objectiveVersion,
		ScenarioID:            in.Scenario.ID,
		CheckedClaim:          verification.CheckedClaim,
		Verified:              verification.Verified,
		ProviderCapturePath:   objective.ProviderArtifactPath,
		BrowserEventsPath:     browserArtifactPath(prepared.browser),
		PageStatePath:         objective.PageStateArtifactPath,
		WorkspaceSnapshotPath: objective.WorkspaceArtifactPath,
		Error:                 verification.Error,
		Divergence:            verification.Divergence,
	})
}

func recordingConfig(in Input, prepared preparedArtifacts, objectiveData []byte) transcript.RecordingConfig {
	model := defaultModelLabel
	if in.ProviderReport != nil && in.ProviderReport.Model != "" {
		model = in.ProviderReport.Model
	}
	return transcript.RecordingConfig{
		Destination:      in.Destination,
		ClientTranscript: []byte("probe.scenario.v2:" + in.Scenario.ID + "\n"),
		AgentTranscript:  []byte("probe.scenario.v2 evidence\n"),
		Metadata: transcript.RecordingMetadata{
			Transport: in.Transport,
			Model:     model,
			ClockBase: in.ClockBase,
		},
		ManifestVersion: transcript.RecordingManifestV2Version,
		BrowserArtifact: prepared.browser,
		AdditionalArtifacts: []transcript.RecordingArtifact{
			{Path: objective.ProviderArtifactPath, Data: prepared.providerCapture},
			{Path: objective.PageStateArtifactPath, Data: prepared.pageState},
			{Path: objective.WorkspaceArtifactPath, Data: prepared.workspace},
			{Path: objective.ObjectiveArtifactPath, Data: objectiveData},
		},
	}
}

func summaryFor(destination, scenarioID string, browser *transcript.BrowserArtifact) Summary {
	return Summary{
		ScenarioID:            scenarioID,
		ManifestPath:          filepath.Join(destination, manifestFileName),
		ProviderCapturePath:   filepath.Join(destination, objective.ProviderArtifactPath),
		BrowserEventsPath:     optionalEvidencePath(destination, browserArtifactPath(browser)),
		PageStatePath:         filepath.Join(destination, objective.PageStateArtifactPath),
		WorkspaceSnapshotPath: filepath.Join(destination, objective.WorkspaceArtifactPath),
		ObjectiveEvidencePath: filepath.Join(destination, objective.ObjectiveArtifactPath),
	}
}
