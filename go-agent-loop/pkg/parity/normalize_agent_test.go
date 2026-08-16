package parity

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

func TestNormalizeAgentRepresentativeProjection(t *testing.T) {
	records := agentFixtureRecords()
	input := encodeAgentRecords(t, records)

	got, err := NormalizeAgent("agent", input)
	if err != nil {
		t.Fatalf("NormalizeAgent: %v", err)
	}
	want := Projection{
		Turns: []TurnBoundary{
			{Order: 1, Tick: 0, Kind: "turn.start", Boundary: "start", ID: "turn-1", Role: "user", Payload: []byte(`{"kind":"turn.start","id":"turn-1","role":"user"}`)},
			{Order: 2, Tick: 7, Kind: "turn.end", Boundary: "end", ID: "turn-1", Role: "user", Payload: []byte(`{"kind":"turn.end","id":"turn-1","role":"user"}`)},
		},
		Audio: AudioSummary{
			FrameCount: 1,
			TotalBytes: 4,
			Frames:     []AudioFrame{{Tick: 1, Bytes: []byte{0, 1, 2, 255}, Payload: []byte(`{"kind":"audio.frame","bytes":[0,1,2,255]}`)}},
		},
		Transcripts: []TranscriptFact{
			{Order: 1, Tick: 2, Text: "  keep whitespace  ", Payload: []byte(`{"kind":"transcript","text":"  keep whitespace  ","extra":[1,1]}`)},
			{Order: 2, Tick: 3, Text: "  keep whitespace  ", Payload: []byte(`{"kind":"transcript","text":"  keep whitespace  ","extra":[1,1]}`)},
		},
		ToolCalls: []ToolCallFact{{
			Order: 1, ID: "tool-1", Name: "lookup", Arguments: []byte(`{"q":"a  b"}`), Result: []byte(`{"answer":"ok"}`),
			CallTick: 4, ResultTick: 5,
			CallPayload:   []byte(`{"kind":"tool.call","id":"tool-1","name":"lookup","arguments":{"q":"a  b"}}`),
			ResultPayload: []byte(`{"kind":"tool.result","id":"tool-1","result":{"answer":"ok"}}`),
		}},
		Interruptions: []InterruptionFact{{Order: 1, Tick: 6, Reason: "barge-in", Provenance: "client", Payload: []byte(`{"kind":"interrupt","reason":"barge-in","provenance":"client"}`)}},
		Terminal:      &TerminalOutcome{Tick: 8, Reason: "provider_close", Provenance: "provider", Payload: []byte(`{"kind":"terminal","reason":"provider_close","provenance":"provider"}`)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projection mismatch\n got: %#v\nwant: %#v", got, want)
	}
	if len(got.Transcripts) == 0 || got.Audio.FrameCount == 0 || got.Audio.TotalBytes == 0 || got.Terminal == nil {
		t.Fatalf("fixture did not prove non-empty evidence: %+v", got)
	}
}

func TestNormalizeAgentExclusionMutations(t *testing.T) {
	baseRecords := agentFixtureRecords()
	base, err := NormalizeAgent("agent", encodeAgentRecords(t, baseRecords))
	if err != nil {
		t.Fatalf("baseline NormalizeAgent: %v", err)
	}

	tests := []struct {
		name   string
		index  int
		mutate func(*transcript.Record)
	}{
		{
			name:  "wall-clock timestamp",
			index: 0,
			mutate: func(record *transcript.Record) {
				record.Timestamp = "2099-12-31T23:59:59.999999999Z"
			},
		},
		{
			name:  "transport identifier",
			index: 9,
			mutate: func(record *transcript.Record) {
				record.Payload = []byte(`{"kind":"transport.id","connection":"different-connection"}`)
			},
		},
		{
			name:  "transport segmentation",
			index: 10,
			mutate: func(record *transcript.Record) {
				record.Payload = []byte(`{"kind":"transport.segment","packet":999,"part":4}`)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			records := cloneAgentRecords(baseRecords)
			tt.mutate(&records[tt.index])
			got, err := NormalizeAgent("agent", encodeAgentRecords(t, records))
			if err != nil {
				t.Fatalf("NormalizeAgent: %v", err)
			}
			if !reflect.DeepEqual(got, base) {
				t.Fatalf("excluded field changed projection\n got: %#v\nwant: %#v", got, base)
			}
		})
	}
}

func TestNormalizeAgentRetainedMutationsChangeProjection(t *testing.T) {
	baseRecords := agentFixtureRecords()
	base, err := NormalizeAgent("agent", encodeAgentRecords(t, baseRecords))
	if err != nil {
		t.Fatalf("baseline NormalizeAgent: %v", err)
	}
	if len(base.Transcripts) == 0 || base.Transcripts[0].Tick == 0 {
		t.Fatalf("baseline lacks retained transcript evidence: %+v", base)
	}

	tests := []struct {
		name   string
		mutate func([]transcript.Record)
	}{
		{
			name: "logical tick",
			mutate: func(records []transcript.Record) {
				records[2].Tick = 200
			},
		},
		{
			name: "semantic content",
			mutate: func(records []transcript.Record) {
				records[2].Payload = []byte(`{"kind":"transcript","text":"changed","extra":[1,1]}`)
			},
		},
		{
			name: "correlation",
			mutate: func(records []transcript.Record) {
				records[4].Payload = []byte(`{"kind":"tool.call","id":"tool-2","name":"lookup","arguments":{"q":"a  b"}}`)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			records := cloneAgentRecords(baseRecords)
			tt.mutate(records)
			got, err := NormalizeAgent("agent", encodeAgentRecords(t, records))
			if err != nil {
				t.Fatalf("NormalizeAgent: %v", err)
			}
			if reflect.DeepEqual(got, base) {
				t.Fatalf("retained field mutation was erased: %#v", got)
			}
		})
	}
}

func TestNormalizeAgentDeterministicAndConcurrent(t *testing.T) {
	input := encodeAgentRecords(t, agentFixtureRecords())
	want, err := NormalizeAgent("agent", input)
	if err != nil {
		t.Fatalf("baseline NormalizeAgent: %v", err)
	}
	for i := 0; i < 20; i++ {
		got, err := NormalizeAgent("agent", input)
		if err != nil {
			t.Fatalf("repeat %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("repeat %d changed projection", i)
		}
	}

	const workers = 16
	results := make(chan Projection, workers)
	errorsCh := make(chan error, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer group.Done()
			got, err := NormalizeAgent("agent", input)
			if err != nil {
				errorsCh <- err
				return
			}
			results <- got
		}()
	}
	group.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatalf("concurrent NormalizeAgent: %v", err)
	}
	for got := range results {
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("concurrent result changed projection")
		}
	}

	owned, err := NormalizeAgent("agent", input)
	if err != nil {
		t.Fatalf("ownership NormalizeAgent: %v", err)
	}
	input[0] ^= 1
	if !reflect.DeepEqual(owned, want) {
		t.Fatalf("caller mutation affected completed projection: got=%#v want=%#v", owned, want)
	}
}

