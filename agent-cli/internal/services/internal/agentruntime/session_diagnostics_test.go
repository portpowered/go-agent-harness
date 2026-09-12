package agentruntime

// This file proves the session diagnostic contract end to end over the real
// RunSession pipeline: committed failure-shaped captures identify WHICH
// closed-set failure mode occurred and WHERE in the turn sequence from
// structured log field maps and a metrics snapshot alone, a zero-turn dead
// session fails every diagnosis, and runs without injected sinks are unchanged.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	gwproviders "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// diagnosticRecordSink is a SessionDiagnosticSink that retains every record for
// exact field-map assertions.
type diagnosticRecordSink struct {
	mu      sync.Mutex
	records []SessionDiagnosticRecord
}

func (s *diagnosticRecordSink) RecordSessionDiagnostic(record SessionDiagnosticRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
}

func (s *diagnosticRecordSink) all() []SessionDiagnosticRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SessionDiagnosticRecord(nil), s.records...)
}

func (s *diagnosticRecordSink) events(event string) []SessionDiagnosticRecord {
	var matched []SessionDiagnosticRecord
	for _, record := range s.all() {
		if record.Event == event {
			matched = append(matched, record)
		}
	}
	return matched
}

// sessionDiagnosticArtifacts is everything an automated responder would see
// from one session run: emitted diagnostic records plus the final metrics
// snapshot. Assertions below read only these values.
type sessionDiagnosticArtifacts struct {
	records  *diagnosticRecordSink
	snapshot metrics.Snapshot
	runErr   error
}

func (a sessionDiagnosticArtifacts) failureRecords() []SessionDiagnosticRecord {
	return a.records.events(SessionDiagnosticEventFailure)
}

func (a sessionDiagnosticArtifacts) turnRecords() []SessionDiagnosticRecord {
	return a.records.events(SessionDiagnosticEventTurn)
}

func (a sessionDiagnosticArtifacts) toolCallRecords() []SessionDiagnosticRecord {
	return a.records.events(SessionDiagnosticEventToolCall)
}

func (a sessionDiagnosticArtifacts) series(direction metrics.Direction, modality metrics.Modality) metrics.SeriesSnapshot {
	return a.snapshot.SeriesFor(direction, modality)
}

// runSessionWithDiagnostics drives the real RunSession pipeline with the
// diagnostic sinks injected and captures the resulting artifacts.
func runSessionWithDiagnostics(t *testing.T, mutate func(*SessionRunOptions)) sessionDiagnosticArtifacts {
	t.Helper()

	sink := &diagnosticRecordSink{}
	metricSink, err := metrics.NewInMemorySink()
	if err != nil {
		t.Fatalf("metrics.NewInMemorySink: %v", err)
	}
	opts := SessionRunOptions{ModelCatalog: testModelCatalog(),
		Diagnostics:     sink,
		MetricsRecorder: metricSink,
	}
	if mutate != nil {
		mutate(&opts)
	}
	var out bytes.Buffer
	runErr := RunSession(context.Background(), &out, opts)
	return sessionDiagnosticArtifacts{
		records:  sink,
		snapshot: metricSink.Snapshot(),
		runErr:   runErr,
	}
}

func runReplayFixture(t *testing.T, fixture string) sessionDiagnosticArtifacts {
	t.Helper()
	path := gwtesting.SharedSessionFixturePath(fixture)
	if _, err := gwtesting.LoadSessionCapture(path); err != nil {
		t.Fatalf("load committed fixture %s: %v", fixture, err)
	}
	return runSessionWithDiagnostics(t, func(opts *SessionRunOptions) {
		opts.ReplayPath = path
	})
}

func TestSessionProgressObserver_IgnoresNonTerminalProviderDiagnostic(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: messages.NewNonTerminalErrorValue("response is not active", "response_cancel_not_active"),
	})

	if observer.failure != nil {
		t.Fatalf("nonterminal provider diagnostic became a session failure: %#v", observer.failure)
	}
}

