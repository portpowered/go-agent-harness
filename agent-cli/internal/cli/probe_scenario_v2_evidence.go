package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	probeScenarioV2ProviderArtifactPath  = "provider.capture.json"
	probeScenarioV2PageStateArtifactPath = "page-state.json"
	probeScenarioV2WorkspaceArtifactPath = "workspace.snapshot.json"
	probeScenarioV2ObjectiveArtifactPath = "objective.evidence.json"
	probeScenarioV2ObjectiveVersion      = "probe.objective-evidence.v1"
)

// probeScenarioV2EvidenceSummary makes the run relationship inspectable from
// the result line. The manifest remains the authoritative source for each
// artifact's digest and contains every listed path exactly once.
type probeScenarioV2EvidenceSummary struct {
	ScenarioID            string `json:"scenario_id"`
	ManifestPath          string `json:"manifest_path"`
	ProviderCapturePath   string `json:"provider_capture_path"`
	BrowserEventsPath     string `json:"browser_events_path,omitempty"`
	PageStatePath         string `json:"page_state_path"`
	WorkspaceSnapshotPath string `json:"workspace_snapshot_path"`
	ObjectiveEvidencePath string `json:"objective_evidence_path"`
}

type probeScenarioV2ObjectiveEvidenceArtifact struct {
	Version               string                     `json:"version"`
	ScenarioID            string                     `json:"scenario_id"`
	CheckedClaim          string                     `json:"checked_claim"`
	Verified              bool                       `json:"verified"`
	ProviderCapturePath   string                     `json:"provider_capture_path"`
	BrowserEventsPath     string                     `json:"browser_events_path,omitempty"`
	PageStatePath         string                     `json:"page_state_path"`
	WorkspaceSnapshotPath string                     `json:"workspace_snapshot_path"`
	Error                 string                     `json:"error,omitempty"`
	Divergence            *probeScenarioV2Divergence `json:"divergence,omitempty"`
}

type probeScenarioV2WorkspaceSnapshot struct {
	Version    string           `json:"version"`
	ScenarioID string           `json:"scenario_id"`
	Scenario   probe.ScenarioV2 `json:"scenario"`
}

type probeScenarioV2ObjectiveVerification struct {
	CheckedClaim string
	Verified     bool
	Error        string
	Divergence   *probeScenarioV2Divergence
}

func (e *probeScenarioV2Executor) finalizeEvidence(destination string) (probeScenarioV2EvidenceSummary, probe.ObjectiveEvidence, error) {
	if e == nil {
		return probeScenarioV2EvidenceSummary{}, probe.ObjectiveEvidence{}, errors.New("v2 executor is nil")
	}
	if strings.TrimSpace(destination) == "" {
		return probeScenarioV2EvidenceSummary{}, probe.ObjectiveEvidence{}, errors.New("v2 evidence destination is empty")
	}

	pageState, err := probeScenarioV2PageStateBytes(e)
	if err != nil {
		return probeScenarioV2EvidenceSummary{}, probe.ObjectiveEvidence{}, err
	}
	workspace, err := probeScenarioV2WorkspaceBytes(e.scenario)
	if err != nil {
		return probeScenarioV2EvidenceSummary{}, probe.ObjectiveEvidence{}, fmt.Errorf("encode workspace snapshot: %w", err)
	}
	providerCapture, err := probeScenarioV2ProviderCaptureBytes(e)
	if err != nil {
		return probeScenarioV2EvidenceSummary{}, probe.ObjectiveEvidence{}, fmt.Errorf("encode provider capture: %w", err)
	}

	var events []testkit.Event
	var browserArtifact *transcript.BrowserArtifact
	if len(e.eventOutput.Bytes()) > 0 {
		events, err = testkit.ValidateEventStream(e.eventOutput.Bytes())
		if err != nil {
			return probeScenarioV2EvidenceSummary{}, probe.ObjectiveEvidence{}, fmt.Errorf("validate browser evidence before finalization: %w", err)
		}
		canonicalEvents, marshalErr := testkit.MarshalEvents(events)
		if marshalErr != nil {
			return probeScenarioV2EvidenceSummary{}, probe.ObjectiveEvidence{}, fmt.Errorf("canonicalize browser evidence: %w", marshalErr)
		}
		browserArtifact = browserRecordingArtifact(canonicalEvents)
	} else if e.scenario.BrowserFixture != "" {
		return probeScenarioV2EvidenceSummary{}, probe.ObjectiveEvidence{}, errors.New("browser fixture completed without browser event evidence")
	}

	verification := verifyProbeScenarioV2EvidenceData(e.scenario, events, bytes.TrimSpace(pageState), e.providerCapture, browserArtifact != nil)
	objectiveArtifact := probeScenarioV2ObjectiveEvidenceArtifact{
		Version:               probeScenarioV2ObjectiveVersion,
		ScenarioID:            e.scenario.ID,
		CheckedClaim:          verification.CheckedClaim,
		Verified:              verification.Verified,
		ProviderCapturePath:   probeScenarioV2ProviderArtifactPath,
		BrowserEventsPath:     browserArtifactPath(browserArtifact),
		PageStatePath:         probeScenarioV2PageStateArtifactPath,
		WorkspaceSnapshotPath: probeScenarioV2WorkspaceArtifactPath,
		Error:                 verification.Error,
		Divergence:            verification.Divergence,
	}
	objective, err := probeScenarioV2JSONLine(objectiveArtifact)
	if err != nil {
		return probeScenarioV2EvidenceSummary{}, probe.ObjectiveEvidence{}, fmt.Errorf("encode objective evidence: %w", err)
	}

	model := e.providerCapture.Provider.Model
	if model == "" {
		model = "fixture"
	}
	config := transcript.RecordingConfig{
		Destination:      destination,
		ClientTranscript: []byte("probe.scenario.v2:" + e.scenario.ID + "\n"),
		AgentTranscript:  []byte("probe.scenario.v2 evidence\n"),
		Metadata: transcript.RecordingMetadata{
			Transport: probeScenarioV2EvidenceTransport(e),
			Model:     model,
			ClockBase: probeScenarioV2EvidenceClockBase(e),
		},
		ManifestVersion: transcript.RecordingManifestV2Version,
		AdditionalArtifacts: []transcript.RecordingArtifact{
			{Path: probeScenarioV2ProviderArtifactPath, Data: providerCapture},
			{Path: probeScenarioV2PageStateArtifactPath, Data: pageState},
			{Path: probeScenarioV2WorkspaceArtifactPath, Data: workspace},
			{Path: probeScenarioV2ObjectiveArtifactPath, Data: objective},
		},
	}
	if browserArtifact != nil {
		config.BrowserArtifact = browserArtifact
	}
	if err := transcript.WriteRecordingBundle(config); err != nil {
		return probeScenarioV2EvidenceSummary{}, probe.ObjectiveEvidence{}, fmt.Errorf("finalize v2 evidence: %w", err)
	}

	post, err := verifyProbeScenarioV2Bundle(destination, e.scenario)
	if err != nil {
		// The destination is a fresh run-scoped path. Remove it if the
		// post-commit verifier rejects the complete layout so callers cannot
		// mistake a structurally unverified bundle for acceptance evidence.
		_ = os.RemoveAll(destination)
		return probeScenarioV2EvidenceSummary{}, probe.ObjectiveEvidence{}, fmt.Errorf("verify finalized v2 evidence: %w", err)
	}
	e.objectiveDivergence = post.Divergence
	summary := probeScenarioV2EvidenceSummary{
		ScenarioID:            e.scenario.ID,
		ManifestPath:          filepath.Join(destination, "manifest.json"),
		ProviderCapturePath:   filepath.Join(destination, probeScenarioV2ProviderArtifactPath),
		BrowserEventsPath:     optionalEvidencePath(destination, browserArtifactPath(browserArtifact)),
		PageStatePath:         filepath.Join(destination, probeScenarioV2PageStateArtifactPath),
		WorkspaceSnapshotPath: filepath.Join(destination, probeScenarioV2WorkspaceArtifactPath),
		ObjectiveEvidencePath: filepath.Join(destination, probeScenarioV2ObjectiveArtifactPath),
	}
	objectiveEvidence := probe.ObjectiveEvidence{
		ArtifactPath: probeScenarioV2ObjectiveArtifactPath,
		CheckedClaim: post.CheckedClaim,
		Verified:     post.Verified,
	}
	return summary, objectiveEvidence, nil
}