func TestNormalizeAgentS4Errors(t *testing.T) {
	valid := agentFixtureRecords()[0]
	tests := []struct {
		name       string
		input      []byte
		field      string
		reasonPart string
	}{
		{
			name:       "unknown projection kind",
			input:      encodeAgentRecords(t, []transcript.Record{agentRecord(1, transcript.DirectionIn, transcript.StreamWS, `{"kind":"future.event"}`)}),
			field:      "records[0].kind",
			reasonPart: "unknown record kind",
		},
		{
			name:       "missing required tick",
			input:      mutateAgentEnvelope(t, valid, func(fields map[string]json.RawMessage) { delete(fields, "tick") }),
			field:      "records[0].tick",
			reasonPart: "is required",
		},
		{
			name:       "wrong tick type",
			input:      mutateAgentEnvelope(t, valid, func(fields map[string]json.RawMessage) { fields["tick"] = json.RawMessage(`"one"`) }),
			field:      "records[0].tick",
			reasonPart: "must be a non-negative integer",
		},
		{
			name:       "missing payload",
			input:      mutateAgentEnvelope(t, valid, func(fields map[string]json.RawMessage) { delete(fields, "payload") }),
			field:      "records[0].payload",
			reasonPart: "is required",
		},
		{
			name:       "explicitly empty payload",
			input:      encodeAgentRecords(t, []transcript.Record{agentRecord(1, transcript.DirectionIn, transcript.StreamWS, "")}),
			field:      "records[0].payload",
			reasonPart: "must not be empty",
		},
		{
			name:       "missing semantic text",
			input:      encodeAgentRecords(t, []transcript.Record{agentRecord(1, transcript.DirectionOut, transcript.StreamWS, `{"kind":"transcript"}`)}),
			field:      "records[0].payload.text",
			reasonPart: "is required",
		},
		{
			name:       "wrong semantic text type",
			input:      encodeAgentRecords(t, []transcript.Record{agentRecord(1, transcript.DirectionOut, transcript.StreamWS, `{"kind":"transcript","text":17}`)}),
			field:      "records[0].payload.text",
			reasonPart: "must be a string",
		},
		{
			name:       "wrong peer",
			input:      mutateAgentEnvelope(t, valid, func(fields map[string]json.RawMessage) { fields["peer"] = json.RawMessage(`"client"`) }),
			field:      "records[0].peer",
			reasonPart: "must be \"agent\"",
		},
		{
			name:       "unknown direction",
			input:      mutateAgentEnvelope(t, valid, func(fields map[string]json.RawMessage) { fields["dir"] = json.RawMessage(`"sideways"`) }),
			field:      "records[0].direction",
			reasonPart: "unknown direction",
		},
		{
			name:       "unknown stream",
			input:      mutateAgentEnvelope(t, valid, func(fields map[string]json.RawMessage) { fields["stream"] = json.RawMessage(`"mystery"`) }),
			field:      "records[0].stream",
			reasonPart: "unknown stream",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeAgent("S4-agent", tt.input)
			if err == nil {
				t.Fatalf("NormalizeAgent returned projection %+v without an error", got)
			}
			if !reflect.DeepEqual(got, Projection{}) {
				t.Fatalf("error returned partial projection: %+v", got)
			}
			assertNormalizationError(t, err, "S4-agent", tt.field, tt.reasonPart)
		})
	}
}