// failureSignature is the identifying structured signature of one closed-set
// failure mode: exact field values on the single canonical failure record.
type failureSignature struct {
	mode       string
	fields     map[string]string
	absentKeys []string
}

var (
	signatureAuthFailure = failureSignature{
		mode: "auth_failure",
		fields: map[string]string{
			"classification":      gwproviders.ErrorClassAuthentication,
			"terminal_reason":     string(messages.TerminalReasonTerminalFailure),
			"terminal_provenance": string(messages.TerminalProvenanceProvider),
			"output_state":        string(messages.TerminalOutputNone),
			"failing_event":       string(messages.StreamTypeError),
			"provider_error_type": "invalid_api_key",
			"provider_error_code": "invalid_api_key",
			"provider":            "grok",
			"model":               "grok-4-failure-auth",
			"turns_completed":     "0",
		},
	}
	signatureDisconnect = failureSignature{
		mode: "mid_session_disconnect",
		fields: map[string]string{
			"classification":      gwproviders.ErrorClassTransport,
			"terminal_reason":     string(messages.TerminalReasonProviderClose),
			"terminal_provenance": string(messages.TerminalProvenanceSession),
			"output_state":        string(messages.TerminalOutputPartial),
			"failing_event":       string(messages.StreamTypeSessionClose),
			"provider":            "grok",
			"model":               "grok-4-failure-disconnect",
			"turns_completed":     "1",
		},
	}
	signatureMalformedFrame = failureSignature{
		mode: "malformed_provider_response",
		fields: map[string]string{
			"classification":      gwproviders.ErrorClassInvalidRequest,
			"terminal_reason":     string(messages.TerminalReasonTerminalFailure),
			"terminal_provenance": string(messages.TerminalProvenanceGateway),
			"output_state":        string(messages.TerminalOutputNone),
			"failing_event":       string(messages.StreamTypeError),
			"provider":            "grok",
			"model":               "grok-4-failure-malformed",
			"turns_completed":     "0",
		},
		absentKeys: []string{"provider_error_type", "provider_error_code"},
	}
)

// matchFailureSignature asserts one mode's signature against captured
// artifacts using structured fields only. A returned error is the exact reason
// the diagnosis does not apply.
func matchFailureSignature(artifacts sessionDiagnosticArtifacts, sig failureSignature) error {
	failures := artifacts.failureRecords()
	if len(failures) != 1 {
		return fmt.Errorf("mode %s: want exactly one %s record, got %d", sig.mode, SessionDiagnosticEventFailure, len(failures))
	}
	fields := failures[0].Fields
	for key, want := range sig.fields {
		got, ok := fields[key]
		if !ok {
			return fmt.Errorf("mode %s: failure record missing structured field %q", sig.mode, key)
		}
		if got != want {
			return fmt.Errorf("mode %s: field %q = %q, want %q", sig.mode, key, got, want)
		}
	}
	for _, key := range sig.absentKeys {
		if _, ok := fields[key]; ok {
			return fmt.Errorf("mode %s: failure record has unexpected field %q", sig.mode, key)
		}
	}
	return nil
}

// matchToolCallFailureSignature identifies the tool-call failure mode: a
// structured unexecutable-tool-call record naming the tool, and no terminal
// failure record (the capture ends with a clean provider close).
func matchToolCallFailureSignature(artifacts sessionDiagnosticArtifacts) error {
	if failures := artifacts.failureRecords(); len(failures) != 0 {
		return fmt.Errorf("tool_call_failure: want no terminal failure record, got %d", len(failures))
	}
	calls := artifacts.toolCallRecords()
	if len(calls) != 1 {
		return fmt.Errorf("tool_call_failure: want exactly one %s record, got %d", SessionDiagnosticEventToolCall, len(calls))
	}
	fields := calls[0].Fields
	want := map[string]string{
		"tool_name":              "get_weather",
		"tool_call_id":           "call_weather_001",
		"failure_classification": gwproviders.ErrorClassUnsupportedRequest,
		"failure_reason":         "no_tool_executor_in_session_runtime",
		"turn_index":             "1",
	}
	for key, wantValue := range want {
		if got := fields[key]; got != wantValue {
			return fmt.Errorf("tool_call_failure: field %q = %q, want %q", key, got, wantValue)
		}
	}
	return nil
}

