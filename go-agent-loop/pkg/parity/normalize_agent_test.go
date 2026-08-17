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

func TestNormalizeAgentPublicInputForms(t *testing.T) {
	records := []transcript.Record{
		agentRecord(2, transcript.DirectionOut, transcript.StreamWS, `{"kind":"transcript","text":"hello"}`),
	}
	input := encodeAgentRecords(t, records)
	want, err := NormalizeAgent("agent", records)
	if err != nil {
		t.Fatalf("baseline NormalizeAgent: %v", err)
	}

	tests := []struct {
		name      string
		normalize func() (Projection, error)
	}{
		{
			name: "single record input",
			normalize: func() (Projection, error) {
				return NormalizeAgent("agent", records[0])
			},
		},
		{
			name: "transcript alias",
			normalize: func() (Projection, error) {
				return NormalizeAgentTranscript("agent", input)
			},
		},
		{
			name: "JSONL alias",
			normalize: func() (Projection, error) {
				return NormalizeAgentJSONL("agent", input)
			},
		},
		{
			name: "records alias",
			normalize: func() (Projection, error) {
				return NormalizeAgentRecords("agent", records)
			},
		},
		{
			name: "default interface name",
			normalize: func() (Projection, error) {
				return NormalizeAgent(" \t", records)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.normalize()
			if err != nil {
				t.Fatalf("normalization: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("projection mismatch\n got: %#v\nwant: %#v", got, want)
			}
		})
	}
}

func TestNormalizeAgentRejectsEmptyAndUnsupportedInputs(t *testing.T) {
	tests := []struct {
		name       string
		input      any
		field      string
		reasonPart string
	}{
		{name: "nil input", input: nil, field: "records", reasonPart: "is required"},
		{name: "empty JSONL", input: []byte{}, field: "records", reasonPart: "is required"},
		{name: "whitespace JSONL", input: []byte(" \n\t"), field: "records", reasonPart: "is required"},
		{name: "empty records", input: []transcript.Record{}, field: "records", reasonPart: "is required"},
		{name: "unsupported input", input: "not a transcript", field: "input", reasonPart: "unsupported transcript input string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeAgent("S4-input", tt.input)
			if err == nil {
				t.Fatalf("NormalizeAgent returned projection %+v without an error", got)
			}
			if !reflect.DeepEqual(got, Projection{}) {
				t.Fatalf("error returned partial projection: %+v", got)
			}
			assertNormalizationError(t, err, "S4-input", tt.field, tt.reasonPart)
		})
	}
}