func probeScenarioV2PageStateBytes(executor *probeScenarioV2Executor) ([]byte, error) {
	state := json.RawMessage(`null`)
	if executor != nil && executor.pageStateSet {
		state = executor.pageState
	} else if executor != nil && executor.runtime != nil && len(executor.runtime.PageState()) > 0 {
		state = executor.runtime.PageState()
	}
	normalized, err := testkit.JSONValue(state)
	if err != nil {
		return nil, fmt.Errorf("encode page-state oracle snapshot: %w", err)
	}
	return append(normalized, '\n'), nil
}

func probeScenarioV2WorkspaceBytes(scenario probe.ScenarioV2) ([]byte, error) {
	snapshot := probeScenarioV2WorkspaceSnapshot{
		Version:    "probe.workspace-snapshot.v1",
		ScenarioID: scenario.ID,
		Scenario:   scenario,
	}
	return probeScenarioV2JSONLine(snapshot)
}

func probeScenarioV2ProviderCaptureBytes(executor *probeScenarioV2Executor) ([]byte, error) {
	capture := executor.providerCapture
	if executor.providerPath == "" {
		capture = gatewaytesting.SessionCapture{
			Version:  gatewaytesting.SessionCaptureVersion,
			Provider: gatewaytesting.SessionProviderMetadata{Name: "probe", Model: "fixture"},
			Session: gatewaytesting.SessionMetadata{
				ID:                executor.scenario.ID,
				FixtureProvenance: gatewaytesting.SessionFixtureProvenanceSynthetic,
			},
			Records: []gatewaytesting.CapturedSessionEvent{},
		}
	}
	if capture.Records == nil {
		capture.Records = []gatewaytesting.CapturedSessionEvent{}
	}
	encoded, err := json.Marshal(capture)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func probeScenarioV2JSONLine(value any) ([]byte, error) {
	encoded, err := testkit.JSONValue(value)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func browserRecordingArtifact(data []byte) *transcript.BrowserArtifact {
	digest := sha256.Sum256(data)
	return &transcript.BrowserArtifact{
		Format: transcript.BrowserEventsVersion,
		Path:   transcript.BrowserArtifactDefaultPath,
		Data:   append([]byte(nil), data...),
		SHA256: hex.EncodeToString(digest[:]),
		Redaction: transcript.BrowserRedactionPolicy{
			URLQuery:    true,
			URLFragment: true,
			RawCDP:      false,
		},
	}
}

func browserArtifactPath(artifact *transcript.BrowserArtifact) string {
	if artifact == nil {
		return ""
	}
	return artifact.Path
}

func optionalEvidencePath(destination, relative string) string {
	if relative == "" {
		return ""
	}
	return filepath.Join(destination, relative)
}

func verifyProbeScenarioV2Bundle(destination string, expected probe.ScenarioV2) (probeScenarioV2ObjectiveVerification, error) {
	manifestBytes, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		return probeScenarioV2ObjectiveVerification{}, fmt.Errorf("read manifest: %w", err)
	}
	var manifest transcript.RecordingManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return probeScenarioV2ObjectiveVerification{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return probeScenarioV2ObjectiveVerification{}, fmt.Errorf("validate manifest: %w", err)
	}
	if manifest.FormatVersion != transcript.RecordingManifestV2Version {
		return probeScenarioV2ObjectiveVerification{}, fmt.Errorf("want recording manifest v%d, got %d", transcript.RecordingManifestV2Version, manifest.FormatVersion)
	}
	hashes := make(map[string]string, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		if _, exists := hashes[artifact.Path]; exists {
			return probeScenarioV2ObjectiveVerification{}, fmt.Errorf("duplicate artifact %q", artifact.Path)
		}
		data, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(artifact.Path)))
		if err != nil {
			return probeScenarioV2ObjectiveVerification{}, fmt.Errorf("read artifact %q: %w", artifact.Path, err)
		}
		digest := sha256.Sum256(data)
		got := hex.EncodeToString(digest[:])
		if got != artifact.SHA256 {
			return probeScenarioV2ObjectiveVerification{}, fmt.Errorf("hash mismatch for artifact %q", artifact.Path)
		}
		hashes[artifact.Path] = artifact.SHA256
	}
	for _, path := range []string{
		probeScenarioV2ProviderArtifactPath,
		probeScenarioV2PageStateArtifactPath,
		probeScenarioV2WorkspaceArtifactPath,
		probeScenarioV2ObjectiveArtifactPath,
	} {
		if _, ok := hashes[path]; !ok {
			return probeScenarioV2ObjectiveVerification{}, fmt.Errorf("required evidence artifact %q is not listed", path)
		}
	}

	providerData, err := readProbeScenarioV2Artifact(destination, probeScenarioV2ProviderArtifactPath)
	if err != nil {
		return probeScenarioV2ObjectiveVerification{}, err
	}
	var capture gatewaytesting.SessionCapture
	if err := json.Unmarshal(providerData, &capture); err != nil {
		return probeScenarioV2ObjectiveVerification{}, fmt.Errorf("decode provider capture: %w", err)
	}
	if validationErrs := gatewaytesting.ValidateSessionCapture(probeScenarioV2ProviderArtifactPath, capture); len(validationErrs) > 0 {
		return probeScenarioV2ObjectiveVerification{}, fmt.Errorf("validate provider capture: %s", joinSessionFixtureErrors(validationErrs))
	}

	pageStateData, err := readProbeScenarioV2Artifact(destination, probeScenarioV2PageStateArtifactPath)
	if err != nil {
		return probeScenarioV2ObjectiveVerification{}, err
	}
	pageState, err := testkit.JSONValue(json.RawMessage(pageStateData))
	if err != nil {
		return probeScenarioV2ObjectiveVerification{}, fmt.Errorf("validate page-state oracle snapshot: %w", err)
	}

	workspaceData, err := readProbeScenarioV2Artifact(destination, probeScenarioV2WorkspaceArtifactPath)
	if err != nil {
		return probeScenarioV2ObjectiveVerification{}, err
	}
	var workspace probeScenarioV2WorkspaceSnapshot
	if err := json.Unmarshal(workspaceData, &workspace); err != nil {
		return probeScenarioV2ObjectiveVerification{}, fmt.Errorf("decode workspace snapshot: %w", err)
	}
	if workspace.Version != "probe.workspace-snapshot.v1" || workspace.ScenarioID == "" || workspace.ScenarioID != expected.ID || workspace.Scenario.ID != expected.ID || workspace.Scenario.SchemaVersion != expected.SchemaVersion {
		return probeScenarioV2ObjectiveVerification{}, errors.New("workspace snapshot does not identify the executed scenario")
	}

	var events []testkit.Event
	hasBrowserArtifact := manifest.Browser != nil
	if expected.BrowserFixture != "" {
		if !hasBrowserArtifact {
			return probeScenarioV2ObjectiveVerification{}, errors.New("browser evidence is missing from the v2 manifest")
		}
		if manifest.Browser.Artifact.Path != transcript.BrowserArtifactDefaultPath {
			return probeScenarioV2ObjectiveVerification{}, errors.New("browser evidence path is not the canonical artifact")
		}
		browserData, readErr := readProbeScenarioV2Artifact(destination, manifest.Browser.Artifact.Path)
		if readErr != nil {
			return probeScenarioV2ObjectiveVerification{}, readErr
		}
		events, err = testkit.ValidateEventStream(browserData)
		if err != nil {
			return probeScenarioV2ObjectiveVerification{}, fmt.Errorf("validate persisted browser events: %w", err)
		}
	} else if hasBrowserArtifact {
		return probeScenarioV2ObjectiveVerification{}, errors.New("provider-only v2 recording unexpectedly contains browser evidence")
	}

	objectiveData, err := readProbeScenarioV2Artifact(destination, probeScenarioV2ObjectiveArtifactPath)
	if err != nil {
		return probeScenarioV2ObjectiveVerification{}, err
	}
	var objective probeScenarioV2ObjectiveEvidenceArtifact
	if err := json.Unmarshal(objectiveData, &objective); err != nil {
		return probeScenarioV2ObjectiveVerification{}, fmt.Errorf("decode objective evidence: %w", err)
	}
	if objective.Version != probeScenarioV2ObjectiveVersion || objective.ScenarioID != expected.ID || objective.ProviderCapturePath != probeScenarioV2ProviderArtifactPath || objective.PageStatePath != probeScenarioV2PageStateArtifactPath || objective.WorkspaceSnapshotPath != probeScenarioV2WorkspaceArtifactPath || objective.BrowserEventsPath != browserArtifactPath(manifestBrowserArtifact(manifest)) {
		return probeScenarioV2ObjectiveVerification{}, errors.New("objective evidence references a different run or artifact set")
	}
	computed := verifyProbeScenarioV2EvidenceData(workspace.Scenario, events, pageState, capture, hasBrowserArtifact)
	if objective.CheckedClaim != computed.CheckedClaim || objective.Verified != computed.Verified || objective.Error != computed.Error || !probeScenarioV2DivergenceEqual(objective.Divergence, computed.Divergence) {
		return probeScenarioV2ObjectiveVerification{}, errors.New("objective evidence is not reproducible from captured artifacts")
	}
	return computed, nil
}

