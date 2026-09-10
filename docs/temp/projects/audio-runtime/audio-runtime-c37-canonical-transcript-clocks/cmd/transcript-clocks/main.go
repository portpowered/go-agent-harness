package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

var (
	baseTimestamp = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	advanceDelta  = 20 * time.Millisecond
)

type observation struct {
	Side      string `json:"side"`
	Tick      uint64 `json:"tick"`
	Timestamp string `json:"timestamp"`
	Peer      string `json:"peer"`
	Direction string `json:"direction"`
	Stream    string `json:"stream"`
	Payload   string `json:"payload_hex"`
}

type memorySink struct {
	records []transcript.Record
	err     error
}

func (sink *memorySink) Write(record transcript.Record) error {
	if sink.err != nil {
		return sink.err
	}
	record.Payload = append([]byte(nil), record.Payload...)
	sink.records = append(sink.records, record)
	return nil
}

type timestampOnlyClock struct{ timestamp time.Time }

func (clock timestampOnlyClock) Now() time.Time { return clock.timestamp }

type panicClock struct{}

func (panicClock) Now() time.Time { panic("supplied clock was read with a nil sink") }

type websocketMessage struct {
	messageType int
	payload     []byte
}

type testWebSocket struct {
	incoming   []websocketMessage
	sent       []websocketMessage
	sendErr    error
	receiveErr error
	closeErr   error
	closeCalls int
}

func (socket *testWebSocket) WriteMessage(messageType int, payload []byte) error {
	socket.sent = append(socket.sent, websocketMessage{
		messageType: messageType,
		payload:     append([]byte(nil), payload...),
	})
	return socket.sendErr
}

func (socket *testWebSocket) ReadMessage() (int, []byte, error) {
	if socket.receiveErr != nil {
		return 0, nil, socket.receiveErr
	}
	if len(socket.incoming) == 0 {
		return 0, nil, io.EOF
	}
	message := socket.incoming[0]
	socket.incoming = socket.incoming[1:]
	return message.messageType, append([]byte(nil), message.payload...), nil
}

func (socket *testWebSocket) Close() error {
	socket.closeCalls++
	return socket.closeErr
}

type partialWriter struct {
	accepted int
	err      error
	seen     []byte
}

func (writer *partialWriter) Write(payload []byte) (int, error) {
	writer.seen = append([]byte(nil), payload...)
	return writer.accepted, writer.err
}

type mutatingWriter struct{}

func (mutatingWriter) Write(payload []byte) (int, error) {
	for index := range payload {
		payload[index] = 0
	}
	return len(payload), nil
}

