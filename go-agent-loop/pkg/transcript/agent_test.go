package transcript

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
)

func TestAgentCaptureRecordsFixedBidirectionalScenario(t *testing.T) {
	base := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.FixedZone("scenario", -7*60*60))
	logicalClock := clock.NewDeterministic(base, time.Millisecond)
	sink := &retainingRecordSink{}
	capture := NewAgentCapture(sink, logicalClock)

	type crossing struct {
		tick      uint64
		direction Direction
		stream    Stream
		payload   []byte
	}
	want := []crossing{
		{1, DirectionIn, StreamWS, []byte{0x00, 0xff, 0x01}},
		{2, DirectionOut, StreamWS, []byte("agent-text")},
		{3, DirectionIn, StreamRTCAudio, []byte{0x7f, 0x00, 0xc3}},
		{4, DirectionOut, StreamRTCAudio, []byte("audio-response")},
		{5, DirectionIn, StreamRTCData, []byte("client-data")},
		{6, DirectionOut, StreamRTCData, []byte{0xde, 0xad, 0xbe, 0xef}},
	}

	for _, event := range want {
		if got := logicalClock.AdvanceTo(event.tick); got != event.tick {
			t.Fatalf("AdvanceTo(%d) = %d", event.tick, got)
		}
		payload := append([]byte(nil), event.payload...)
		var count int
		var err error
		if event.direction == DirectionIn {
			count, err = capture.Inbound(event.stream, payload, func(data []byte) (int, error) {
				// Ingress must be captured before downstream processing mutates it.
				for index := range data {
					data[index] ^= 0xff
				}
				return len(data), nil
			})
		} else {
			count, err = capture.Outbound(event.stream, payload, func(data []byte) (int, error) {
				return len(data), nil
			})
		}
		if count != len(payload) || err != nil {
			t.Fatalf("crossing tick %d result = (%d, %v), want (%d, nil)", event.tick, count, err, len(payload))
		}
	}

	if len(sink.records) != len(want) {
		t.Fatalf("agent records = %d, want exactly %d", len(sink.records), len(want))
	}
	for index, event := range want {
		got := sink.records[index]
		wantRecord := NewRecord(event.tick, base.Add(time.Duration(event.tick)*time.Millisecond),
			PeerAgent, event.direction, event.stream, event.payload)
		if !recordsEqual(got, wantRecord) {
			t.Fatalf("agent record %d = %+v, want %+v", index, got, wantRecord)
		}
		if len(got.Payload) == 0 {
			t.Fatalf("agent record %d has empty payload", index)
		}
	}

	counts := map[Direction]int{}
	streams := map[Stream]int{}
	for _, record := range sink.records {
		if record.Peer != PeerAgent {
			t.Fatalf("record peer = %q, want %q", record.Peer, PeerAgent)
		}
		counts[record.Direction]++
		streams[record.Stream]++
	}
	if counts[DirectionIn] != 3 || counts[DirectionOut] != 3 {
		t.Fatalf("direction counts = %v, want three ingress and three egress records", counts)
	}
	for _, stream := range []Stream{StreamWS, StreamRTCAudio, StreamRTCData} {
		if streams[stream] != 2 {
			t.Fatalf("stream %q count = %d, want 2", stream, streams[stream])
		}
	}
}

func TestAgentCaptureCorrelatesWithClientCrossingsAndRejectsMutations(t *testing.T) {
	base := time.Unix(1_750_000_000, 123).UTC()
	logicalClock := clock.NewDeterministic(base, 10*time.Millisecond)
	sink := &retainingRecordSink{}
	capture := NewAgentCapture(sink, logicalClock)

	type crossing struct {
		tick   uint64
		stream Stream
		data   []byte
	}
	events := []crossing{
		{1, StreamWS, []byte("client-request-1")},
		{2, StreamRTCAudio, []byte{0x01, 0x00, 0xff}},
		{3, StreamRTCData, []byte("client-request-2")},
		{4, StreamWS, []byte("agent-response-1")},
		{5, StreamRTCAudio, []byte{0x80, 0x7f, 0x00}},
		{6, StreamRTCData, []byte("agent-response-2")},
	}
	client := make([]Record, 0, len(events))
	for index, event := range events {
		if got := logicalClock.AdvanceTo(event.tick); got != event.tick {
			t.Fatalf("AdvanceTo(%d) = %d", event.tick, got)
		}
		payload := append([]byte(nil), event.data...)
		if index < 3 {
			client = append(client, NewRecord(event.tick, logicalClock.Now(), PeerClient, DirectionOut, event.stream, payload))
			if _, err := capture.Inbound(event.stream, payload, nil); err != nil {
				t.Fatalf("Inbound tick %d: %v", event.tick, err)
			}
			continue
		}
		client = append(client, NewRecord(event.tick, logicalClock.Now(), PeerClient, DirectionIn, event.stream, payload))
		if _, err := capture.Outbound(event.stream, payload, func(data []byte) (int, error) {
			return len(data), nil
		}); err != nil {
			t.Fatalf("Outbound tick %d: %v", event.tick, err)
		}
	}

	if !agentClientCorrespondence(client, sink.records) {
		t.Fatalf("positive client/agent correspondence failed:\nclient=%+v\nagent=%+v", client, sink.records)
	}

	mutations := map[string]func([]Record) []Record{
		"delete": func(records []Record) []Record {
			return append([]Record(nil), records[:len(records)-1]...)
		},
		"duplicate": func(records []Record) []Record {
			mutated := append([]Record(nil), records...)
			return append(mutated, cloneRecord(records[0]))
		},
		"reorder": func(records []Record) []Record {
			mutated := append([]Record(nil), records...)
			mutated[0], mutated[1] = mutated[1], mutated[0]
			return mutated
		},
		"swap direction": func(records []Record) []Record {
			mutated := cloneRecords(records)
			mutated[0].Direction = DirectionOut
			return mutated
		},
		"shift tick": func(records []Record) []Record {
			mutated := cloneRecords(records)
			mutated[0].Tick++
			return mutated
		},
		"alter payload": func(records []Record) []Record {
			mutated := cloneRecords(records)
			mutated[0].Payload[0] ^= 0xff
			return mutated
		},
	}
	for name, mutate := range mutations {
		if got := agentClientCorrespondence(client, mutate(sink.records)); got {
			t.Errorf("mutation %q still passed correspondence", name)
		}
	}
}