func readProbeScenarioV2Artifact(destination, relative string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(relative)))
	if err != nil {
		return nil, fmt.Errorf("read evidence artifact %q: %w", relative, err)
	}
	return data, nil
}

func manifestBrowserArtifact(manifest transcript.RecordingManifest) *transcript.BrowserArtifact {
	if manifest.Browser == nil {
		return nil
	}
	return &transcript.BrowserArtifact{
		Format: manifest.Browser.Format,
		Path:   manifest.Browser.Artifact.Path,
		SHA256: manifest.Browser.Artifact.SHA256,
		Redaction: transcript.BrowserRedactionPolicy{
			URLQuery:           manifest.Browser.Redaction.URLQuery,
			URLFragment:        manifest.Browser.Redaction.URLFragment,
			ToolArguments:      append([]string(nil), manifest.Browser.Redaction.ToolArguments...),
			ResultJSONPointers: append([]string(nil), manifest.Browser.Redaction.ResultJSONPointers...),
			DigestTools:        append([]string(nil), manifest.Browser.Redaction.DigestTools...),
			RawCDP:             manifest.Browser.Redaction.RawCDP,
		},
	}
}

type probeScenarioV2PersistedBrowserEvidence struct {
	browserCount      int64
	browserCountSet   bool
	browserCountPos   int
	targets           map[string]probeScenarioV2PersistedTarget
	selectedTarget    string
	selectedTargetPos int
	catalog           map[string]probeScenarioV2PersistedTool
	catalogByRef      map[string]string
	catalogGeneration int64
	catalogSet        bool
	catalogPos        int
	invocations       []*probeScenarioV2PersistedInvocation
	invocationByID    map[string]*probeScenarioV2PersistedInvocation
	stale             bool
	stalePos          int
	staleToolRef      string
	canceled          bool
	canceledPos       int
	closed            bool
	closedPos         int
	approvalRequested bool
	approvalPos       int
	operations        []probeScenarioV2ObservedOperation
	methods           []probeScenarioV2ObservedOperation
}

