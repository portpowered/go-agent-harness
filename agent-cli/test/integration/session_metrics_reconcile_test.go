package integration

// The final-accounting proof drives the shipped `agent session` command over
// the hermetic replay transport. The expected side is an independent fold of
// the raw replay ledger; the actual side is the production-owned terminal
// SessionRuntimeObservation.FinalAccounting value. Duration WAV and JSONL
// artifacts remain user-visible output checks, but are never used as the
// accounting source.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

const (
	metricsReconcileText     = "Reconciled text."
	metricsReconcileDeadline = 30 * time.Second
)

// corpusAudioWAVPath locates a committed corpus WAV. The fixture is assembled
// in a temporary directory so raw audio never enters a committed JSON capture.
func corpusAudioWAVPath(t *testing.T, name string) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve corpus audio path: runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "go-agent-loop", "testdata", "audio", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("committed corpus WAV %s not found: %v", name, err)
	}
	return path
}

// buildMetricsReconcileFixture creates a normalized stream-message capture in
// a temporary directory. It contains two complete assistant turns: a text-only
// turn with a non-empty text delta and an audio-only turn with two non-empty
// audio deltas cut from the committed corpus. Each turn has an incremental,
// usage-bearing MESSAGE.END, followed by a recorded SESSION.CLOSE boundary.
// There are no client records because this proof intentionally exercises the
// output/accounting side without making input-series claims.
func buildMetricsReconcileFixture(t *testing.T) (path string, audioPCM []byte) {
	t.Helper()

	wavBytes, err := os.ReadFile(corpusAudioWAVPath(t, "utt_short_16k.wav"))
	if err != nil {
		t.Fatalf("read committed corpus WAV: %v", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse committed corpus WAV: %v", err)
	}
	if rate != wavio.Rate16kHz || len(samples) < 2 {
		t.Fatalf("committed corpus WAV = rate %d, %d samples; want 16kHz and audio", rate, len(samples))
	}
	audioPCM = make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(audioPCM[index*2:], uint16(sample))
	}
	half := len(audioPCM) / 2
	if half == 0 {
		t.Fatal("committed corpus WAV produced no PCM bytes")
	}

	streamRecord := func(sequence int, msg messages.StreamMessage) map[string]any {
		payload, marshalErr := gwtesting.MarshalStreamMessage(msg)
		if marshalErr != nil {
			t.Fatalf("marshal %s stream message: %v", msg.Type, marshalErr)
		}
		direction := "server_to_client"
		return map[string]any{
			"sequence": sequence, "direction": direction, "timestamp_ms": sequence - 1,
			"type": msg.Type, "payload_type": gwtesting.SessionPayloadTypeStreamMessage,
			"payload": json.RawMessage(payload),
		}
	}

	textUsage := messages.TokenUsage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18, ReasoningTokens: 2}
	audioUsage := messages.TokenUsage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8, ReasoningTokens: 1}
	records := []map[string]any{
		streamRecord(1, messages.StreamMessage{
			Type:  messages.StreamTypeSessionOpen,
			Value: messages.NewSessionOpenValue("sess_metrics_reconcile", "replay"),
		}),
		streamRecord(2, messages.StreamMessage{
			Type:  messages.StreamTypeSessionCreated,
			Value: messages.NewSessionCreatedValue("sess_metrics_reconcile", "grok-synthetic"),
		}),
		streamRecord(3, messages.StreamMessage{
			Type:  messages.StreamTypeMessageStart,
			Role:  messages.RoleAssistant,
			Value: messages.NewMessageStartValue(),
		}),
		streamRecord(4, messages.StreamMessage{
			Type:  messages.StreamTypeTextStart,
			Role:  messages.RoleAssistant,
			Value: messages.NewTextStartValue(),
		}),
		streamRecord(5, messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewTextDeltaValue(metricsReconcileText),
		}),
		streamRecord(6, messages.StreamMessage{
			Type:  messages.StreamTypeTextEnd,
			Role:  messages.RoleAssistant,
			Value: messages.NewTextEndValue(),
		}),
		streamRecord(7, messages.StreamMessage{
			Type:  messages.StreamTypeMessageEnd,
			Role:  messages.RoleAssistant,
			Value: messages.NewMessageEndValue(textUsage),
		}),
		streamRecord(8, messages.StreamMessage{
			Type:  messages.StreamTypeMessageStart,
			Role:  messages.RoleAssistant,
			Value: messages.NewMessageStartValue(),
		}),
		streamRecord(9, messages.StreamMessage{
			Type:  messages.StreamTypeAudioStart,
			Role:  messages.RoleAssistant,
			Value: messages.NewAudioStartValue(),
		}),
		streamRecord(10, messages.StreamMessage{
			Type:  messages.StreamTypeAudioDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewAudioDeltaValue(audioPCM[:half]),
		}),
		streamRecord(11, messages.StreamMessage{
			Type:  messages.StreamTypeAudioDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewAudioDeltaValue(audioPCM[half:]),
		}),
		streamRecord(12, messages.StreamMessage{
			Type:  messages.StreamTypeAudioEnd,
			Role:  messages.RoleAssistant,
			Value: messages.NewAudioEndValue(),
		}),
		streamRecord(13, messages.StreamMessage{
			Type:  messages.StreamTypeMessageEnd,
			Role:  messages.RoleAssistant,
			Value: messages.NewMessageEndValue(audioUsage),
		}),
		streamRecord(14, messages.StreamMessage{
			Type:  messages.StreamTypeSessionClose,
			Value: messages.NewSessionCloseValue("sess_metrics_reconcile", "fixture_complete"),
		}),
	}

	fixture := map[string]any{
		"version":  1,
		"provider": map[string]any{"name": "grok", "model": "grok-synthetic"},
		"session": map[string]any{
			"id":                 "sess_metrics_reconcile",
			"started_at_utc":     "2026-08-25T00:00:00Z",
			"fixture_provenance": gwtesting.SessionFixtureProvenanceSynthetic,
		},
		"records": records,
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatalf("marshal metrics reconciliation fixture: %v", err)
	}
	path = filepath.Join(t.TempDir(), "metrics_reconcile.session.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write metrics reconciliation fixture: %v", err)
	}
	if violations := gwtesting.ValidateSessionCaptureFile(path); len(violations) > 0 {
		t.Fatalf("assembled fixture failed hygiene validation: %v", violations)
	}
	return path, audioPCM
}