func TestAgentCapturePreservesLiveResultsAndBoundaryBytes(t *testing.T) {
	transcriptErr := errors.New("transcript sink unavailable")
	liveErr := errors.New("provider boundary stopped")
	var reports []error
	sink := &retainingRecordSink{err: transcriptErr}
	logicalClock := clock.NewDeterministic(time.Unix(42, 0), time.Second)
	capture := NewAgentCaptureWithReporter(sink, logicalClock, func(err error) {
		reports = append(reports, err)
	})

	input := []byte("provider-ingress")
	var inboundBytes []byte
	inboundCount, inboundError := capture.Inbound(StreamWS, input, func(data []byte) (int, error) {
		inboundBytes = append(inboundBytes, data...)
		data[0] = 'X'
		return 7, liveErr
	})
	if inboundCount != 7 || inboundError != liveErr {
		t.Fatalf("Inbound result = (%d, %v), want (7, exact live error)", inboundCount, inboundError)
	}
	if !bytes.Equal(inboundBytes, []byte("provider-ingress")) {
		t.Fatalf("live ingress bytes = %q, want original bytes", inboundBytes)
	}

	egressInput := []byte("provider-egress")
	var egressBytes []byte
	egressCount, egressError := capture.Outbound(StreamRTCData, egressInput, func(data []byte) (int, error) {
		egressBytes = append(egressBytes, data...)
		data[0] = 'X'
		return len(data), liveErr
	})
	if egressCount != len(egressInput) || egressError != liveErr {
		t.Fatalf("Outbound result = (%d, %v), want (%d, exact live error)", egressCount, egressError, len(egressInput))
	}
	if !bytes.Equal(egressBytes, []byte("provider-egress")) {
		t.Fatalf("live egress bytes = %q, want original bytes", egressBytes)
	}
	if len(reports) != 1 || !errors.Is(reports[0], transcriptErr) {
		t.Fatalf("sink reports = %v, want one transcript error", reports)
	}
}

func TestAgentCaptureEnabledAndDisabledLivePathsAreEquivalent(t *testing.T) {
	liveErr := errors.New("live result")
	baseline := runAgentLiveScenario(t, NewAgentCapture(nil, clock.NewDeterministic(time.Unix(1, 0), time.Second)), liveErr)
	sink := &retainingRecordSink{}
	enabled := runAgentLiveScenario(t, NewAgentCapture(sink, clock.NewDeterministic(time.Unix(1, 0), time.Second)), liveErr)

	if baseline.inCount != enabled.inCount || baseline.inError != enabled.inError || baseline.inCalls != enabled.inCalls ||
		!bytes.Equal(baseline.inSeen, enabled.inSeen) || !bytes.Equal(baseline.inPayload, enabled.inPayload) {
		t.Fatalf("inbound enabled result differs:\nbaseline=%+v\nenabled=%+v", baseline, enabled)
	}
	if baseline.outCount != enabled.outCount || baseline.outError != enabled.outError || baseline.outCalls != enabled.outCalls ||
		!bytes.Equal(baseline.outSeen, enabled.outSeen) || !bytes.Equal(baseline.outPayload, enabled.outPayload) {
		t.Fatalf("outbound enabled result differs:\nbaseline=%+v\nenabled=%+v", baseline, enabled)
	}
	if len(sink.records) != 2 {
		t.Fatalf("enabled transcript records = %d, want 2", len(sink.records))
	}
}