func main() {
	action := flag.String("action", "all", "verification action")
	flag.Parse()

	var result map[string]any
	var err error
	switch *action {
	case "all":
		result, err = runAll()
	case "timing":
		result, err = runTiming()
	case "mutate-timestamp":
		err = runTimestampMutation()
	case "mutate-tick":
		err = runTickMutation()
	case "parity":
		result, err = runParity()
	case "hang-control":
		err = runHangControl()
	default:
		err = fmt.Errorf("unknown action %q", *action)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if result != nil {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(result); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func runAll() (map[string]any, error) {
	timing, err := runTiming()
	if err != nil {
		return nil, err
	}
	parity, err := runParity()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"action": "all",
		"ok":     true,
		"timing": timing,
		"parity": parity,
	}, nil
}

func runTiming() (map[string]any, error) {
	shared := clock.NewDeterministic(baseTimestamp, advanceDelta)
	clientSink := &memorySink{}
	agentSink := &memorySink{}
	metadata := func() (uint64, time.Time) { return shared.Tick(), shared.Now() }
	client := transcript.NewClientCapture(clientSink, metadata)
	agent := transcript.NewAgentCapture(agentSink, shared)

	input := client.WrapDeviceInput(bytes.NewReader([]byte{0x00, 0xff, 0x01}))
	buffer := make([]byte, 3)
	if n, err := input.Read(buffer); n != len(buffer) || err != nil {
		return nil, fmt.Errorf("timing client input = (%d, %v), want (3, nil)", n, err)
	}
	if n, err := agent.Inbound(transcript.StreamWS, []byte{0x10, 0x00}, completeAgentBoundary); n != 2 || err != nil {
		return nil, fmt.Errorf("timing agent ingress = (%d, %v), want (2, nil)", n, err)
	}
	if shared.Advance() != 1 {
		return nil, errors.New("timing deterministic clock did not advance to tick 1")
	}
	socket := &testWebSocket{}
	if err := client.WrapWebSocket(socket).WriteMessage(2, []byte{0x7f, 0x80}); err != nil {
		return nil, fmt.Errorf("timing client websocket send: %w", err)
	}
	if n, err := agent.Outbound(transcript.StreamWS, []byte{0x01, 0xfe}, completeAgentBoundary); n != 2 || err != nil {
		return nil, fmt.Errorf("timing agent egress = (%d, %v), want (2, nil)", n, err)
	}

	observations := make([]observation, 0, len(clientSink.records)+len(agentSink.records))
	for _, record := range clientSink.records {
		observations = append(observations, toObservation("client", record))
	}
	for _, record := range agentSink.records {
		observations = append(observations, toObservation("agent", record))
	}
	if err := verifyTimingObservations(observations); err != nil {
		return nil, err
	}

	defaultControls, err := defaultTimingControls()
	if err != nil {
		return nil, err
	}
	nowOnly, err := nowOnlyControl()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"action":           "timing",
		"ok":               true,
		"base":             baseTimestamp.Format(time.RFC3339Nano),
		"advance_delta":    advanceDelta.String(),
		"observations":     observations,
		"default_controls": defaultControls,
		"now_only":         nowOnly,
	}, nil
}

func verifyTimingObservations(observations []observation) error {
	if len(observations) != 4 {
		return fmt.Errorf("timing observations = %d, want 4", len(observations))
	}
	want := []observation{
		{Side: "client", Tick: 0, Timestamp: "2026-01-02T03:04:05Z", Peer: "client", Direction: "in", Stream: "device-in", Payload: "00ff01"},
		{Side: "client", Tick: 1, Timestamp: "2026-01-02T03:04:05.02Z", Peer: "client", Direction: "out", Stream: "ws", Payload: "7f80"},
		{Side: "agent", Tick: 0, Timestamp: "2026-01-02T03:04:05Z", Peer: "agent", Direction: "in", Stream: "ws", Payload: "1000"},
		{Side: "agent", Tick: 1, Timestamp: "2026-01-02T03:04:05.02Z", Peer: "agent", Direction: "out", Stream: "ws", Payload: "01fe"},
	}
	for index, actual := range observations {
		if actual != want[index] {
			return fmt.Errorf("timing observation %d = %+v, want %+v", index, actual, want[index])
		}
	}
	return nil
}

func defaultTimingControls() (map[string]any, error) {
	clientSink := &memorySink{}
	client := transcript.NewClientCapture(clientSink, nil)
	reader := client.WrapDeviceInput(bytes.NewReader([]byte("default-client")))
	buffer := make([]byte, len("default-client"))
	before := time.Now().UTC()
	if n, err := reader.Read(buffer); n != len(buffer) || err != nil {
		return nil, fmt.Errorf("default client read = (%d, %v)", n, err)
	}
	after := time.Now().UTC()
	if len(clientSink.records) != 1 || clientSink.records[0].Tick != 0 {
		return nil, fmt.Errorf("default client metadata = %+v, want one tick-zero record", clientSink.records)
	}
	clientTimestamp, err := time.Parse(time.RFC3339Nano, clientSink.records[0].Timestamp)
	if err != nil || clientTimestamp.Before(before) || clientTimestamp.After(after) || clientTimestamp.Location() != time.UTC {
		return nil, fmt.Errorf("default client timestamp = %q, want UTC host time", clientSink.records[0].Timestamp)
	}

	agentSink := &memorySink{}
	agent := transcript.NewAgentCapture(agentSink, nil)
	for _, payload := range [][]byte{[]byte("one"), []byte("two")} {
		if _, err := agent.Inbound(transcript.StreamWS, payload, completeAgentBoundary); err != nil {
			return nil, fmt.Errorf("default agent inbound: %w", err)
		}
	}
	if len(agentSink.records) != 2 || agentSink.records[0].Tick != 1 || agentSink.records[1].Tick != 2 {
		return nil, fmt.Errorf("default agent ticks = %+v, want [1 2]", agentSink.records)
	}
	return map[string]any{
		"client_tick":        clientSink.records[0].Tick,
		"client_timestamp":   clientSink.records[0].Timestamp,
		"agent_ticks":        []uint64{agentSink.records[0].Tick, agentSink.records[1].Tick},
		"host_timestamp_utc": true,
	}, nil
}

func nowOnlyControl() (map[string]any, error) {
	want := time.Date(2026, time.January, 2, 3, 4, 5, 123000000, time.UTC)
	sink := &memorySink{}
	agent := transcript.NewAgentCapture(sink, timestampOnlyClock{timestamp: want})
	if _, err := agent.Inbound(transcript.StreamWS, []byte("now-only"), completeAgentBoundary); err != nil {
		return nil, err
	}
	if len(sink.records) != 1 || sink.records[0].Tick != 1 || sink.records[0].Timestamp != want.Format(time.RFC3339Nano) {
		return nil, fmt.Errorf("now-only result = %+v", sink.records)
	}
	return map[string]any{
		"tick":      sink.records[0].Tick,
		"timestamp": sink.records[0].Timestamp,
	}, nil
}

func runTimestampMutation() error {
	result, err := runTiming()
	if err != nil {
		return err
	}
	observations := result["observations"].([]observation)
	if observations[0].Timestamp == "2026-01-02T03:04:05.001Z" {
		return errors.New("expected timestamp mismatch was not detected")
	}
	return fmt.Errorf("expected timestamp mismatch: got %q want %q", observations[0].Timestamp, "2026-01-02T03:04:05.001Z")
}

func runTickMutation() error {
	result, err := runTiming()
	if err != nil {
		return err
	}
	observations := result["observations"].([]observation)
	if observations[0].Tick == 99 {
		return errors.New("expected tick mismatch was not detected")
	}
	return fmt.Errorf("expected tick mismatch: got %d want %d", observations[0].Tick, 99)
}

func runParity() (map[string]any, error) {
	partialError := errors.New("partial live write")
	partialSink := &memorySink{}
	partialClient := transcript.NewClientCapture(partialSink, fixedMetadata(7, baseTimestamp))
	partial := &partialWriter{accepted: 2, err: partialError}
	partialPayload := []byte{0x01, 0xff, 0x02, 0xfe}
	partialCount, partialResultError := partialClient.WrapDeviceOutput(partial).Write(partialPayload)
	if partialCount != 2 || !errors.Is(partialResultError, partialError) || len(partialSink.records) != 1 ||
		!bytes.Equal(partialSink.records[0].Payload, partialPayload[:2]) {
		return nil, fmt.Errorf("client partial result = (%d, %v), records=%+v", partialCount, partialResultError, partialSink.records)
	}

	zeroSink := &memorySink{}
	zeroClient := transcript.NewClientCapture(zeroSink, fixedMetadata(8, baseTimestamp))
	zeroCount, zeroError := zeroClient.WrapDeviceOutput(&partialWriter{accepted: 0}).Write([]byte("zero"))
	if zeroCount != 0 || zeroError != nil || len(zeroSink.records) != 0 {
		return nil, fmt.Errorf("client zero result = (%d, %v), records=%d", zeroCount, zeroError, len(zeroSink.records))
	}

	rejectedError := errors.New("websocket rejected")
	rejectedSink := &memorySink{}
	rejectedClient := transcript.NewClientCapture(rejectedSink, fixedMetadata(9, baseTimestamp))
	rejectedSocket := &testWebSocket{sendErr: rejectedError}
	rejectedResultError := rejectedClient.WrapWebSocket(rejectedSocket).WriteMessage(1, []byte("rejected"))
	if !errors.Is(rejectedResultError, rejectedError) || len(rejectedSink.records) != 0 {
		return nil, fmt.Errorf("client rejected result = %v, records=%d", rejectedResultError, len(rejectedSink.records))
	}

	closeError := errors.New("close failed")
	closeSocket := &testWebSocket{closeErr: closeError}
	closeClient := transcript.NewClientCapture(nil, nil)
	if err := closeClient.WrapWebSocket(closeSocket).Close(); !errors.Is(err, closeError) || closeSocket.closeCalls != 1 {
		return nil, fmt.Errorf("client close result = (%v, %d)", err, closeSocket.closeCalls)
	}

	copySink := &memorySink{}
	copyClient := transcript.NewClientCapture(copySink, fixedMetadata(10, baseTimestamp))
	copyPayload := []byte{0x11, 0x00, 0xee}
	if n, err := copyClient.WrapDeviceOutput(mutatingWriter{}).Write(copyPayload); n != len(copyPayload) || err != nil {
		return nil, fmt.Errorf("client mutation live result = (%d, %v)", n, err)
	}
	if !bytes.Equal(copySink.records[0].Payload, []byte{0x11, 0x00, 0xee}) {
		return nil, fmt.Errorf("client mutation record = %x", copySink.records[0].Payload)
	}

	agentSink := &memorySink{}
	agent := transcript.NewAgentCapture(agentSink, fixedAgentClock{timestamp: baseTimestamp})
	agentPartialError := errors.New("agent partial")
	agentPartialCount, agentPartialResultError := agent.Outbound(transcript.StreamWS, []byte("agent"), func(payload []byte) (int, error) {
		return len(payload) - 1, agentPartialError
	})
	if agentPartialCount != 4 || !errors.Is(agentPartialResultError, agentPartialError) || len(agentSink.records) != 0 {
		return nil, fmt.Errorf("agent partial result = (%d, %v), records=%d", agentPartialCount, agentPartialResultError, len(agentSink.records))
	}
	if n, err := agent.Outbound(transcript.StreamWS, []byte("agent"), completeAgentBoundary); n != 5 || err != nil || len(agentSink.records) != 1 {
		return nil, fmt.Errorf("agent complete result = (%d, %v), records=%d", n, err, len(agentSink.records))
	}

	reporterCalls := 0
	degraded := &memorySink{err: errors.New("sink degraded")}
	degradedAgent := transcript.NewAgentCaptureWithReporter(degraded, fixedAgentClock{timestamp: baseTimestamp}, func(error) { reporterCalls++ })
	if _, err := degradedAgent.Inbound(transcript.StreamWS, []byte("one"), completeAgentBoundary); err != nil {
		return nil, fmt.Errorf("degraded agent inbound: %w", err)
	}
	if _, err := degradedAgent.Inbound(transcript.StreamWS, []byte("two"), completeAgentBoundary); err != nil {
		return nil, fmt.Errorf("degraded agent second inbound: %w", err)
	}
	if reporterCalls != 1 {
		return nil, fmt.Errorf("degraded reporter calls = %d, want 1", reporterCalls)
	}

	if err := nilSinkNoReadControl(); err != nil {
		return nil, err
	}
	return map[string]any{
		"action": "parity",
		"ok":     true,
		"client_partial": map[string]any{
			"accepted":    partialCount,
			"payload_hex": fmt.Sprintf("%x", partialSink.records[0].Payload),
		},
		"client_zero_record_count":     len(zeroSink.records),
		"client_rejected_record_count": len(rejectedSink.records),
		"client_close_calls":           closeSocket.closeCalls,
		"client_copy_payload_hex":      fmt.Sprintf("%x", copySink.records[0].Payload),
		"agent_partial_record_count":   0,
		"agent_complete_record_count":  len(agentSink.records),
		"agent_reporter_calls":         reporterCalls,
		"nil_sink_clock_reads":         0,
		"close_error_authoritative":    true,
		"sink_failure_isolated":        true,
	}, nil
}

func nilSinkNoReadControl() error {
	agent := transcript.NewAgentCapture(nil, panicClock{})
	if n, err := agent.Inbound(transcript.StreamWS, []byte("in"), completeAgentBoundary); n != 2 || err != nil {
		return fmt.Errorf("nil-sink agent inbound = (%d, %v)", n, err)
	}
	if n, err := agent.Outbound(transcript.StreamWS, []byte("out"), completeAgentBoundary); n != 3 || err != nil {
		return fmt.Errorf("nil-sink agent outbound = (%d, %v)", n, err)
	}
	client := transcript.NewClientCapture(nil, func() (uint64, time.Time) {
		panic("nil-sink metadata was read")
	})
	if n, err := client.WrapDeviceInput(bytes.NewReader([]byte("in"))).Read(make([]byte, 2)); n != 2 || err != nil {
		return fmt.Errorf("nil-sink client input = (%d, %v)", n, err)
	}
	if n, err := client.WrapDeviceOutput(io.Discard).Write([]byte("out")); n != 3 || err != nil {
		return fmt.Errorf("nil-sink client output = (%d, %v)", n, err)
	}
	return nil
}

func runHangControl() error {
	child := exec.Command("sh", "-c", "trap '' TERM; sleep 120")
	child.Stdout = io.Discard
	child.Stderr = io.Discard
	if err := child.Start(); err != nil {
		return fmt.Errorf("start hang child: %w", err)
	}
	fmt.Fprintf(os.Stdout, "hang child pid=%d\n", child.Process.Pid)
	select {}
}

func completeAgentBoundary(payload []byte) (int, error) { return len(payload), nil }

func fixedMetadata(tick uint64, timestamp time.Time) transcript.ClientMetadata {
	return func() (uint64, time.Time) { return tick, timestamp }
}

type fixedAgentClock struct{ timestamp time.Time }

func (clock fixedAgentClock) Now() time.Time { return clock.timestamp }

func toObservation(side string, record transcript.Record) observation {
	return observation{
		Side:      side,
		Tick:      record.Tick,
		Timestamp: record.Timestamp,
		Peer:      string(record.Peer),
		Direction: string(record.Direction),
		Stream:    string(record.Stream),
		Payload:   fmt.Sprintf("%x", record.Payload),
	}
}
