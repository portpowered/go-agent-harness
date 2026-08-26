package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

// The v7b vertical proves that s2s buffer overflow is never silent: every
// buffer-full drop increments a durable counter on the owning TypedBuffer,
// emits exactly one structured "buffer drop" log line, and the figures are
// reported by probe/scenario results so runs reconcile exactly against
// forced drops.
//
// The subject is an in-process duplex session with tiny input (client-to-
// provider send) and output (provider-to-client receive) buffers, following
// the same Session contract the provider sessions implement. Overflow is
// forced deterministically by writing past capacity with no consumer; no
// network, wall clock, or fixture replay is involved.

const dropProbeCapacity = 2

// dropProbeSession is the minimal duplex s2s session whose two directions own
// real TypedBuffers, so forced overflow lands on the same counting path used
// by production sessions.
type dropProbeSession struct {
	input  *messages.TypedBuffer[messages.StreamMessage]
	output *messages.TypedBuffer[messages.StreamMessage]
	done   chan struct{}
}

var (
	_ messages.Session             = (*dropProbeSession)(nil)
	_ messages.SessionDropCounters = (*dropProbeSession)(nil)
)

func newDropProbeSession() *dropProbeSession {
	return &dropProbeSession{
		input:  messages.NewTypedBuffer[messages.StreamMessage](dropProbeCapacity),
		output: messages.NewTypedBuffer[messages.StreamMessage](dropProbeCapacity),
		done:   make(chan struct{}),
	}
}

func (s *dropProbeSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.input.WriteContext(ctx, msg).OK()
}

func (s *dropProbeSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.output
}

func (s *dropProbeSession) Done() <-chan struct{} { return s.done }

func (s *dropProbeSession) Close() error {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	return nil
}

func (s *dropProbeSession) InputDrops() int64  { return s.input.Drops() }
func (s *dropProbeSession) OutputDrops() int64 { return s.output.Drops() }

// wireDefaultDropObservers attaches the canonical default observers to both
// directions, mirroring the production session wiring in
// go-llm-gateway/pkg/providers.AttachSessionDropLoggers.
func wireDefaultDropObservers(session *dropProbeSession, sink messages.DropLogSink) {
	streamKind := func(m messages.StreamMessage) string { return string(m.Type) }
	messages.AttachDefaultDropObserver(sink, messages.DropDirectionInput, "send_queue", session.input, streamKind)
	messages.AttachDefaultDropObserver(sink, messages.DropDirectionOutput, "receive_queue", session.output, streamKind)
}

// captureDropSink records every Warn call for exact drop-line assertions.
type captureDropSink struct {
	warn [][]messages.DropLogField
}

func (c *captureDropSink) Warn(msg string, fields ...messages.DropLogField) {
	if msg != messages.DropLogMessage {
		return
	}
	c.warn = append(c.warn, append([]messages.DropLogField(nil), fields...))
}

func (c *captureDropSink) records() [][]messages.DropLogField {
	return c.warn
}

func dropRecordField(record []messages.DropLogField, key string) (any, bool) {
	for _, field := range record {
		if field.Key == key {
			return field.Value, true
		}
	}
	return nil, false
}

// dropProbeResultLine decodes the single scenario JSONL record emitted by the
// runner (all lines before the trailing summary object).
func dropProbeResultLines(t *testing.T, out *bytes.Buffer) []map[string]any {
	t.Helper()
	var results []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var value map[string]any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatalf("decode probe JSONL line %q: %v", line, err)
		}
		if _, isSummary := value["status"]; !isSummary {
			results = append(results, value)
		}
	}
	return results
}

func dropProbeScenario(id string) probe.Scenario {
	expectations := []probe.ExpectedBehavior{
		{Type: probe.ExpectTerminalReason, Kind: probe.ExpectTerminalReason, Value: "synthetic"},
	}
	return probe.Scenario{
		ID:               id,
		Name:             id,
		Description:      "v7b buffer-drop observability vertical",
		Steps:            []probe.Step{{Type: probe.StepSendText, Text: "overflow probe"}, {Type: probe.StepClose}},
		Expectations:     expectations,
		Expected:         expectations,
		ExpectedBehavior: expectations,
	}
}