// ledgerEntry is one normalized stream event observed at either side of the
// independent comparison. The command artifact and fixture are both decoded
// into this small immutable representation before folding.
type ledgerEntry struct {
	typeName messages.StreamMessageType
	text     string
	audioLen int
	usage    *messages.TokenUsage
}

type runtimeObservationCapture struct {
	mu           sync.Mutex
	observations []wire.SessionRuntimeObservation
}

func (o *runtimeObservationCapture) ObserveSessionRuntime(observation wire.SessionRuntimeObservation) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.observations = append(o.observations, observation)
	o.mu.Unlock()
}

func (o *runtimeObservationCapture) snapshot() []wire.SessionRuntimeObservation {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	observations := make([]wire.SessionRuntimeObservation, len(o.observations))
	copy(observations, o.observations)
	return observations
}

func ledgerEntryFromMessage(msg messages.StreamMessage) ledgerEntry {
	entry := ledgerEntry{typeName: msg.Type}
	switch value := msg.Value.(type) {
	case *messages.TextDeltaValue:
		entry.text = value.Content
	case *messages.TranscriptDeltaValue:
		entry.text = value.Text
	case *messages.AudioDeltaValue:
		entry.audioLen = len(value.Content)
	case *messages.MessageEndValue:
		usage := value.Usage
		entry.usage = &usage
	}
	return entry
}

func decodeFixtureLedger(path string) ([]ledgerEntry, error) {
	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		return nil, fmt.Errorf("load replay fixture %s: %w", path, err)
	}
	ledger := make([]ledgerEntry, 0, len(capture.Records))
	for index, record := range capture.Records {
		if record.Direction != gwtesting.DirectionServerToClient {
			continue
		}
		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
		msg, decodeErr := gwtesting.UnmarshalStreamMessage(payload)
		if decodeErr != nil {
			return nil, fmt.Errorf("decode fixture stream message %d (%s): %w", index+1, record.Type, decodeErr)
		}
		ledger = append(ledger, ledgerEntryFromMessage(msg))
	}
	return ledger, nil
}

