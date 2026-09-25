package wire

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	wireSendKind    = sessiontrace.SessionRuntimeObservationKind("provider_wire_send")
	wireReceiveKind = sessiontrace.SessionRuntimeObservationKind("provider_wire_receive")
	fixtureName     = "fixture.json"
)

// recordingObserver captures runtime observations and optional preferences.
type recordingObserver struct {
	mu                 sync.Mutex
	observations       []sessiontrace.SessionRuntimeObservation
	providerBoundaries bool
	retainCommit       bool
}

func (o *recordingObserver) ObserveSessionRuntime(observation sessiontrace.SessionRuntimeObservation) {
	o.mu.Lock()
	o.observations = append(o.observations, observation)
	o.mu.Unlock()
}

func (o *recordingObserver) ObserveProviderBoundaries() bool { return o.providerBoundaries }
func (o *recordingObserver) RetainCommitPayload() bool       { return o.retainCommit }

func (o *recordingObserver) snapshot() []sessiontrace.SessionRuntimeObservation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]sessiontrace.SessionRuntimeObservation(nil), o.observations...)
}

// scriptedConn answers one read with a fixed frame and fails the second write.
type scriptedConn struct {
	readType    int
	readPayload []byte
	writes      int
}

func (c *scriptedConn) ReadMessage() (int, []byte, error) { return c.readType, c.readPayload, nil }

func (c *scriptedConn) WriteMessage(int, []byte) error {
	c.writes++
	if c.writes > 1 {
		return errors.New("socket closed")
	}
	return nil
}

func (*scriptedConn) Close() error { return nil }

type scriptedDialer struct{ conn *scriptedConn }

func (d scriptedDialer) Dial(string, map[string]string) (transport.Conn, error) { return d.conn, nil }