// matchSilentInputSignature identifies the truncated/silent-audio-input mode:
// per-turn accounting attributes a zero-byte input to turn 1 while turn 2
// reports nonzero input, corroborated by the input/audio metrics series.
func matchSilentInputSignature(artifacts sessionDiagnosticArtifacts) error {
	if failures := artifacts.failureRecords(); len(failures) != 0 {
		return fmt.Errorf("silent_audio_input: want no terminal failure record, got %d", len(failures))
	}
	turns := artifacts.turnRecords()
	if len(turns) < 2 {
		return fmt.Errorf("silent_audio_input: want at least two turn records, got %d", len(turns))
	}
	byIndex := map[string]map[string]string{}
	for _, turn := range turns {
		index, ok := turn.Fields["turn_index"]
		if !ok {
			return fmt.Errorf("silent_audio_input: turn record missing turn_index")
		}
		byIndex[index] = turn.Fields
	}
	silent, ok := byIndex["1"]
	if !ok {
		return fmt.Errorf("silent_audio_input: missing turn_index=1 record")
	}
	if got := silent["input_audio_bytes"]; got != "0" {
		return fmt.Errorf("silent_audio_input: turn 1 input_audio_bytes = %q, want \"0\"", got)
	}
	voiced, ok := byIndex["2"]
	if !ok {
		return fmt.Errorf("silent_audio_input: missing turn_index=2 record")
	}
	if voiced["input_audio_bytes"] == "0" || voiced["input_audio_bytes"] == "" {
		return fmt.Errorf("silent_audio_input: turn 2 input_audio_bytes = %q, want nonzero", voiced["input_audio_bytes"])
	}
	series := artifacts.series(metrics.DirectionInput, metrics.ModalityAudio)
	if series.EventCount != 1 || series.TotalBytes != 4 {
		return fmt.Errorf("silent_audio_input: input/audio series = (count=%d, bytes=%d), want (count=1, bytes=4)", series.EventCount, series.TotalBytes)
	}
	return nil
}

// requireExactlyOneFailureRecord is the emission-contract core: exactly one
// canonical failure record carrying every stable field.
func requireExactlyOneFailureRecord(t *testing.T, artifacts sessionDiagnosticArtifacts) map[string]string {
	t.Helper()
	failures := artifacts.failureRecords()
	if len(failures) != 1 {
		t.Fatalf("want exactly one %s record, got %d (all records: %v)", SessionDiagnosticEventFailure, len(failures), artifacts.records.all())
	}
	required := []string{
		"classification",
		"terminal_reason",
		"terminal_provenance",
		"output_state",
		"provider",
		"model",
		"turns_completed",
		"failing_event",
	}
	fields := failures[0].Fields
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			t.Fatalf("failure record missing required field %q; fields: %v", key, fields)
		}
	}
	return fields
}

// --- Story -001: one canonical failure record per phase -----------------

type connectFailingSessionInferencer struct {
	err error
}

func (i *connectFailingSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, i.err
}

func TestSessionDiagnostics_ConnectPhaseFailureEmitsOneCanonicalRecord(t *testing.T) {
	connectErr := fmt.Errorf("dial refused: %w", gwproviders.ErrAuthentication)
	artifacts := runSessionWithDiagnostics(t, func(opts *SessionRunOptions) {
		opts.ReplayPath = "scripted-connect-failure.session.json"
		opts.Provider = "grok"
		opts.Model = "scripted-realtime"
		opts.SessionInferencer = &connectFailingSessionInferencer{err: connectErr}
	})
	if artifacts.runErr == nil {
		t.Fatal("expected RunSession to fail when the session dial fails")
	}
	fields := requireExactlyOneFailureRecord(t, artifacts)
	want := map[string]string{
		"classification":      gwproviders.ErrorClassAuthentication,
		"terminal_reason":     string(messages.TerminalReasonTerminalFailure),
		"terminal_provenance": string(messages.TerminalProvenanceCLI),
		"output_state":        string(messages.TerminalOutputNone),
		"failing_event":       failingEventConnect,
		"turns_completed":     "0",
	}
	for key, wantValue := range want {
		if got := fields[key]; got != wantValue {
			t.Fatalf("field %q = %q, want %q (fields: %v)", key, got, wantValue, fields)
		}
	}
}