func validateTranscriptArtifact(path string, data []byte) error {
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return fmt.Errorf("command duration transcript %s is empty", path)
	}
	for index, line := range lines {
		record, decodeErr := transcript.Decode([]byte(line))
		if decodeErr != nil {
			return fmt.Errorf("decode duration transcript line %d of %s: %w", index+1, path, decodeErr)
		}
		if _, decodeErr := gwtesting.UnmarshalStreamMessage(record.Payload); decodeErr != nil {
			return fmt.Errorf("decode duration event line %d of %s: %w", index+1, path, decodeErr)
		}
	}
	return nil
}

type seriesFold struct {
	eventCount uint64
	totalBytes uint64
	histogram  metrics.HistogramSnapshot
}

type accountingFold struct {
	series     map[metrics.SeriesKey]seriesFold
	tokenTotal messages.TokenUsage
}

func newAccountingFold() accountingFold {
	series := make(map[metrics.SeriesKey]seriesFold)
	bounds := metrics.DefaultHistogramBounds()
	for _, direction := range metrics.SupportedDirections() {
		for _, modality := range metrics.SupportedModalities() {
			series[metrics.SeriesKey{Direction: direction, Modality: modality}] = seriesFold{
				histogram: metrics.HistogramSnapshot{
					Bounds:       append([]int64(nil), bounds...),
					BucketCounts: make([]uint64, len(bounds)),
				},
			}
		}
	}
	return accountingFold{series: series}
}

func (f *accountingFold) record(direction metrics.Direction, modality metrics.Modality, byteCount int) {
	if f == nil || byteCount <= 0 {
		return
	}
	key := metrics.SeriesKey{Direction: direction, Modality: modality}
	series := f.series[key]
	bytes := uint64(byteCount)
	series.eventCount++
	series.totalBytes += bytes
	series.histogram.SampleCount++
	series.histogram.ByteSum += bytes
	bucket := len(series.histogram.Bounds)
	for index, bound := range series.histogram.Bounds {
		if bytes <= uint64(bound) {
			bucket = index
			break
		}
	}
	if bucket == len(series.histogram.Bounds) {
		series.histogram.OverflowCount++
	} else {
		series.histogram.BucketCounts[bucket]++
	}
	f.series[key] = series
}

// foldAccounting is the independent replay oracle. Text and transcript deltas
// are output/text observations; audio deltas are output/audio observations.
// Every non-negative MESSAGE.END usage value is an incremental contribution for
// that completed turn, regardless of whether a payload was observed in the
// same turn. The initialized map makes every supported, unobserved series an
// exact zero, including its histogram state.
func foldAccounting(ledger []ledgerEntry) (accountingFold, error) {
	fold := newAccountingFold()
	inTurn := false
	for index, entry := range ledger {
		switch entry.typeName {
		case messages.StreamTypeMessageStart:
			if inTurn {
				return accountingFold{}, fmt.Errorf("ledger entry %d starts a turn before the prior turn ended", index+1)
			}
			inTurn = true
		case messages.StreamTypeTextDelta, messages.StreamTypeTranscriptDelta:
			fold.record(metrics.DirectionOutput, metrics.ModalityText, len(entry.text))
		case messages.StreamTypeAudioDelta:
			fold.record(metrics.DirectionOutput, metrics.ModalityAudio, entry.audioLen)
		case messages.StreamTypeMessageEnd:
			if !inTurn || entry.usage == nil {
				return accountingFold{}, fmt.Errorf("ledger entry %d is not a usage-bearing message close", index+1)
			}
			usage := *entry.usage
			if usage.PromptTokens+usage.CompletionTokens != usage.TotalTokens {
				return accountingFold{}, fmt.Errorf("ledger entry %d violates prompt+completion=total: %+v", index+1, usage)
			}
			fold.tokenTotal.PromptTokens += usage.PromptTokens
			fold.tokenTotal.CompletionTokens += usage.CompletionTokens
			fold.tokenTotal.TotalTokens += usage.TotalTokens
			fold.tokenTotal.ReasoningTokens += usage.ReasoningTokens
			inTurn = false
		}
	}
	if inTurn {
		return accountingFold{}, errors.New("ledger ends with an unclosed output turn")
	}
	return fold, nil
}