func TestProviderWireDialerRecordsBothDirectionsWithPayloadShape(t *testing.T) {
	observer := &recordingObserver{}
	conn := &scriptedConn{readType: 2, readPayload: []byte{0xff, 0x00}}
	dialer := NewProviderWireDialer(scriptedDialer{conn: conn}, observer, clock.Real{})
	wrapped, err := dialer.Dial("wss://provider", nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if err := wrapped.WriteMessage(1, []byte(`{"type":"session.update"}`)); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := wrapped.WriteMessage(1, []byte(`{"type":"response.create"}`)); err == nil {
		t.Fatal("second write must surface the transport failure")
	}
	if _, _, err := wrapped.ReadMessage(); err != nil {
		t.Fatalf("read: %v", err)
	}
	observations := observer.snapshot()
	if len(observations) != 3 {
		t.Fatalf("observations = %d, want one per wire message", len(observations))
	}
	var sent struct {
		MessageType int             `json:"message_type"`
		Payload     json.RawMessage `json:"payload"`
	}
	if observations[0].Kind != wireSendKind || !observations[0].Clean || json.Unmarshal(observations[0].Payload, &sent) != nil || sent.MessageType != 1 || !strings.Contains(string(sent.Payload), "session.update") {
		t.Fatalf("send observation = %+v", observations[0])
	}
	if observations[1].Clean || !strings.Contains(observations[1].Error, "socket closed") {
		t.Fatalf("failed send observation = %+v, want the transport error", observations[1])
	}
	var received struct {
		BinaryPayload []byte `json:"binary_payload"`
	}
	if observations[2].Kind != wireReceiveKind || json.Unmarshal(observations[2].Payload, &received) != nil || len(received.BinaryPayload) != 2 {
		t.Fatalf("binary receive observation = %+v", observations[2])
	}
	if got := NewProviderWireDialer(nil, observer, clock.Real{}); got != nil {
		t.Fatalf("dialer without an inner transport = %v, want nil", got)
	}
	inner := scriptedDialer{conn: conn}
	if got := NewProviderWireDialer(inner, nil, clock.Real{}); got != inner {
		t.Fatalf("dialer without an observer = %v, want the undecorated transport", got)
	}
}

type fixtureInspector struct {
	inspection replay.CaptureInspection
	err        error
}

func (i fixtureInspector) InspectCapture(context.Context, string) (replay.CaptureInspection, error) {
	return i.inspection, i.err
}

func TestReplayMetricsCollectorMergesReportedAndObservedSeries(t *testing.T) {
	runner := func(context.Context, string, string) (metrics.Snapshot, error) {
		return metrics.Snapshot{Series: []metrics.SeriesSnapshot{{Direction: metrics.DirectionOutput, Modality: metrics.ModalityText, TotalBytes: 5}}}, nil
	}
	inspector := fixtureInspector{inspection: replay.CaptureInspection{Facts: replay.CaptureFacts{MetricDeltas: []replay.CaptureMetricDelta{
		{Direction: metrics.DirectionOutput, Modality: metrics.ModalityText, Bytes: 3},
		{Direction: metrics.DirectionOutput, Modality: metrics.ModalityText, Bytes: 2},
		{Direction: metrics.DirectionInput, Modality: metrics.ModalityAudio, Bytes: 7},
	}}}}
	collector := NewReplayMetricsCollector(sessiontrace.MetricsCollectorOptions{Clock: clock.Real{}, Runner: runner, ReplayInspector: inspector, FactoryReady: func() bool { return true }})
	series, err := collector.Collect(context.Background(), fixtureName, "prompt")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(series) != 2 || series[0].ReportedTotal != 5 || series[0].ObservedDeltas != 5 {
		t.Fatalf("reported series = %+v, want the output/text total paired with observed deltas", series)
	}
	if series[1].Direction != string(metrics.DirectionInput) || series[1].ObservedDeltas != 7 || series[1].ReportedTotal != 0 {
		t.Fatalf("observed-only series = %+v", series[1])
	}
}

func TestReplayMetricsCollectorRequiresDependencies(t *testing.T) {
	runErr := errors.New("replay failed")
	failingRunner := func(context.Context, string, string) (metrics.Snapshot, error) { return metrics.Snapshot{}, runErr }
	okRunner := func(context.Context, string, string) (metrics.Snapshot, error) { return metrics.Snapshot{}, nil }
	inspectErr := errors.New("missing fixture")
	cases := map[string]sessiontrace.MetricsCollectorOptions{
		"injected clock":          {},
		"session runtime factory": {Clock: clock.Real{}, FactoryReady: func() bool { return false }},
		"replay runner":           {Clock: clock.Real{}},
		"replay service":          {Clock: clock.Real{}, Runner: okRunner},
		"replay fixture.json":     {Clock: clock.Real{}, Runner: failingRunner, ReplayInspector: fixtureInspector{}},
		"inspect replay fixture":  {Clock: clock.Real{}, Runner: okRunner, ReplayInspector: fixtureInspector{err: inspectErr}},
	}
	for want, options := range cases {
		_, err := NewReplayMetricsCollector(options).Collect(context.Background(), fixtureName, "")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Collect with missing %q = %v", want, err)
		}
	}
}

func TestRuntimeRecorderHonorsObserverPreferencesAndClonesAccounting(t *testing.T) {
	observer := &recordingObserver{providerBoundaries: true, retainCommit: false}
	recorder := NewRuntimeRecorder(observer, clock.Real{})
	if !recorder.ObservesProviderBoundaries() {
		t.Fatal("recorder ignored the observer's provider-boundary preference")
	}
	accounting := &sessiontrace.SessionFinalAccounting{TotalTokens: 9, Metrics: metrics.Snapshot{
		HistogramBounds: []int64{1, 2},
		Series:          []metrics.SeriesSnapshot{{Direction: metrics.DirectionOutput, Modality: metrics.ModalityText, TotalBytes: 4}},
	}}
	recorder.TerminalWithAccounting(1, nil, accounting)
	accounting.Metrics.Series[0].TotalBytes = 99
	accounting.Metrics.HistogramBounds[0] = 99
	observations := observer.snapshot()
	final := observations[len(observations)-1]
	if final.FinalAccounting == nil || final.FinalAccounting.TotalTokens != 9 {
		t.Fatalf("terminal observation = %+v, want the final accounting", final)
	}
	if final.FinalAccounting.Metrics.Series[0].TotalBytes != 4 || final.FinalAccounting.Metrics.HistogramBounds[0] != 1 {
		t.Fatal("terminal accounting shares the caller's metric storage")
	}
	if recorder := NewRuntimeRecorder(nil, clock.Real{}); recorder != nil {
		t.Fatalf("recorder without an observer = %v, want none", recorder)
	}
}