func TestSessionDiagnostics_MidStreamFailureEmitsOneCanonicalRecord(t *testing.T) {
	sessionInf := &scriptedSessionInferencer{
		events: []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("quota gone")},
			{Type: messages.StreamTypeError, Value: messages.NewErrorValueWithTerminal(
				"provider quota exhausted",
				gwproviders.ErrorClassRateLimited,
				messages.TerminalReasonTerminalFailure,
				messages.TerminalProvenanceProvider,
				messages.TerminalOutputNone,
			)},
			{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("scripted-midstream", "stream ended")},
		},
	}
	artifacts := runSessionWithDiagnostics(t, func(opts *SessionRunOptions) {
		opts.ReplayPath = "scripted-midstream.session.json"
		opts.Provider = "grok"
		opts.Model = "scripted-realtime"
		opts.SessionInferencer = sessionInf
		opts.WaitForClose = true
	})
	if artifacts.runErr == nil {
		t.Fatal("expected RunSession to surface the provider stream error")
	}
	fields := requireExactlyOneFailureRecord(t, artifacts)
	want := map[string]string{
		"classification":      gwproviders.ErrorClassRateLimited,
		"terminal_reason":     string(messages.TerminalReasonTerminalFailure),
		"terminal_provenance": string(messages.TerminalProvenanceProvider),
		"output_state":        string(messages.TerminalOutputNone),
		"failing_event":       string(messages.StreamTypeError),
		"turns_completed":     "0",
	}
	for key, wantValue := range want {
		if got := fields[key]; got != wantValue {
			t.Fatalf("field %q = %q, want %q (fields: %v)", key, got, wantValue, fields)
		}
	}
}

func TestSessionDiagnostics_DrainPhaseFailureEmitsOneCanonicalRecord(t *testing.T) {
	sessionInf := &scriptedSessionInferencer{
		events: []messages.StreamMessage{
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("unwritable")},
			{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("scripted-drain", "done")},
		},
	}
	sink := &diagnosticRecordSink{}
	opts := SessionRunOptions{ModelCatalog: testModelCatalog(),
		ReplayPath:        "scripted-drain-failure.session.json",
		SessionInferencer: sessionInf,
		WaitForClose:      true,
		Diagnostics:       sink,
	}
	runErr := RunSession(context.Background(), failingWriter{err: errors.New("unwritable")}, opts)
	if runErr == nil {
		t.Fatal("expected RunSession to surface the drain write failure")
	}
	failures := sink.events(SessionDiagnosticEventFailure)
	if len(failures) != 1 {
		t.Fatalf("want exactly one failure record, got %d", len(failures))
	}
	fields := failures[0].Fields
	want := map[string]string{
		"terminal_reason":     string(messages.TerminalReasonTerminalFailure),
		"terminal_provenance": string(messages.TerminalProvenanceCLI),
		"failing_event":       failingEventRun,
		"turns_completed":     "0",
	}
	for key, wantValue := range want {
		if got := fields[key]; got != wantValue {
			t.Fatalf("field %q = %q, want %q (fields: %v)", key, got, wantValue, fields)
		}
	}
}

// --- Stories -002/-003: fixture-driven closed-set signatures ------------