func compareAccounting(expected accountingFold, actual *wire.SessionFinalAccounting) error {
	var mismatches []string
	if actual == nil {
		return errors.New("session accounting does not reconcile: missing production final accounting")
	}
	if !reflect.DeepEqual(metrics.DefaultHistogramBounds(), actual.Metrics.HistogramBounds) {
		mismatches = append(mismatches, fmt.Sprintf("histogram_bounds: expected %v, actual %v", metrics.DefaultHistogramBounds(), actual.Metrics.HistogramBounds))
	}
	orderedKeys := make([]metrics.SeriesKey, 0, len(metrics.SupportedDirections())*len(metrics.SupportedModalities()))
	for _, direction := range metrics.SupportedDirections() {
		for _, modality := range metrics.SupportedModalities() {
			key := metrics.SeriesKey{Direction: direction, Modality: modality}
			orderedKeys = append(orderedKeys, key)
			want := expected.series[key]
			got, ok := actual.Metrics.Lookup(direction, modality)
			name := key.String()
			if !ok {
				mismatches = append(mismatches, fmt.Sprintf("%s series: expected present, actual missing", name))
				continue
			}
			if want.eventCount != got.EventCount {
				mismatches = append(mismatches, fmt.Sprintf("%s event_count: expected %d, actual %d", name, want.eventCount, got.EventCount))
			}
			if want.totalBytes != got.TotalBytes {
				mismatches = append(mismatches, fmt.Sprintf("%s total_bytes: expected %d, actual %d", name, want.totalBytes, got.TotalBytes))
			}
			if !reflect.DeepEqual(want.histogram, got.Histogram) {
				mismatches = append(mismatches, fmt.Sprintf("%s histogram: expected %#v, actual %#v", name, want.histogram, got.Histogram))
			}
		}
	}
	if len(actual.Metrics.Series) != len(orderedKeys) {
		mismatches = append(mismatches, fmt.Sprintf("series count: expected %d, actual %d", len(orderedKeys), len(actual.Metrics.Series)))
	}
	for index, key := range orderedKeys {
		if index >= len(actual.Metrics.Series) {
			break
		}
		got := actual.Metrics.Series[index]
		if got.Direction != key.Direction || got.Modality != key.Modality {
			mismatches = append(mismatches, fmt.Sprintf("series order at index %d: expected %s, actual %s", index, key, metrics.SeriesKey{Direction: got.Direction, Modality: got.Modality}))
		}
	}
	if uint64(expected.tokenTotal.PromptTokens) != actual.PromptTokens {
		mismatches = append(mismatches, fmt.Sprintf("token prompt: expected %d, actual %d", expected.tokenTotal.PromptTokens, actual.PromptTokens))
	}
	if uint64(expected.tokenTotal.CompletionTokens) != actual.CompletionTokens {
		mismatches = append(mismatches, fmt.Sprintf("token completion: expected %d, actual %d", expected.tokenTotal.CompletionTokens, actual.CompletionTokens))
	}
	if uint64(expected.tokenTotal.TotalTokens) != actual.TotalTokens {
		mismatches = append(mismatches, fmt.Sprintf("token total: expected %d, actual %d", expected.tokenTotal.TotalTokens, actual.TotalTokens))
	}
	if uint64(expected.tokenTotal.ReasoningTokens) != actual.ReasoningTokens {
		mismatches = append(mismatches, fmt.Sprintf("token reasoning: expected %d, actual %d", expected.tokenTotal.ReasoningTokens, actual.ReasoningTokens))
	}
	if len(mismatches) > 0 {
		return fmt.Errorf("session accounting does not reconcile:\n  %s", strings.Join(mismatches, "\n  "))
	}
	return nil
}