func TestPreparedTraceChainsObserverPreferencesAndPublishes(t *testing.T) {
	observer := &recordingObserver{retainCommit: false}
	bundle := filepath.Join(t.TempDir(), "bundle")
	prepared, err := NewService().Prepare(sessiontrace.Request{RecordDirectory: bundle, Clock: clock.Real{}, RuntimeObserver: observer})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	chain := prepared.RuntimeObserver()
	boundaries, ok := chain.(sessiontrace.ProviderBoundaryObserver)
	if !ok || !boundaries.ObserveProviderBoundaries() {
		t.Fatal("trace chain must observe provider boundaries for the audio trace")
	}
	commit, ok := chain.(sessiontrace.CommitPayloadObserver)
	if !ok || commit.RetainCommitPayload() {
		t.Fatal("trace chain retained commit payloads although the host observer declined them")
	}
	payload := []byte("audio")
	accounting := &sessiontrace.SessionFinalAccounting{TotalTokens: 3}
	chain.ObserveSessionRuntime(sessiontrace.SessionRuntimeObservation{Kind: sessiontrace.SessionRuntimeObservationAudioInput, Payload: payload, FinalAccounting: accounting})
	payload[0] = 'X'
	accounting.TotalTokens = 99
	if got := observer.snapshot(); len(got) != 1 || string(got[0].Payload) != "audio" || got[0].FinalAccounting.TotalTokens != 3 {
		t.Fatalf("host observation = %+v, want owned payload and accounting copies", got)
	}
	recorder := prepared.WrapLiveRecorder(nil, session.LiveRequest{})
	if err := recorder.RecordMessage(context.Background(), session.LiveRecord{Direction: session.LiveRecordClient}); err != nil {
		t.Fatalf("wrapped RecordMessage: %v", err)
	}
	if got := observer.snapshot(); len(got) != 2 {
		t.Fatalf("host observations after a recorded message = %d, want the trace-wrapped recorder to publish it", len(got))
	}
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Finish(context.Background(), bundle, true); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bundle, "audio-trace")); err != nil {
		t.Fatalf("published trace missing from bundle: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := prepared.Finish(canceled, bundle, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("Finish after close with canceled context = %v", err)
	}
}

func TestUnresolvedToolResultsErrorOrdersIDsAndKeepsOwnedStatuses(t *testing.T) {
	statuses := map[string]messages.SessionSendStatus{"call-b": messages.SessionSendBufferFull, "stale": messages.SessionSendClosed}
	err := NewUnresolvedToolResultsError([]string{" call-b ", "call-a", "", "call-b"}, statuses)
	if got := err.UnresolvedCallIDs(); len(got) != 2 || got[0] != "call-a" || got[1] != "call-b" {
		t.Fatalf("call IDs = %v, want trimmed, deduplicated and ordered", got)
	}
	if _, kept := err.SendStatuses["stale"]; kept {
		t.Fatal("send statuses retained an ID outside the unresolved set")
	}
	statuses["call-b"] = messages.SessionSendSucceeded
	want := "tool results were not delivered for 2 unresolved call(s): call-a, call-b (send outcomes: call-b=" + string(messages.SessionSendBufferFull) + ")"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, sessiontrace.ErrUnresolvedToolResults) {
		t.Fatal("unresolved error lost its sentinel")
	}
	empty := NewUnresolvedToolResultsError(nil, nil)
	if empty.Error() != sessiontrace.ErrUnresolvedToolResults.Error() {
		t.Fatalf("empty unresolved error = %q", empty.Error())
	}
}

func TestCancellationIntentRecordsOperatorSIGINT(t *testing.T) {
	intent := NewCancellationIntent()
	if intent.SIGINTReceived() {
		t.Fatal("new cancellation intent reports SIGINT")
	}
	intent.MarkSIGINT()
	if !intent.SIGINTReceived() {
		t.Fatal("cancellation intent lost the operator SIGINT")
	}
}