type probeScenarioV2PersistedTarget struct {
	Origin   string
	Eligible bool
	Position int
}

type probeScenarioV2PersistedTool struct {
	Name        string
	Ref         string
	InputSchema json.RawMessage
	Position    int
}

type probeScenarioV2PersistedInvocation struct {
	ID            string
	Name          string
	ToolRef       string
	Input         json.RawMessage
	Output        json.RawMessage
	State         string
	ErrorCode     string
	Terminal      bool
	CreatedPos    int
	DispatchedPos int
	TerminalPos   int
	Approval      bool
	ApprovalPos   int
}

type probeScenarioV2ObservedOperation struct {
	Name              string
	OperationPosition int
	EventPosition     int
}

func verifyProbeScenarioV2EvidenceData(
	scenario probe.ScenarioV2,
	events []testkit.Event,
	pageState json.RawMessage,
	capture gatewaytesting.SessionCapture,
	hasBrowserArtifact bool,
) probeScenarioV2ObjectiveVerification {
	verification := probeScenarioV2ObjectiveVerification{
		CheckedClaim: probeScenarioV2CheckedClaim(scenario),
		Verified:     true,
	}
	hasBrowserObjectives := false
	for index, expectation := range scenario.Expectations {
		if !isProbeScenarioV2BrowserObjective(expectation.Type) {
			continue
		}
		hasBrowserObjectives = true
		if !hasBrowserArtifact || len(events) == 0 {
			return failedProbeScenarioV2Objective(scenario, index, expectation, probeScenarioV2ObservationCheck{
				Expected:         "captured objective evidence",
				Actual:           "missing browser event artifact",
				EvidenceArtifact: probeScenarioV2ExpectationArtifact(expectation.Type),
			})
		}
	}
	if hasBrowserObjectives {
		evidence := indexProbeScenarioV2BrowserEvents(events)
		for index, expectation := range scenario.Expectations {
			if !isProbeScenarioV2BrowserObjective(expectation.Type) {
				continue
			}
			check := probeScenarioV2BrowserObjectiveCheck(evidence, pageState, expectation)
			if !check.Passed {
				return failedProbeScenarioV2Objective(scenario, index, expectation, check)
			}
		}
	}
	for index, expectation := range scenario.Expectations {
		if !isProbeScenarioV2ProviderObjective(expectation.Type) {
			continue
		}
		check := probeScenarioV2ProviderObjectiveCheck(capture, expectation)
		if !check.Passed {
			return failedProbeScenarioV2Objective(scenario, index, expectation, check)
		}
	}
	return verification
}

func probeScenarioV2CheckedClaim(scenario probe.ScenarioV2) string {
	for _, expectation := range scenario.Expectations {
		if expectation.Type == probe.ScenarioV2ExpectationPageStateEquals {
			return string(probe.ScenarioV2ExpectationPageStateEquals)
		}
	}
	for _, expectation := range scenario.Expectations {
		if isProbeScenarioV2BrowserObjective(expectation.Type) {
			return "browser objectives"
		}
	}
	for _, expectation := range scenario.Expectations {
		if isProbeScenarioV2ProviderObjective(expectation.Type) {
			return "provider objectives"
		}
	}
	return "captured probe artifacts"
}

func failedProbeScenarioV2Objective(
	scenario probe.ScenarioV2,
	index int,
	expectation probe.ScenarioV2Expectation,
	check probeScenarioV2ObservationCheck,
) probeScenarioV2ObjectiveVerification {
	divergence := makeProbeScenarioV2Divergence(scenario, index, expectation, check)
	return probeScenarioV2ObjectiveVerification{
		CheckedClaim: probeScenarioV2CheckedClaim(scenario),
		Verified:     false,
		Error:        divergence.Error(),
		Divergence:   divergence,
	}
}

func isProbeScenarioV2BrowserObjective(kind probe.ScenarioV2ExpectationType) bool {
	switch kind {
	case probe.ScenarioV2ExpectationBrowserCountEquals,
		probe.ScenarioV2ExpectationEligibleTabCountEquals,
		probe.ScenarioV2ExpectationSelectedTabEquals,
		probe.ScenarioV2ExpectationSelectedOriginEquals,
		probe.ScenarioV2ExpectationCatalogGenerationEquals,
		probe.ScenarioV2ExpectationToolCatalogContains,
		probe.ScenarioV2ExpectationToolCatalogNotContains,
		probe.ScenarioV2ExpectationToolSchemaEquals,
		probe.ScenarioV2ExpectationToolInvocationCount,
		probe.ScenarioV2ExpectationToolInputJSONEquals,
		probe.ScenarioV2ExpectationToolResultJSONPathEquals,
		probe.ScenarioV2ExpectationToolStatusEquals,
		probe.ScenarioV2ExpectationChromeOperationOrder,
		probe.ScenarioV2ExpectationNoUnexpectedChromeOperations,
		probe.ScenarioV2ExpectationGeneratedCDPMethodOrder,
		probe.ScenarioV2ExpectationNoUnexpectedGeneratedCDPMethods,
		probe.ScenarioV2ExpectationNoPendingInvocations,
		probe.ScenarioV2ExpectationPageStateEquals,
		probe.ScenarioV2ExpectationResponseCanceled,
		probe.ScenarioV2ExpectationApprovalRequested,
		probe.ScenarioV2ExpectationApprovalNotRequested,
		probe.ScenarioV2ExpectationStaleToolRejected,
		probe.ScenarioV2ExpectationBrowserConnectionClosed:
		return true
	default:
		return false
	}
}