func TestSessionDiagnostics_AuthFailureFixtureIsDiagnosable(t *testing.T) {
	artifacts := runReplayFixture(t, "session_failure_auth.session.json")
	if err := matchFailureSignature(artifacts, signatureAuthFailure); err != nil {
		t.Fatal(err)
	}
	input := artifacts.series(metrics.DirectionInput, metrics.ModalityAudio)
	output := artifacts.series(metrics.DirectionOutput, metrics.ModalityAudio)
	if input.EventCount != 0 || output.EventCount != 0 {
		t.Fatalf("auth rejection precedes any media: input/audio=(%d,%d) output/audio=(%d,%d), want zeros",
			input.EventCount, input.TotalBytes, output.EventCount, output.TotalBytes)
	}
}

func TestSessionDiagnostics_MidSessionDisconnectFixtureIsDiagnosable(t *testing.T) {
	artifacts := runReplayFixture(t, "session_failure_disconnect.session.json")
	if err := matchFailureSignature(artifacts, signatureDisconnect); err != nil {
		t.Fatal(err)
	}
	text := artifacts.series(metrics.DirectionOutput, metrics.ModalityText)
	if text.EventCount != 1 || text.TotalBytes != uint64(len("partial answer before the transport died")) {
		t.Fatalf("disconnect output/text series = (count=%d, bytes=%d), want (1, %d)",
			text.EventCount, text.TotalBytes, len("partial answer before the transport died"))
	}
}

func TestSessionDiagnostics_MalformedFrameFixtureIsDiagnosable(t *testing.T) {
	artifacts := runReplayFixture(t, "session_failure_malformed_frame.session.json")
	if err := matchFailureSignature(artifacts, signatureMalformedFrame); err != nil {
		t.Fatal(err)
	}
}

func TestSessionDiagnostics_ToolCallFailureFixtureIsDiagnosable(t *testing.T) {
	artifacts := runReplayFixture(t, "session_failure_tool_call.session.json")
	if err := matchToolCallFailureSignature(artifacts); err != nil {
		t.Fatal(err)
	}
}

// --- Story -002: metrics observations and per-turn input accounting -----

func TestSessionDiagnostics_HealthyMultiTurnFixtureYieldsReceivedAudioSeries(t *testing.T) {
	artifacts := runReplayFixture(t, "session_healthy_multiturn_audio.session.json")
	if artifacts.runErr != nil {
		t.Fatalf("healthy replay failed: %v", artifacts.runErr)
	}
	outputAudio := artifacts.series(metrics.DirectionOutput, metrics.ModalityAudio)
	if outputAudio.EventCount != 1 || outputAudio.TotalBytes != 6 {
		t.Fatalf("output/audio series = (count=%d, bytes=%d), want (count=1, bytes=6)", outputAudio.EventCount, outputAudio.TotalBytes)
	}
	outputText := artifacts.series(metrics.DirectionOutput, metrics.ModalityText)
	if outputText.EventCount != 2 || outputText.TotalBytes != uint64(len("Hello there")+len("Second turn reply")) {
		t.Fatalf("output/text series = (count=%d, bytes=%d), want (2, %d)",
			outputText.EventCount, outputText.TotalBytes, len("Hello there")+len("Second turn reply"))
	}
	turns := artifacts.turnRecords()
	if len(turns) != 2 {
		t.Fatalf("want two per-turn records, got %d", len(turns))
	}
	first := turns[0].Fields
	if first["turn_index"] != "1" || first["output_audio_bytes"] != "6" || first["output_text_bytes"] != "11" {
		t.Fatalf("turn 1 record = %v, want turn_index=1 output_audio_bytes=6 output_text_bytes=11", first)
	}
	second := turns[1].Fields
	if second["turn_index"] != "2" || second["output_text_bytes"] != "17" {
		t.Fatalf("turn 2 record = %v, want turn_index=2 output_text_bytes=17", second)
	}
}