func TestNormalizeAgentExclusionMutations(t *testing.T) {
	baseRecords := agentFixtureRecords()
	base, err := NormalizeAgent("agent", encodeAgentRecords(t, baseRecords))
	if err != nil {
		t.Fatalf("baseline NormalizeAgent: %v", err)
	}

	tests := []struct {
		name          string
		field         string
		justification string
		index         int
		mutate        func(*transcript.Record)
	}{
		{
			name:          "wall-clock timestamp",
			field:         "t",
			justification: "arrival time is capture-specific; logical tick is the comparable clock",
			index:         0,
			mutate: func(record *transcript.Record) {
				record.Timestamp = "2099-12-31T23:59:59.999999999Z"
			},
		},
		{
			name:          "agent-relative direction",
			field:         "dir",
			justification: "direction is validated and translated to semantic orientation, then omitted by the shared projection",
			index:         2,
			mutate: func(record *transcript.Record) {
				if record.Direction == transcript.DirectionIn {
					record.Direction = transcript.DirectionOut
				} else {
					record.Direction = transcript.DirectionIn
				}
			},
		},
		{
			name:          "capture stream",
			field:         "stream",
			justification: "the known transport channel is validated but is not semantic evidence in the shared projection",
			index:         2,
			mutate: func(record *transcript.Record) {
				record.Stream = transcript.StreamRTCData
			},
		},
		{
			name:          "transport identifier",
			field:         "payload transport identity",
			justification: "connection identity is transport state and carries no comparable conversation fact",
			index:         9,
			mutate: func(record *transcript.Record) {
				record.Payload = []byte(`{"kind":"transport.id","connection":"different-connection"}`)
			},
		},
		{
			name:          "transport segmentation",
			field:         "payload transport segmentation",
			justification: "packet and fragment boundaries vary with capture mechanics and are not semantic evidence",
			index:         10,
			mutate: func(record *transcript.Record) {
				record.Payload = []byte(`{"kind":"transport.segment","packet":999,"part":4}`)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.field == "" || tt.justification == "" {
				t.Fatal("exclusion must name a raw field and a narrow justification")
			}
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

func TestNormalizeAgentValidatedMetadata(t *testing.T) {
	base := agentFixtureRecords()[0]
	tests := []struct {
		name          string
		field         string
		justification string
		reasonPart    string
		mutate        func(*transcript.Record)
	}{
		{
			name:          "missing schema version",
			field:         "records[0].version",
			justification: "format version selects the transcript schema and has no valid alternate for this adapter",
			reasonPart:    "missing format version",
			mutate: func(record *transcript.Record) {
				record.Version = 0
			},
		},
		{
			name:          "unsupported schema version",
			field:         "records[0].version",
			justification: "format version selects the transcript schema and has no valid alternate for this adapter",
			reasonPart:    "unsupported format version",
			mutate: func(record *transcript.Record) {
				record.Version = transcript.FormatVersion + 1
			},
		},
		{
			name:          "agent peer invariant",
			field:         "records[0].peer",
			justification: "peer identity selects the agent-side adapter; a client peer belongs to the other adapter contract",
			reasonPart:    "must be \"agent\"",
			mutate: func(record *transcript.Record) {
				record.Peer = transcript.PeerClient
			},
		},
		{
			name:          "unknown direction",
			field:         "records[0].direction",
			justification: "direction must be known before the adapter can translate the recorder viewpoint",
			reasonPart:    "unknown direction",
			mutate: func(record *transcript.Record) {
				record.Direction = transcript.Direction("sideways")
			},
		},
		{
			name:          "unknown stream",
			field:         "records[0].stream",
			justification: "stream must be a known capture channel before the adapter can classify raw audio evidence",
			reasonPart:    "unknown stream",
			mutate: func(record *transcript.Record) {
				record.Stream = transcript.Stream("mystery")
			},
		},
		{
			name:          "empty retained payload",
			field:         "records[0].payload",
			justification: "an agent observation without payload bytes cannot represent evidence or a transport mechanic",
			reasonPart:    "must not be empty",
			mutate: func(record *transcript.Record) {
				record.Payload = nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.field == "" || tt.justification == "" {
				t.Fatal("validated metadata must name the field and its validation rationale")
			}
			record := base
			record.Payload = append([]byte(nil), base.Payload...)
			tt.mutate(&record)
			got, err := NormalizeAgentRecords("S4-records", []transcript.Record{record})
			if err == nil {
				t.Fatalf("NormalizeAgentRecords returned projection %+v without an error", got)
			}
			if !reflect.DeepEqual(got, Projection{}) {
				t.Fatalf("error returned partial projection: %+v", got)
			}
			assertNormalizationError(t, err, "S4-records", tt.field, tt.reasonPart)
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
			name:       "malformed JSON record",
			input:      []byte("{"),
			field:      "records[0].record",
			reasonPart: "must be a JSON object",
		},
		{
			name:       "null JSON record",
			input:      []byte("null\n"),
			field:      "records[0].record",
			reasonPart: "must be a JSON object",
		},
		{
			name:       "missing required version",
			input:      mutateAgentEnvelope(t, valid, func(fields map[string]json.RawMessage) { delete(fields, "v") }),
			field:      "records[0].version",
			reasonPart: "is required",
		},
		{
			name:       "unsupported version",
			input:      mutateAgentEnvelope(t, valid, func(fields map[string]json.RawMessage) { fields["v"] = json.RawMessage(`2`) }),
			field:      "records[0].version",
			reasonPart: "unsupported format version",
		},
		{
			name:       "wrong timestamp type",
			input:      mutateAgentEnvelope(t, valid, func(fields map[string]json.RawMessage) { fields["t"] = json.RawMessage(`17`) }),
			field:      "records[0].timestamp",
			reasonPart: "must be a string",
		},
		{
			name:       "invalid encoded payload",
			input:      mutateAgentEnvelope(t, valid, func(fields map[string]json.RawMessage) { fields["payload"] = json.RawMessage(`"not base64"`) }),
			field:      "records[0].payload",
			reasonPart: "must be valid base64",
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