func indexProbeScenarioV2BrowserEvents(events []testkit.Event) probeScenarioV2PersistedBrowserEvidence {
	evidence := probeScenarioV2PersistedBrowserEvidence{
		targets:        make(map[string]probeScenarioV2PersistedTarget),
		catalog:        make(map[string]probeScenarioV2PersistedTool),
		catalogByRef:   make(map[string]string),
		invocationByID: make(map[string]*probeScenarioV2PersistedInvocation),
	}
	for index, event := range events {
		position := int(event.Sequence)
		if position <= 0 {
			position = index + 1
		}
		fields, ok := probeScenarioV2EventFields(event)
		switch event.Type {
		case testkit.EventBrowserDiscoveryStarted:
			appendProbeScenarioV2ObservedOperation(&evidence.operations, "connect", position)
		case testkit.EventBrowserDiscoveryCompleted:
			appendProbeScenarioV2ObservedOperation(&evidence.operations, "discover", position)
			if !ok {
				continue
			}
			if count, ok := probeScenarioV2PayloadInt(fields, "candidate_count"); ok {
				evidence.browserCount = count
				evidence.browserCountSet = true
				evidence.browserCountPos = position
			}
		case testkit.EventBrowserTargetsSnapshot:
			if !ok {
				continue
			}
			var targets []map[string]json.RawMessage
			if raw := fields["targets"]; json.Unmarshal(raw, &targets) == nil {
				for _, target := range targets {
					id, idOK := probeScenarioV2PayloadString(target, "id")
					if !idOK || id == "" {
						continue
					}
					eligible, _ := probeScenarioV2PayloadBool(target, "eligible")
					origin, _ := probeScenarioV2PayloadString(target, "origin")
					key := string(event.BrowserID) + "\x00" + id
					evidence.targets[key] = probeScenarioV2PersistedTarget{Origin: origin, Eligible: eligible, Position: position}
				}
			}
		case testkit.EventBrowserTargetSelected:
			evidence.selectedTarget = string(event.TargetID)
			evidence.selectedTargetPos = position
			appendProbeScenarioV2ObservedOperation(&evidence.operations, "select", position)
		case testkit.EventBrowserCatalogToolAdded:
			if !ok {
				continue
			}
			var tools []map[string]json.RawMessage
			if raw := fields["tools"]; json.Unmarshal(raw, &tools) == nil {
				for _, rawTool := range tools {
					name, nameOK := probeScenarioV2PayloadString(rawTool, "name")
					if !nameOK || name == "" {
						continue
					}
					ref, _ := probeScenarioV2PayloadString(rawTool, "ref")
					tool := probeScenarioV2PersistedTool{Name: name, Ref: ref, InputSchema: append(json.RawMessage(nil), rawTool["input_schema"]...), Position: position}
					evidence.catalog[name] = tool
					if ref != "" {
						evidence.catalogByRef[ref] = name
					}
				}
			}
		case testkit.EventBrowserCatalogToolRemoved:
			var refs []string
			if raw := fields["tool_refs"]; json.Unmarshal(raw, &refs) == nil {
				for _, ref := range refs {
					if name := evidence.catalogByRef[ref]; name != "" {
						delete(evidence.catalog, name)
						delete(evidence.catalogByRef, ref)
					}
				}
			}
		case testkit.EventBrowserCatalogReady:
			evidence.catalogGeneration = int64(event.Generation)
			evidence.catalogSet = true
			evidence.catalogPos = position
			appendProbeScenarioV2ObservedOperation(&evidence.operations, "list_tools", position)
		case testkit.EventBrowserInvocationCreated:
			appendProbeScenarioV2ObservedOperation(&evidence.operations, "invoke", position)
			if !ok {
				continue
			}
			id, idOK := probeScenarioV2PayloadString(fields, "invocation_id")
			if !idOK || id == "" {
				continue
			}
			invocation := evidence.invocationByID[id]
			if invocation == nil {
				invocation = &probeScenarioV2PersistedInvocation{ID: id}
				evidence.invocations = append(evidence.invocations, invocation)
				evidence.invocationByID[id] = invocation
			}
			invocation.Name, _ = probeScenarioV2PayloadString(fields, "tool_name")
			invocation.ToolRef, _ = probeScenarioV2PayloadString(fields, "tool_ref")
			invocation.State = string(webmcp.InvocationCreated)
			invocation.CreatedPos = position
		case testkit.EventBrowserInvocationDispatched:
			appendProbeScenarioV2ObservedOperation(&evidence.methods, "WebMCP.invokeTool", position)
			if !ok {
				continue
			}
			if invocation := evidence.invocationForEvent(fields); invocation != nil {
				invocation.Input = append(json.RawMessage(nil), fields["input"]...)
				invocation.State = string(webmcp.InvocationDispatched)
				invocation.DispatchedPos = position
			}
		case testkit.EventBrowserInvocationApproval:
			if !ok {
				continue
			}
			evidence.approvalRequested = true
			evidence.approvalPos = position
			if invocation := evidence.invocationForEvent(fields); invocation != nil {
				invocation.Approval = true
				invocation.ApprovalPos = position
			}
		case testkit.EventBrowserInvocationCompleted:
			if !ok {
				continue
			}
			if invocation := evidence.invocationForEvent(fields); invocation != nil {
				invocation.Output = append(json.RawMessage(nil), fields["output"]...)
				invocation.State, _ = probeScenarioV2PayloadString(fields, "status")
				invocation.Terminal = true
				invocation.TerminalPos = position
			}
		case testkit.EventBrowserInvocationError:
			if !ok {
				continue
			}
			if code, ok := probeScenarioV2PayloadString(fields, "code"); ok && code == string(webmcp.ErrorStaleToolRef) {
				evidence.stale = true
				evidence.stalePos = position
				evidence.staleToolRef, _ = probeScenarioV2PayloadString(fields, "tool_ref")
			}
			if invocation := evidence.invocationForEvent(fields); invocation != nil {
				invocation.ErrorCode, _ = probeScenarioV2PayloadString(fields, "code")
				invocation.State = string(webmcp.InvocationError)
				invocation.Terminal = true
				invocation.TerminalPos = position
			}
		case testkit.EventBrowserInvocationCanceled:
			evidence.canceled = true
			evidence.canceledPos = position
			if !ok {
				continue
			}
			if invocation := evidence.invocationForEvent(fields); invocation != nil {
				invocation.State = string(webmcp.InvocationCanceled)
				invocation.Terminal = true
				invocation.TerminalPos = position
			}
		case testkit.EventBrowserInvocationCancel:
			appendProbeScenarioV2ObservedOperation(&evidence.operations, "cancel", position)
			appendProbeScenarioV2ObservedOperation(&evidence.methods, "WebMCP.cancelInvocation", position)
		case testkit.EventBrowserWebMCPEnabled:
			appendProbeScenarioV2ObservedOperation(&evidence.methods, "WebMCP.enable", position)
		case testkit.EventBrowserPageGenerationChanged:
			appendProbeScenarioV2ObservedOperation(&evidence.operations, "navigate", position)
		case testkit.EventBrowserChromeTargetAttached:
			appendProbeScenarioV2ObservedOperation(&evidence.operations, "attach", position)
		case testkit.EventBrowserTargetDetached:
			appendProbeScenarioV2ObservedOperation(&evidence.operations, "detach", position)
		case testkit.EventBrowserChromeTargetClosed:
			evidence.closed = true
			evidence.closedPos = position
			appendProbeScenarioV2ObservedOperation(&evidence.operations, "close", position)
		}
	}
	for _, invocation := range evidence.invocations {
		if invocation.Name == "" && invocation.ToolRef != "" {
			invocation.Name = evidence.catalogByRef[invocation.ToolRef]
		}
	}
	return evidence
}