func TestSessionDiagnostics_SilentAudioInputAttributesZeroBytesToItsTurn(t *testing.T) {
	sessionInf := &scriptedSessionInferencer{
		events: []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("t1")},
			{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("t2")},
			{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("t3")},
			{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
			{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("scripted-silent", "done")},
		},
	}
	artifacts := runSessionWithDiagnostics(t, func(opts *SessionRunOptions) {
		opts.ReplayPath = "scripted-silent-input.session.json"
		opts.Provider = "grok"
		opts.Model = "scripted-realtime"
		opts.SessionInferencer = sessionInf
		opts.WaitForClose = true
		opts.AudioInputs = []ScheduledAudioInput{
			{AfterCompletedTurns: 0, PCM: nil},
			{AfterCompletedTurns: 1, PCM: []byte{1, 2, 3, 4}},
		}
	})
	if artifacts.runErr != nil {
		t.Fatalf("silent-input replay failed: %v", artifacts.runErr)
	}
	if err := matchSilentInputSignature(artifacts); err != nil {
		t.Fatal(err)
	}
	turns := artifacts.turnRecords()
	if len(turns) != 3 {
		t.Fatalf("want three per-turn records, got %d", len(turns))
	}
	last := turns[2].Fields
	if last["turn_index"] != "3" || last["input_audio_bytes"] != "0" {
		t.Fatalf("turn 3 record = %v, want turn_index=3 input_audio_bytes=0", last)
	}
}

// --- Story -004: negative control and pairwise distinctness -------------