// removeOneNonEmptyOutputTextDelta returns an independent ledger copy with
// exactly one known non-empty output-text observation removed. The production
// final accounting observation remains untouched by this oracle-only mutation.
func removeOneNonEmptyOutputTextDelta(t *testing.T, ledger []ledgerEntry) ([]ledgerEntry, ledgerEntry) {
	t.Helper()
	for index, entry := range ledger {
		if (entry.typeName == messages.StreamTypeTextDelta || entry.typeName == messages.StreamTypeTranscriptDelta) && entry.text != "" {
			mutated := make([]ledgerEntry, 0, len(ledger)-1)
			mutated = append(mutated, ledger[:index]...)
			mutated = append(mutated, ledger[index+1:]...)
			return mutated, entry
		}
	}
	t.Fatal("fixture ledger has no non-empty output-text delta to remove")
	return nil, ledgerEntry{}
}

func readCommandObservation(t *testing.T) (fixturePath string, expectedPCM []byte, stdout string, fixtureLedger []ledgerEntry, finalAccounting *wire.SessionFinalAccounting, audioOut, durationAudio []byte, runErr error) {
	t.Helper()
	fixturePath, expectedPCM = buildMetricsReconcileFixture(t)
	artifactBase := strings.TrimSuffix(fixturePath, filepath.Ext(fixturePath))
	transcriptPath := artifactBase + ".jsonl"
	durationWAVPath := artifactBase + ".wav"
	audioOutPath := filepath.Join(filepath.Dir(fixturePath), "assistant-reply.wav")

	runtimeObserver := &runtimeObservationCapture{}
	agentCLI, err := wire.InitializeMockAgentCLIWithPorts(
		wire.NewPortSwap(wire.PortToolExecutor, &mockToolExecutor{}),
		wire.NewPortSwap(wire.PortSessionRuntimeObserver, runtimeObserver),
	)
	if err != nil {
		t.Fatalf("initialize production CLI router: %v", err)
	}
	output := &bytes.Buffer{}
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(output)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{
		"session",
		"--replay", fixturePath,
		"--provider", "grok",
		"--model", "grok-synthetic",
		"--audio-out", audioOutPath,
		"--max-duration", "2s",
	})

	bounded, cancel := context.WithTimeout(context.Background(), metricsReconcileDeadline)
	defer cancel()
	runErr = rootCmd.ExecuteContext(bounded)
	stdout = output.String()

	transcriptData, readErr := os.ReadFile(transcriptPath)
	if readErr != nil {
		t.Fatalf("read command duration transcript %s (run error: %v): %v", transcriptPath, runErr, readErr)
	}
	if transcriptErr := validateTranscriptArtifact(transcriptPath, transcriptData); transcriptErr != nil {
		t.Fatalf("validate command duration transcript: %v", transcriptErr)
	}
	durationAudio, readErr = os.ReadFile(durationWAVPath)
	if readErr != nil {
		t.Fatalf("read command duration WAV %s (run error: %v): %v", durationWAVPath, runErr, readErr)
	}
	audioOut, readErr = os.ReadFile(audioOutPath)
	if readErr != nil {
		t.Fatalf("read command --audio-out WAV %s (run error: %v): %v", audioOutPath, runErr, readErr)
	}
	fixtureLedger, err = decodeFixtureLedger(fixturePath)
	if err != nil {
		t.Fatalf("decode fixture ledger: %v", err)
	}
	observations := runtimeObserver.snapshot()
	terminalCount := 0
	for _, observation := range observations {
		if observation.Kind == wire.SessionRuntimeObservationTerminal {
			terminalCount++
			if observation.FinalAccounting != nil {
				if finalAccounting != nil {
					t.Fatalf("runtime observer delivered more than one final accounting value")
				}
				finalAccounting = observation.FinalAccounting
			}
		} else if observation.FinalAccounting != nil {
			t.Fatalf("non-terminal runtime observation %q carried final accounting", observation.Kind)
		}
	}
	if terminalCount != 1 || finalAccounting == nil {
		t.Fatalf("runtime terminal observations = %d, final accounting nil = %t (run error: %v)", terminalCount, finalAccounting == nil, runErr)
	}
	return fixturePath, expectedPCM, stdout, fixtureLedger, finalAccounting, audioOut, durationAudio, runErr
}

func wavPCM(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	_, samples, err := wavio.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	pcm := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(pcm[index*2:], uint16(sample))
	}
	return pcm
}