func appendProbeScenarioV2ObservedOperation(operations *[]probeScenarioV2ObservedOperation, name string, position int) {
	if operations == nil || name == "" {
		return
	}
	*operations = append(*operations, probeScenarioV2ObservedOperation{
		Name:              name,
		OperationPosition: len(*operations) + 1,
		EventPosition:     position,
	})
}

func (e probeScenarioV2PersistedBrowserEvidence) invocationForEvent(fields map[string]json.RawMessage) *probeScenarioV2PersistedInvocation {
	id, ok := probeScenarioV2PayloadString(fields, "invocation_id")
	if !ok {
		return nil
	}
	return e.invocationByID[id]
}

func probeScenarioV2EventFields(event testkit.Event) (map[string]json.RawMessage, bool) {
	if len(event.Payload) == 0 || bytes.TrimSpace(event.Payload)[0] != '{' {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(event.Payload, &fields); err != nil {
		return nil, false
	}
	return fields, true
}

func probeScenarioV2PayloadString(fields map[string]json.RawMessage, key string) (string, bool) {
	var value string
	if err := json.Unmarshal(fields[key], &value); err != nil {
		return "", false
	}
	return value, true
}

func probeScenarioV2PayloadBool(fields map[string]json.RawMessage, key string) (bool, bool) {
	var value bool
	if err := json.Unmarshal(fields[key], &value); err != nil {
		return false, false
	}
	return value, true
}

func probeScenarioV2PayloadInt(fields map[string]json.RawMessage, key string) (int64, bool) {
	decoder := json.NewDecoder(bytes.NewReader(fields[key]))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return 0, false
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := number.Int64()
	return parsed, err == nil
}

func (c *ProbeRunCommand) prepareProbeScenarioV2RecordingRoot(count int) (string, error) {
	if c == nil {
		return "", errors.New("probe run command is nil")
	}
	root := strings.TrimSpace(c.RecordingRoot)
	if root == "" {
		created, err := os.MkdirTemp("", "go-agent-probe-v2-evidence-")
		if err != nil {
			return "", fmt.Errorf("create v2 evidence root for %d scenarios: %w", count, err)
		}
		return created, nil
	}
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve v2 evidence root %q: %w", c.RecordingRoot, err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create v2 evidence root %q: %w", root, err)
	}
	return root, nil
}

func probeScenarioV2RecordingDirectory(root string, index int, entry probeScenarioV2Selection) string {
	if strings.TrimSpace(root) == "" {
		return ""
	}
	name := entry.Scenario.ID
	if name == "" {
		name = entry.Selection
	}
	name = probeScenarioV2PathSlug(name)
	if name == "" {
		name = "scenario"
	}
	return filepath.Join(root, fmt.Sprintf("%03d-%s", index+1, name))
}

func probeScenarioV2PathSlug(value string) string {
	var builder strings.Builder
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' {
			builder.WriteRune(character)
			continue
		}
		builder.WriteByte('_')
	}
	return strings.Trim(builder.String(), ".")
}

func (e *probeScenarioV2Executor) recordBrowserEvent(eventType testkit.EventType, browserID webmcp.BrowserID, targetID webmcp.TargetID, generation uint64, payload any) error {
	if e == nil || e.recorder == nil {
		return errors.New("browser evidence recorder is unavailable")
	}
	input, err := testkit.NewEventInput(eventType, payload)
	if err != nil {
		return fmt.Errorf("encode browser event %s: %w", eventType, err)
	}
	input.BrowserID = string(browserID)
	input.TargetID = string(targetID)
	input.Generation = generation
	if _, err := e.recorder.Record(input); err != nil {
		return fmt.Errorf("record browser event %s: %w", eventType, err)
	}
	return nil
}