func agentFixtureRecords() []transcript.Record {
	return []transcript.Record{
		agentRecord(0, transcript.DirectionIn, transcript.StreamWS, `{"kind":"turn.start","id":"turn-1","role":"user"}`),
		agentRecord(1, transcript.DirectionIn, transcript.StreamRTCAudio, `{"kind":"audio.frame","bytes":[0,1,2,255]}`),
		agentRecord(2, transcript.DirectionOut, transcript.StreamWS, `{"kind":"transcript","text":"  keep whitespace  ","extra":[1,1]}`),
		agentRecord(3, transcript.DirectionOut, transcript.StreamWS, `{"kind":"transcript","text":"  keep whitespace  ","extra":[1,1]}`),
		agentRecord(4, transcript.DirectionOut, transcript.StreamRTCData, `{"kind":"tool.call","id":"tool-1","name":"lookup","arguments":{"q":"a  b"}}`),
		agentRecord(5, transcript.DirectionIn, transcript.StreamRTCData, `{"kind":"tool.result","id":"tool-1","result":{"answer":"ok"}}`),
		agentRecord(6, transcript.DirectionIn, transcript.StreamWS, `{"kind":"interrupt","reason":"barge-in","provenance":"client"}`),
		agentRecord(7, transcript.DirectionOut, transcript.StreamWS, `{"kind":"turn.end","id":"turn-1","role":"user"}`),
		agentRecord(8, transcript.DirectionOut, transcript.StreamWS, `{"kind":"terminal","reason":"provider_close","provenance":"provider"}`),
		agentRecord(9, transcript.DirectionOut, transcript.StreamWS, `{"kind":"transport.id","connection":"conn-1"}`),
		agentRecord(10, transcript.DirectionOut, transcript.StreamRTCData, `{"kind":"transport.segment","packet":7,"part":1}`),
	}
}

func agentRecord(tick uint64, direction transcript.Direction, stream transcript.Stream, payload string) transcript.Record {
	return transcript.NewRecord(tick, time.Unix(int64(tick), 0).UTC(), transcript.PeerAgent, direction, stream, []byte(payload))
}

func encodeAgentRecords(t *testing.T, records []transcript.Record) []byte {
	t.Helper()
	var output bytes.Buffer
	for index, record := range records {
		line, err := transcript.Encode(record)
		if err != nil {
			t.Fatalf("encode record %d: %v", index, err)
		}
		output.Write(line)
	}
	return output.Bytes()
}

func cloneAgentRecords(records []transcript.Record) []transcript.Record {
	cloned := make([]transcript.Record, len(records))
	for index, record := range records {
		cloned[index] = record
		cloned[index].Payload = append([]byte(nil), record.Payload...)
	}
	return cloned
}

func mutateAgentEnvelope(t *testing.T, record transcript.Record, mutate func(map[string]json.RawMessage)) []byte {
	t.Helper()
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	mutate(fields)
	line, err = json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal mutated record: %v", err)
	}
	return append(line, '\n')
}