// TestSessionCommandMetricsReconcileMatchesIndependentFoldOverFullSession is
// the positive proof. It runs the actual command with normal argv, folds the
// raw replay fixture independently, compares it with the production-owned
// terminal observation across every supported series and usage field, and
// verifies the command reached SESSION.CLOSE.
func TestSessionCommandMetricsReconcileMatchesIndependentFoldOverFullSession(t *testing.T) {
	fixturePath, expectedPCM, stdout, fixtureLedger, captured, audioOut, durationAudio, runErr := readCommandObservation(t)
	if runErr != nil {
		t.Fatalf("session command returned an error over hermetic replay fixture %s: %v", fixturePath, runErr)
	}
	if !strings.Contains(stdout, "[session closed: fixture_complete]") {
		t.Fatalf("command did not reach recorded terminal boundary, stdout=%q", stdout)
	}

	expected, err := foldAccounting(fixtureLedger)
	if err != nil {
		t.Fatalf("fold independent fixture ledger: %v", err)
	}
	if err := compareAccounting(expected, captured); err != nil {
		t.Fatal(err)
	}

	textSeries := expected.series[metrics.SeriesKey{Direction: metrics.DirectionOutput, Modality: metrics.ModalityText}]
	if textSeries.eventCount == 0 || textSeries.totalBytes == 0 {
		t.Fatalf("output/text must be non-empty, got %+v", textSeries)
	}
	audioSeries := expected.series[metrics.SeriesKey{Direction: metrics.DirectionOutput, Modality: metrics.ModalityAudio}]
	if audioSeries.eventCount == 0 || audioSeries.totalBytes == 0 {
		t.Fatalf("output/audio must be non-empty, got %+v", audioSeries)
	}
	for _, key := range []metrics.SeriesKey{
		{Direction: metrics.DirectionInput, Modality: metrics.ModalityAudio},
		{Direction: metrics.DirectionInput, Modality: metrics.ModalityText},
		{Direction: metrics.DirectionInput, Modality: metrics.ModalityImage},
		{Direction: metrics.DirectionOutput, Modality: metrics.ModalityImage},
	} {
		got := expected.series[key]
		if got.eventCount != 0 || got.totalBytes != 0 || got.histogram.SampleCount != 0 || got.histogram.ByteSum != 0 || got.histogram.OverflowCount != 0 || !reflect.DeepEqual(got.histogram.BucketCounts, make([]uint64, len(got.histogram.Bounds))) {
			t.Fatalf("supported but unobserved %s must be exact zero, got %+v", key, got)
		}
	}

	for name, wavData := range map[string][]byte{
		"--audio-out WAV":       audioOut,
		"duration artifact WAV": durationAudio,
	} {
		if got := wavPCM(t, name, wavData); !bytes.Equal(got, expectedPCM) {
			t.Fatalf("%s PCM does not exactly equal the fixture audio fold: got %d bytes, want %d", name, len(got), len(expectedPCM))
		}
	}
	if !strings.Contains(stdout, metricsReconcileText) {
		t.Fatalf("stdout does not carry the command's text delta %q: %q", metricsReconcileText, stdout)
	}
}

// TestSessionCommandMetricsReconcileMissingOutputTextDeltaFails is the
// negative control. It starts from the same successful CLI replay observation,
// removes exactly one non-empty output-text delta from the independent fixture
// ledger, and compares that corrupted expectation with the unchanged
// production final accounting using the same exact-equality verdict as the
// positive case.
func TestSessionCommandMetricsReconcileMissingOutputTextDeltaFails(t *testing.T) {
	fixturePath, _, stdout, fixtureLedger, captured, _, _, runErr := readCommandObservation(t)
	if runErr != nil {
		t.Fatalf("session command returned an error over hermetic replay fixture %s: %v", fixturePath, runErr)
	}
	if !strings.Contains(stdout, "[session closed: fixture_complete]") {
		t.Fatalf("command did not reach recorded terminal boundary before negative-control mutation, stdout=%q", stdout)
	}

	positiveExpected, err := foldAccounting(fixtureLedger)
	if err != nil {
		t.Fatalf("fold independent fixture ledger: %v", err)
	}
	if err := compareAccounting(positiveExpected, captured); err != nil {
		t.Fatalf("successful production final accounting is not a valid starting point for the negative control: %v", err)
	}

	mutatedLedger, removed := removeOneNonEmptyOutputTextDelta(t, fixtureLedger)
	if removed.text != metricsReconcileText {
		t.Fatalf("negative control removed text %q, want %q", removed.text, metricsReconcileText)
	}
	mutatedExpected, err := foldAccounting(mutatedLedger)
	if err != nil {
		t.Fatalf("fold oracle ledger after one missing output-text delta: %v", err)
	}
	if err := compareAccounting(mutatedExpected, captured); err == nil {
		t.Fatal("missing output-text delta unexpectedly reconciled with unchanged production accounting")
	} else {
		got := err.Error()
		for _, want := range []string{
			"output/text event_count: expected 0, actual 1",
			"output/text total_bytes: expected 0, actual 16",
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("negative-control mismatch missing %q: %v", want, err)
			}
		}
		t.Logf("negative control rejected as expected: %v", err)
	}
}