// diagnoseClosedSetMode applies the identifying assertions of one closed-set
// mode to captured artifacts. A nil error means the artifacts match that
// mode's signature exactly.
func diagnoseClosedSetMode(mode string, artifacts sessionDiagnosticArtifacts) error {
	switch mode {
	case "auth_failure":
		return matchFailureSignature(artifacts, signatureAuthFailure)
	case "mid_session_disconnect":
		return matchFailureSignature(artifacts, signatureDisconnect)
	case "malformed_provider_response":
		return matchFailureSignature(artifacts, signatureMalformedFrame)
	case "tool_call_failure":
		return matchToolCallFailureSignature(artifacts)
	case "silent_audio_input":
		return matchSilentInputSignature(artifacts)
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
}

var closedSetModes = []string{
	"auth_failure",
	"mid_session_disconnect",
	"malformed_provider_response",
	"tool_call_failure",
	"silent_audio_input",
}

func TestSessionDiagnostics_ZeroTurnDeadSessionFailsEveryDiagnosis(t *testing.T) {
	artifacts := runReplayFixture(t, "session_dead_zeroturn.session.json")
	for _, mode := range closedSetModes {
		err := diagnoseClosedSetMode(mode, artifacts)
		if err == nil {
			t.Fatalf("negative control violated: zero-turn dead-session artifacts satisfied the %q diagnosis", mode)
		}
	}
}

func TestSessionDiagnostics_FailureModeSignaturesArePairwiseDistinct(t *testing.T) {
	artifactSets := map[string]sessionDiagnosticArtifacts{}
	for _, mode := range closedSetModes {
		var artifacts sessionDiagnosticArtifacts
		switch mode {
		case "auth_failure":
			artifacts = runReplayFixture(t, "session_failure_auth.session.json")
		case "mid_session_disconnect":
			artifacts = runReplayFixture(t, "session_failure_disconnect.session.json")
		case "malformed_provider_response":
			artifacts = runReplayFixture(t, "session_failure_malformed_frame.session.json")
		case "tool_call_failure":
			artifacts = runReplayFixture(t, "session_failure_tool_call.session.json")
		case "silent_audio_input":
			artifacts = runSessionWithDiagnostics(t, func(opts *SessionRunOptions) {
				opts.ReplayPath = "scripted-pairwise-silent.session.json"
				opts.Provider = "grok"
				opts.Model = "scripted-realtime"
				opts.SessionInferencer = &scriptedSessionInferencer{
					events: []messages.StreamMessage{
						{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
						{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("t1")},
						{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
						{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
						{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("t2")},
						{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
						{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("scripted-pairwise", "done")},
					},
				}
				opts.WaitForClose = true
				opts.AudioInputs = []ScheduledAudioInput{
					{AfterCompletedTurns: 0, PCM: nil},
					{AfterCompletedTurns: 1, PCM: []byte{1, 2, 3, 4}},
				}
			})
		}
		if err := diagnoseClosedSetMode(mode, artifacts); err != nil {
			t.Fatalf("mode %s must satisfy its own diagnosis: %v", mode, err)
		}
		artifactSets[mode] = artifacts
	}
	for _, modeX := range closedSetModes {
		for _, modeY := range closedSetModes {
			if modeX == modeY {
				continue
			}
			if err := diagnoseClosedSetMode(modeY, artifactSets[modeX]); err == nil {
				t.Fatalf("signatures are not distinct: %q artifacts satisfied the %q diagnosis", modeX, modeY)
			}
		}
	}
}

func TestSessionDiagnostics_HealthyRunMatchesNoFailureSignature(t *testing.T) {
	healthy := runReplayFixture(t, "session_healthy_multiturn_audio.session.json")
	for _, mode := range closedSetModes {
		if err := diagnoseClosedSetMode(mode, healthy); err == nil {
			t.Fatalf("healthy-run artifacts unexpectedly matched the %q failure signature", mode)
		}
	}
}

// --- Emission contract guard --------------------------------------------

func TestSessionDiagnostics_CleanCloseProducesNoFailureRecord(t *testing.T) {
	artifacts := runReplayFixture(t, "session_healthy_multiturn_audio.session.json")
	if failures := artifacts.failureRecords(); len(failures) != 0 {
		t.Fatalf("clean run emitted %d failure records, want 0", len(failures))
	}
}

func TestSessionDiagnostics_OnlyNoExecutorToolCallsAreUnexecutable(t *testing.T) {
	newToolCall := func() messages.StreamMessage {
		return messages.StreamMessage{
			Type:  messages.StreamTypeToolCallEnd,
			Role:  messages.RoleAssistant,
			Value: messages.NewToolCallEndValue("call-room-proof", "exec", `{"command":"printf ROOMPROOF"}`),
		}
	}

	t.Run("tool-enabled session", func(t *testing.T) {
		sink := &diagnosticRecordSink{}
		observer := newSessionProgressObserver(sink, nil, "openai", "gpt-realtime-2.1-mini")
		observer.setToolResultsEnabled(true)
		observer.observe(newToolCall())
		if records := sink.events(SessionDiagnosticEventToolCall); len(records) != 0 {
			t.Fatalf("tool-enabled session emitted %d unexecutable records: %v", len(records), records)
		}
	})

	t.Run("no-executor session", func(t *testing.T) {
		sink := &diagnosticRecordSink{}
		observer := newSessionProgressObserver(sink, nil, "openai", "gpt-realtime-2.1-mini")
		observer.setToolResultsEnabled(false)
		observer.observe(newToolCall())
		records := sink.events(SessionDiagnosticEventToolCall)
		if len(records) != 1 {
			t.Fatalf("no-executor session emitted %d unexecutable records, want one", len(records))
		}
		if records[0].Fields["tool_call_id"] != "call-room-proof" || records[0].Fields["tool_name"] != "exec" {
			t.Fatalf("unexecutable record = %#v, want correlated exec call", records[0])
		}
	})
}

// Guard the harness itself against accidental dependence on prose: the
// assertions above must keep compiling when the human-readable stream text
// changes. This test pins that behavior by asserting the disconnect fixture's
// rendered prose is NOT part of any diagnostic field map.
func TestSessionDiagnostics_FieldMapsCarryNoProse(t *testing.T) {
	prose := "partial answer before the transport died"
	artifacts := runReplayFixture(t, "session_failure_disconnect.session.json")
	for _, record := range artifacts.records.all() {
		for key, value := range record.Fields {
			if strings.Contains(value, prose) {
				t.Fatalf("field %q carries human prose %q; diagnostics must stay structured", key, prose)
			}
		}
	}
	if artifacts.runErr != nil && strings.Contains(fmt.Sprint(artifacts.runErr), prose) {
		t.Log("run error carries prose; diagnostics contract covers the field maps only")
	}
}