func TestS2SV7BForcedOverflowBothDirectionsReconcilesWithReportedCounts(t *testing.T) {
	session := newDropProbeSession()
	sink := &captureDropSink{}
	wireDefaultDropObservers(session, sink)

	ctx := context.Background()
	msg := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("audio-ish")}

	// Force input-path overflow: one more send than the queue holds.
	for range dropProbeCapacity + 1 {
		session.Send(ctx, msg) //nolint:errcheck // final send deliberately drops
	}
	if got := session.InputDrops(); got != 1 {
		t.Fatalf("forced input overflow produced InputDrops()=%d, want 1", got)
	}

	// Force output-path overflow: one more receive-side write than fits.
	for range dropProbeCapacity + 1 {
		session.Receive().Write(ctx, msg) //nolint:errcheck // final write deliberately drops
	}
	if got := session.OutputDrops(); got != 1 {
		t.Fatalf("forced output overflow produced OutputDrops()=%d, want 1", got)
	}

	exec := func(ctx context.Context, scenario probe.Scenario) (probe.ObservationSnapshot, error) {
		return probe.ObservationSnapshot{
			InputDrops:      uint64(session.InputDrops()),
			OutputDrops:     uint64(session.OutputDrops()),
			TerminalReason:  "synthetic",
			FrameCount:      1,
			ObservedTick:    1,
			HasObservedTick: true,
		}, nil
	}
	var out bytes.Buffer
	runner := &probe.Runner{Exec: exec, Out: &out}
	summary, err := runner.Run(ctx, []probe.Scenario{dropProbeScenario("s2s-v7b-buffer-drops-forced")})
	if err != nil {
		t.Fatalf("run probe: %v", err)
	}
	if summary.Passed != 1 || summary.Status != probe.StatusPass {
		t.Fatalf("run summary = %+v, want the single scenario to pass", summary)
	}

	results := dropProbeResultLines(t, &out)
	if len(results) != 1 {
		t.Fatalf("decoded %d result lines, want 1", len(results))
	}
	result := results[0]
	// The reported figures must reconcile EXACTLY with the forced drops.
	if result["input_drop_count"] != float64(1) {
		t.Errorf("input_drop_count = %v, want exactly the 1 forced input drop", result["input_drop_count"])
	}
	if result["output_drop_count"] != float64(1) {
		t.Errorf("output_drop_count = %v, want exactly the 1 forced output drop", result["output_drop_count"])
	}
	if result["pass"] != true {
		t.Errorf("pass = %v, want true: terminal expectation must hold on a dropping run", result["pass"])
	}

	// Exactly one structured line per drop, one per direction, greppable.
	records := sink.records()
	if len(records) != 2 {
		t.Fatalf("emitted %d drop log lines, want exactly one per drop (2)", len(records))
	}
	directions := map[string]bool{}
	for i, record := range records {
		if msgValue, ok := dropRecordField(record, "direction"); ok {
			directions[msgValue.(string)] = true
		} else {
			t.Errorf("drop line %d has no direction field", i)
		}
		if got, _ := dropRecordField(record, "count"); got != int64(1) {
			t.Errorf("drop line %d count = %v, want cumulative 1", i, got)
		}
		if got, _ := dropRecordField(record, "type"); got != string(messages.StreamTypeTextDelta) {
			t.Errorf("drop line %d type = %v, want %q", i, got, messages.StreamTypeTextDelta)
		}
	}
	if !directions[string(messages.DropDirectionInput)] || !directions[string(messages.DropDirectionOutput)] {
		t.Errorf("drop lines cover directions %v, want one input and one output", directions)
	}
}

func TestS2SV7BNormalTrafficReportsZeroDropsAndEmitsNoLines(t *testing.T) {
	session := newDropProbeSession()
	sink := &captureDropSink{}
	wireDefaultDropObservers(session, sink)

	ctx := context.Background()
	msg := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("fits")}
	for range dropProbeCapacity {
		if !session.Send(ctx, msg) {
			t.Fatal("in-capacity send failed")
		}
	}

	exec := func(ctx context.Context, scenario probe.Scenario) (probe.ObservationSnapshot, error) {
		return probe.ObservationSnapshot{
			InputDrops:      uint64(session.InputDrops()),
			OutputDrops:     uint64(session.OutputDrops()),
			TerminalReason:  "synthetic",
			FrameCount:      1,
			ObservedTick:    1,
			HasObservedTick: true,
		}, nil
	}
	var out bytes.Buffer
	runner := &probe.Runner{Exec: exec, Out: &out}
	if _, err := runner.Run(ctx, []probe.Scenario{dropProbeScenario("s2s-v7b-buffer-drops-clean")}); err != nil {
		t.Fatalf("run probe: %v", err)
	}

	results := dropProbeResultLines(t, &out)
	if len(results) != 1 {
		t.Fatalf("decoded %d result lines, want 1", len(results))
	}
	if results[0]["input_drop_count"] != float64(0) {
		t.Errorf("input_drop_count = %v, want explicit 0", results[0]["input_drop_count"])
	}
	if results[0]["output_drop_count"] != float64(0) {
		t.Errorf("output_drop_count = %v, want explicit 0", results[0]["output_drop_count"])
	}
	if records := sink.records(); len(records) != 0 {
		t.Fatalf("zero-drop run emitted %d drop log lines, want 0", len(records))
	}
}