func TestAgentCaptureDoesNotRecordPartialOrRejectedEgress(t *testing.T) {
	logicalClock := clock.NewDeterministic(time.Unix(0, 0), time.Second)
	sink := &retainingRecordSink{}
	capture := NewAgentCapture(sink, logicalClock)
	payload := []byte("complete-frame")
	liveErr := errors.New("partial boundary write")

	cases := []struct {
		name     string
		accepted int
		err      error
	}{
		{name: "rejected", accepted: 0, err: liveErr},
		{name: "partial", accepted: len(payload) - 1, err: nil},
		{name: "partial with error", accepted: len(payload) - 1, err: liveErr},
		{name: "complete", accepted: len(payload), err: nil},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			before := len(sink.records)
			count, err := capture.Outbound(StreamWS, payload, func(data []byte) (int, error) {
				if !bytes.Equal(data, payload) {
					t.Fatalf("live payload = %q, want %q", data, payload)
				}
				return test.accepted, test.err
			})
			if count != test.accepted || err != test.err {
				t.Fatalf("Outbound result = (%d, %v), want (%d, %v)", count, err, test.accepted, test.err)
			}
			if test.name == "complete" {
				if len(sink.records) != before+1 {
					t.Fatalf("complete egress records = %d, want one new record", len(sink.records)-before)
				}
				return
			}
			if len(sink.records) != before {
				t.Fatalf("%s egress was recorded despite incomplete acceptance", test.name)
			}
		})
	}
}

func TestAgentCaptureSupportsTransparentWriterAndSinkContracts(t *testing.T) {
	logicalClock := clock.NewDeterministic(time.Unix(9, 0), time.Second)
	sink := &retainingRecordSink{}
	capture := NewAgentCapture(sink, logicalClock)

	var live bytes.Buffer
	payload := []byte{0x00, 0xff, 0x7f}
	count, err := capture.Outbound(StreamRTCData, payload, &live)
	if err != nil || count != len(payload) {
		t.Fatalf("writer Outbound result = (%d, %v), want (%d, nil)", count, err, len(payload))
	}
	if !bytes.Equal(live.Bytes(), payload) {
		t.Fatalf("live writer bytes = %x, want %x", live.Bytes(), payload)
	}
	if len(sink.records) != 1 || !bytes.Equal(sink.records[0].Payload, payload) {
		t.Fatalf("writer transcript = %+v, want one opaque payload", sink.records)
	}

	if _, err := capture.Inbound(StreamWS, []byte("unsupported"), struct{}{}); !errors.Is(err, ErrUnsupportedAgentBoundary) {
		t.Fatalf("unsupported boundary error = %v, want ErrUnsupportedAgentBoundary", err)
	}
	if _, err := capture.Outbound(StreamWS, []byte("unsupported"), struct{}{}); !errors.Is(err, ErrUnsupportedAgentBoundary) {
		t.Fatalf("unsupported boundary error = %v, want ErrUnsupportedAgentBoundary", err)
	}
}

func agentClientCorrespondence(client, agent []Record) bool {
	if len(client) != len(agent) {
		return false
	}
	clientOut := filterRecords(client, PeerClient, DirectionOut)
	clientIn := filterRecords(client, PeerClient, DirectionIn)
	agentIn := filterRecords(agent, PeerAgent, DirectionIn)
	agentOut := filterRecords(agent, PeerAgent, DirectionOut)
	return pairedRecordsEqual(clientOut, agentIn) && pairedRecordsEqual(clientIn, agentOut)
}

func filterRecords(records []Record, peer Peer, direction Direction) []Record {
	filtered := make([]Record, 0, len(records))
	for _, record := range records {
		if record.Peer == peer && record.Direction == direction {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

func pairedRecordsEqual(left, right []Record) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Tick != right[index].Tick || left[index].Stream != right[index].Stream ||
			!bytes.Equal(left[index].Payload, right[index].Payload) {
			return false
		}
	}
	return true
}

type agentLiveScenarioResult struct {
	inCount   int
	inError   error
	inCalls   int
	inSeen    []byte
	inPayload []byte
	outCount   int
	outError   error
	outCalls   int
	outSeen    []byte
	outPayload []byte
}

func runAgentLiveScenario(t *testing.T, capture *AgentCapture, liveErr error) agentLiveScenarioResult {
	t.Helper()
	result := agentLiveScenarioResult{}
	inPayload := []byte("same-ingress")
	result.inCount, result.inError = capture.Inbound(StreamWS, inPayload, func(data []byte) (int, error) {
		result.inCalls++
		result.inSeen = append(result.inSeen, data...)
		data[0] = 'X'
		return 4, liveErr
	})
	result.inPayload = append([]byte(nil), inPayload...)

	outPayload := []byte("same-egress")
	result.outCount, result.outError = capture.Outbound(StreamRTCData, outPayload, func(data []byte) (int, error) {
		result.outCalls++
		result.outSeen = append(result.outSeen, data...)
		data[0] = 'X'
		return len(data), liveErr
	})
	result.outPayload = append([]byte(nil), outPayload...)
	return result
}

func cloneRecords(records []Record) []Record {
	cloned := make([]Record, len(records))
	for index, record := range records {
		cloned[index] = cloneRecord(record)
	}
	return cloned
}

type retainingRecordSink struct {
	mu      sync.Mutex
	records []Record
	err     error
}

func (sink *retainingRecordSink) Write(record Record) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.records = append(sink.records, record)
	return sink.err
}