// TestSessionCommandFinalAccountingIsEmittedOnceOnError covers the same
// runtime-owned terminal value on a provider error path. The command error is
// allowed and is not used to manufacture the accounting result.
func TestSessionCommandFinalAccountingIsEmittedOnceOnError(t *testing.T) {
	fixturePath := gwtesting.SharedSessionFixturePath("session_failure_auth.session.json")
	runtimeObserver := &runtimeObservationCapture{}
	agentCLI, err := wire.InitializeMockAgentCLIWithPorts(
		wire.NewPortSwap(wire.PortToolExecutor, &mockToolExecutor{}),
		wire.NewPortSwap(wire.PortSessionRuntimeObserver, runtimeObserver),
	)
	if err != nil {
		t.Fatalf("initialize production CLI router: %v", err)
	}
	output := &bytes.Buffer{}
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(output)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{"session", "--replay", fixturePath})
	if runErr := rootCmd.ExecuteContext(context.Background()); runErr == nil {
		t.Fatal("auth failure replay unexpectedly succeeded")
	}

	observations := runtimeObserver.snapshot()
	terminalCount := 0
	var terminal *wire.SessionRuntimeObservation
	for index := range observations {
		observation := observations[index]
		if observation.Kind != wire.SessionRuntimeObservationTerminal {
			if observation.FinalAccounting != nil {
				t.Fatalf("non-terminal observation %q carried final accounting", observation.Kind)
			}
			continue
		}
		terminalCount++
		if terminal != nil {
			t.Fatal("error replay delivered more than one terminal observation")
		}
		terminal = &observation
	}
	if terminalCount != 1 || terminal == nil || terminal.FinalAccounting == nil {
		t.Fatalf("error replay terminal observations = %d, terminal accounting = %#v", terminalCount, terminal)
	}
	if terminal.Clean || terminal.Error == "" {
		t.Fatalf("error replay terminal observation = %#v, want an error result", *terminal)
	}
	if !reflect.DeepEqual(terminal.FinalAccounting.Metrics.HistogramBounds, metrics.DefaultHistogramBounds()) {
		t.Fatalf("error replay histogram bounds = %v, want %v", terminal.FinalAccounting.Metrics.HistogramBounds, metrics.DefaultHistogramBounds())
	}
	if got, want := len(terminal.FinalAccounting.Metrics.Series), len(metrics.SupportedDirections())*len(metrics.SupportedModalities()); got != want {
		t.Fatalf("error replay series = %d, want all %d supported series", got, want)
	}
	for _, direction := range metrics.SupportedDirections() {
		for _, modality := range metrics.SupportedModalities() {
			series := terminal.FinalAccounting.Metrics.SeriesFor(direction, modality)
			if !reflect.DeepEqual(series.Histogram.Bounds, metrics.DefaultHistogramBounds()) || !reflect.DeepEqual(series.Histogram.BucketCounts, make([]uint64, len(metrics.DefaultHistogramBounds()))) || series.EventCount != 0 || series.TotalBytes != 0 || series.Histogram.SampleCount != 0 || series.Histogram.ByteSum != 0 || series.Histogram.OverflowCount != 0 {
				t.Fatalf("auth error series %s = %#v, want zero accounting", metrics.SeriesKey{Direction: direction, Modality: modality}, series)
			}
		}
	}
}