func (e *probeScenarioV2Executor) recordDiscoveryStarted() error {
	return e.recordBrowserEvent(testkit.EventBrowserDiscoveryStarted, "", "", 0, map[string]any{
		"source": e.browserEvidenceSource(),
		"mode":   e.browserEvidenceMode(),
	})
}

func (e *probeScenarioV2Executor) recordDiscoveryEvidence(ctx context.Context) error {
	if err := e.recordBrowserEvent(testkit.EventBrowserDiscoveryCompleted, discoveryBrowserID(e.discovered), "", 0, map[string]any{
		"candidate_count": len(e.discovered),
		"candidates":      discoveryCandidateEvidence(e.discovered),
		"source":          e.browserEvidenceSource(),
	}); err != nil {
		return err
	}
	for _, candidate := range e.discovered {
		if err := e.recordBrowserEvent(testkit.EventBrowserEndpointVersion, candidate.ID, "", 0, map[string]any{
			"browser":                candidate.Product,
			"protocol_version":       candidate.Protocol,
			"websocket_debugger_url": safeEvidenceURL(candidate.BrowserWSURL),
		}); err != nil {
			return err
		}
		targets, err := e.broker.ListTargets(ctx, webmcp.BrowserSelector{BrowserID: candidate.ID})
		if err != nil {
			return fmt.Errorf("record targets for browser %q: %w", candidate.ID, err)
		}
		if err := e.recordBrowserEvent(testkit.EventBrowserTargetsSnapshot, candidate.ID, "", 0, map[string]any{
			"target_count": len(targets),
			"targets":      targetEvidence(targets),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (e *probeScenarioV2Executor) browserEvidenceMode() string {
	if e != nil && e.mode == ProbeScenarioV2BrowserExecutorReal {
		return string(ProbeScenarioV2BrowserExecutorReal)
	}
	return string(ProbeScenarioV2BrowserExecutorHermetic)
}

func (e *probeScenarioV2Executor) browserEvidenceSource() string {
	if e != nil && e.mode == ProbeScenarioV2BrowserExecutorReal {
		return "webmcp-browser-adapter"
	}
	return "browser-script"
}

func probeScenarioV2EvidenceTransport(e *probeScenarioV2Executor) string {
	if e != nil && e.mode == ProbeScenarioV2BrowserExecutorReal {
		return "browser"
	}
	return "replay"
}

func probeScenarioV2EvidenceClockBase(e *probeScenarioV2Executor) string {
	if e != nil && e.mode == ProbeScenarioV2BrowserExecutorReal {
		return "runtime"
	}
	return "fake:0"
}

func discoveryBrowserID(candidates []webmcp.BrowserCandidate) webmcp.BrowserID {
	if len(candidates) == 1 {
		return candidates[0].ID
	}
	return ""
}

func discoveryCandidateEvidence(candidates []webmcp.BrowserCandidate) []map[string]any {
	result := make([]map[string]any, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, map[string]any{
			"id":            string(candidate.ID),
			"source":        string(candidate.Source),
			"product":       candidate.Product,
			"protocol":      candidate.Protocol,
			"loopback":      candidate.Loopback,
			"explicit":      candidate.Explicit,
			"harness_owned": candidate.HarnessOwned,
		})
	}
	return result
}

func targetEvidence(targets []webmcp.Target) []map[string]any {
	result := make([]map[string]any, 0, len(targets))
	for _, target := range targets {
		result = append(result, map[string]any{
			"id":         string(target.ID),
			"type":       target.Type,
			"title":      target.Title,
			"url":        safeEvidenceURL(target.URL),
			"origin":     safeEvidenceURL(target.Origin),
			"generation": target.Generation,
			"eligible":   target.Eligible,
		})
	}
	return result
}

func safeEvidenceURL(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}

func (e *probeScenarioV2Executor) recordSelectionEvidence(page webmcp.PageContext, reason string) error {
	browserID := page.Key.BrowserID
	targetID := page.Key.TargetID
	if err := e.recordBrowserEvent(testkit.EventBrowserTargetSelected, browserID, targetID, 0, map[string]any{
		"generation": page.Generation,
		"reason":     reason,
	}); err != nil {
		return err
	}
	if err := e.recordBrowserEvent(testkit.EventBrowserChromeTargetAttached, browserID, targetID, 0, map[string]any{
		"phase":     "attached",
		"ownership": "harness",
		"reason":    reason,
	}); err != nil {
		return err
	}
	return e.recordBrowserEvent(testkit.EventBrowserWebMCPEnabled, browserID, targetID, page.Generation, map[string]any{
		"enabled":    true,
		"capability": "webmcp",
		"status":     "ready",
	})
}

func (e *probeScenarioV2Executor) recordCatalogEvidence(catalog webmcp.ToolCatalogSnapshot) error {
	tools := make([]map[string]any, 0, len(catalog.Tools))
	for _, tool := range catalog.Tools {
		tools = append(tools, toolEvidence(tool))
	}
	if len(tools) > 0 {
		if err := e.recordBrowserEvent(testkit.EventBrowserCatalogToolAdded, catalog.Context.Key.BrowserID, catalog.Context.Key.TargetID, catalog.Generation, map[string]any{
			"tools":      tools,
			"tool_count": len(tools),
		}); err != nil {
			return err
		}
	}
	digest := sha256.New()
	for _, tool := range catalog.Tools {
		digest.Write([]byte(tool.Name))
		digest.Write([]byte{0})
		digest.Write(bytes.TrimSpace(tool.InputSchema))
		digest.Write([]byte{0})
	}
	return e.recordBrowserEvent(testkit.EventBrowserCatalogReady, catalog.Context.Key.BrowserID, catalog.Context.Key.TargetID, catalog.Generation, map[string]any{
		"tool_count":    len(catalog.Tools),
		"schema_digest": hex.EncodeToString(digest.Sum(nil)),
	})
}

func toolEvidence(tool webmcp.ToolDescriptor) map[string]any {
	evidence := map[string]any{
		"ref":           string(tool.Ref),
		"name":          tool.Name,
		"description":   tool.Description,
		"input_schema":  json.RawMessage(append([]byte(nil), tool.InputSchema...)),
		"frame_id":      string(tool.FrameID),
		"generation":    tool.Generation,
		"origin":        safeEvidenceURL(tool.Origin),
		"schema_digest": tool.SchemaDigest,
	}
	if len(tool.Annotations.Raw) > 0 {
		evidence["annotations"] = json.RawMessage(append([]byte(nil), tool.Annotations.Raw...))
	}
	return evidence
}

func (e *probeScenarioV2Executor) recordInvocationAdmission(invocation probeScenarioV2Invocation) error {
	browserID := e.selected.Key.BrowserID
	targetID := e.selected.Key.TargetID
	generation := e.selected.Generation
	if invocation.PublicID == "" {
		fields := map[string]any{"code": probeScenarioV2ErrorCode(invocation.Err)}
		if invocation.ToolRef != "" {
			fields["tool_ref"] = string(invocation.ToolRef)
		}
		if invocation.Name != "" {
			fields["tool_name"] = invocation.Name
		}
		return e.recordBrowserEvent(testkit.EventBrowserInvocationError, browserID, targetID, generation, fields)
	}
	fields := map[string]any{
		"invocation_id": string(invocation.PublicID),
		"tool_ref":      string(invocation.ToolRef),
	}
	if invocation.Name != "" {
		fields["tool_name"] = invocation.Name
	}
	if descriptor, ok := e.toolForRef(string(invocation.ToolRef)); ok && descriptor.FrameID != "" {
		fields["frame_id"] = string(descriptor.FrameID)
	}
	if err := e.recordBrowserEvent(testkit.EventBrowserInvocationCreated, browserID, targetID, generation, fields); err != nil {
		return err
	}
	if invocation.Err != nil {
		return e.recordInvocationError(browserID, targetID, generation, string(invocation.PublicID), invocation.Err)
	}
	return e.recordBrowserEvent(testkit.EventBrowserInvocationDispatched, browserID, targetID, generation, map[string]any{
		"invocation_id": string(invocation.PublicID),
		"tool_ref":      string(invocation.ToolRef),
		"input":         json.RawMessage(append([]byte(nil), invocation.Input...)),
	})
}

func (e *probeScenarioV2Executor) recordInvocationError(browserID webmcp.BrowserID, targetID webmcp.TargetID, generation uint64, invocationID string, err error) error {
	fields := map[string]any{"code": probeScenarioV2ErrorCode(err)}
	if invocationID != "" {
		fields["invocation_id"] = invocationID
	}
	return e.recordBrowserEvent(testkit.EventBrowserInvocationError, browserID, targetID, generation, fields)
}

func probeScenarioV2ErrorCode(err error) string {
	if err == nil {
		return "invocation_error"
	}
	var executorErr *ProbeScenarioV2BrowserExecutorError
	if errors.As(err, &executorErr) && executorErr != nil {
		return string(executorErr.Code)
	}
	var classified *webmcp.ClassifiedError
	if errors.As(err, &classified) && classified != nil && classified.Code != "" {
		return string(classified.Code)
	}
	if errors.Is(err, webmcp.ErrStaleToolRef) {
		return string(webmcp.ErrorStaleToolRef)
	}
	if errors.Is(err, webmcp.ErrInvocationNotFound) {
		return "invocation_not_found"
	}
	return "invocation_error"
}

func (e *probeScenarioV2Executor) recordInvocationTerminal(invocation probeScenarioV2Invocation) error {
	if invocation.PublicID == "" {
		return nil
	}
	browserID := e.selected.Key.BrowserID
	targetID := e.selected.Key.TargetID
	generation := e.selected.Generation
	if invocation.Err != nil {
		return e.recordInvocationError(browserID, targetID, generation, string(invocation.PublicID), invocation.Err)
	}
	switch invocation.Result.State {
	case webmcp.InvocationCanceled, webmcp.InvocationTimedOut:
		return e.recordBrowserEvent(testkit.EventBrowserInvocationCanceled, browserID, targetID, generation, map[string]any{
			"invocation_id": string(invocation.PublicID),
			"source":        "browser",
			"reason":        string(invocation.Result.State),
		})
	case webmcp.InvocationError, webmcp.InvocationOrphaned, webmcp.InvocationPolicyDenied:
		return e.recordInvocationError(browserID, targetID, generation, string(invocation.PublicID), errors.New(string(invocation.Result.ErrorCode)))
	default:
		return e.recordBrowserEvent(testkit.EventBrowserInvocationCompleted, browserID, targetID, generation, map[string]any{
			"invocation_id": string(invocation.PublicID),
			"status":        string(invocation.Result.State),
			"output":        json.RawMessage(append([]byte(nil), invocation.Result.Output...)),
		})
	}
}

func (e *probeScenarioV2Executor) recordInvocationCancel(step probe.ScenarioV2Step) error {
	return e.recordBrowserEvent(testkit.EventBrowserInvocationCancel, e.selected.Key.BrowserID, e.selected.Key.TargetID, e.selected.Generation, map[string]any{
		"invocation_id": string(step.InvocationID),
		"source":        "scenario",
		"reason":        step.Reason,
	})
}

func (e *probeScenarioV2Executor) recordGenerationChange(previous, current uint64) error {
	return e.recordBrowserEvent(testkit.EventBrowserPageGenerationChanged, e.selected.Key.BrowserID, e.selected.Key.TargetID, 0, map[string]any{
		"previous_generation": previous,
		"current_generation":  current,
		"reason":              "fixture_navigation",
	})
}

func (e *probeScenarioV2Executor) recordCleanupEvidence() error {
	if e == nil || e.selected.Key.BrowserID == "" || e.selected.Key.TargetID == "" {
		return nil
	}
	if err := e.recordBrowserEvent(testkit.EventBrowserTargetDetached, e.selected.Key.BrowserID, e.selected.Key.TargetID, 0, map[string]any{
		"reason":    "broker_close",
		"ownership": "harness",
	}); err != nil {
		return err
	}
	return e.recordBrowserEvent(testkit.EventBrowserChromeTargetClosed, e.selected.Key.BrowserID, e.selected.Key.TargetID, 0, map[string]any{
		"reason":    "broker_close",
		"ownership": "harness",
	})
}
