package parity

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

func TestNormalizeClientPreservesRepresentativeProjection(t *testing.T) {
	records := normalizeClientTestCapture()
	got, err := NormalizeClient(records)
	if err != nil {
		t.Fatalf("NormalizeClient: %v", err)
	}

	want := Projection{
		Turns: []TurnBoundary{
			{Order: 1, Tick: 1, Kind: "turn.start", Boundary: "start", ID: "turn-1", Role: "user", Payload: []byte(`{"kind":"turn.start","id":"turn-1","role":"user"}`)},
			{Order: 2, Tick: 9, Kind: "turn.end", Boundary: "end", ID: "turn-1", Role: "user", Payload: []byte(`{"kind":"turn.end","id":"turn-1","role":"user"}`)},
		},
		Audio: AudioSummary{
			FrameCount: 2,
			TotalBytes: 5,
			Frames: []AudioFrame{
				{Tick: 2, Bytes: []byte{1, 0, 255}, Payload: []byte(`{"kind":"audio.frame","bytes":[1,0,255]}`)},
				{Tick: 3, Bytes: []byte{2, 3}, Payload: []byte(`{"kind":"audio.frame","bytes":"AgM="}`)},
			},
		},
		Transcripts: []TranscriptFact{
			{Order: 1, Tick: 4, Text: "  exact  ", Payload: []byte(`{"kind":"transcript","text":"  exact  "}`)},
			{Order: 2, Tick: 5, Text: "  exact  ", Payload: []byte(`{"kind":"transcript","text":"  exact  "}`)},
		},
		ToolCalls: []ToolCallFact{
			{
				Order: 1, ID: "tool-1", Name: "lookup",
				Arguments: []byte(`{"q":"a  b"}`), Result: []byte(`{"value":"ok"}`),
				CallTick: 6, ResultTick: 7,
				CallPayload:   []byte(`{"kind":"tool.call","id":"tool-1","name":"lookup","arguments":{"q":"a  b"}}`),
				ResultPayload: []byte(`{"kind":"tool.result","id":"tool-1","result":{"value":"ok"}}`),
			},
		},
		Interruptions: []InterruptionFact{
			{Order: 1, Tick: 8, Reason: "barge-in", Provenance: "client", Payload: []byte(`{"kind":"interrupt","reason":"barge-in","provenance":"client"}`)},
		},
		Terminal: &TerminalOutcome{
			Tick: 10, Reason: "provider_close", Provenance: "provider", Payload: []byte(`{"kind":"terminal","reason":"provider_close","provenance":"provider"}`),
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projection mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestNormalizeClientExcludesOnlyNamedRecorderMechanics(t *testing.T) {
	baseline := normalizeClientTestNormalize(t, normalizeClientTestCapture())

	mutations := []struct {
		name   string
		mutate func([]transcript.Record) []transcript.Record
	}{
		{"wall_clock_timestamp", func(records []transcript.Record) []transcript.Record {
			records[4].Timestamp = time.Date(2040, 1, 2, 3, 4, 5, 6, time.UTC).Format(time.RFC3339Nano)
			return records
		}},
		{"transport_stream_metadata", func(records []transcript.Record) []transcript.Record {
			records[2].Stream = transcript.StreamRTCData
			return records
		}},
		{"transport_direction_metadata", func(records []transcript.Record) []transcript.Record {
			records[3].Direction = transcript.DirectionOut
			return records
		}},
		{"transport_id_event", func(records []transcript.Record) []transcript.Record {
			return append(records, normalizeClientTestRecord(11, transcript.StreamWS, `{"kind":"transport.id","connection":"changed"}`))
		}},
		{"transport_identifier_event", func(records []transcript.Record) []transcript.Record {
			return append(records, normalizeClientTestRecord(11, transcript.StreamWS, `{"kind":"transport.identifier","connection":"changed"}`))
		}},
		{"transport_packet_event", func(records []transcript.Record) []transcript.Record {
			return append(records, normalizeClientTestRecord(12, transcript.StreamWS, `{"kind":"transport.packet","sequence":99,"payload":"changed"}`))
		}},
		{"transport_frame_event", func(records []transcript.Record) []transcript.Record {
			return append(records, normalizeClientTestRecord(13, transcript.StreamWS, `{"kind":"transport.frame","sequence":99,"part":4}`))
		}},
		{"payload_framing_event", func(records []transcript.Record) []transcript.Record {
			return append(records, normalizeClientTestRecord(14, transcript.StreamRTCData, `{"kind":"transport.segment","packet":99,"part":4}`))
		}},
	}

	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			records := mutation.mutate(normalizeClientTestCloneRecords(normalizeClientTestCapture()))
			got := normalizeClientTestNormalize(t, records)
			if !reflect.DeepEqual(got, baseline) {
				t.Fatalf("projection changed for excluded %s\n got: %#v\nwant: %#v", mutation.name, got, baseline)
			}
		})
	}

	retained := normalizeClientTestCloneRecords(normalizeClientTestCapture())
	retained[4].Tick++
	changed := normalizeClientTestNormalize(t, retained)
	if reflect.DeepEqual(changed, baseline) {
		t.Fatal("retained logical tick mutation did not change projection")
	}
	if changed.Transcripts[1].Tick == baseline.Transcripts[1].Tick {
		t.Fatal("retained logical tick mutation was not preserved")
	}
}

func TestNormalizeClientRejectsInvalidInputWithoutPartialProjection(t *testing.T) {
	valid := normalizeClientTestCapture()
	tests := []struct {
		name   string
		mutate func([]transcript.Record)
		field  string
		reason string
	}{
		{"unknown_event_kind", func(records []transcript.Record) {
			records[4].Payload = []byte(`{"kind":"future.event"}`)
		}, "records[4].kind", "unknown record kind"},
		{"missing_required_text", func(records []transcript.Record) {
			records[4].Payload = []byte(`{"kind":"transcript"}`)
		}, "records[4].payload.text", "is required"},
		{"wrong_text_type", func(records []transcript.Record) {
			records[4].Payload = []byte(`{"kind":"transcript","text":7}`)
		}, "records[4].payload.text", "must be a string"},
		{"missing_tool_correlation", func(records []transcript.Record) {
			records[5].Payload = []byte(`{"kind":"tool.call","name":"lookup","arguments":{}}`)
		}, "records[5].payload.id", "is required"},
		{"wrong_stream_type", func(records []transcript.Record) {
			records[0].Stream = transcript.Stream("unknown")
		}, "records[0].stream", "unknown stream"},
		{"wrong_client_peer", func(records []transcript.Record) {
			records[0].Peer = transcript.PeerAgent
		}, "records[0].peer", "must be \"client\""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			records := normalizeClientTestCloneRecords(valid)
			test.mutate(records)
			projection, err := NormalizeClient(records)
			if err == nil {
				t.Fatalf("NormalizeClient returned projection %+v", projection)
			}
			normalizeClientTestAssertError(t, err, test.field, test.reason)
			if !reflect.DeepEqual(projection, Projection{}) {
				t.Fatalf("invalid input returned partial projection: %#v", projection)
			}
		})
	}
}

func TestNormalizeClientDistinguishesAbsentAndPresentEmptyValues(t *testing.T) {
	missing := normalizeClientTestCloneRecords(normalizeClientTestCapture())
	missing[4].Payload = []byte(`{"kind":"transcript"}`)
	if _, err := NormalizeClient(missing); err == nil {
		t.Fatal("missing text was accepted")
	} else {
		normalizeClientTestAssertError(t, err, "records[4].payload.text", "is required")
	}

	empty := normalizeClientTestCloneRecords(normalizeClientTestCapture())
	empty[4].Payload = []byte(`{"kind":"transcript","text":""}`)
	projection, err := NormalizeClient(empty)
	if err != nil {
		t.Fatalf("present empty text: %v", err)
	}
	if got := projection.Transcripts[1].Text; got != "" {
		t.Fatalf("present empty text = %q, want empty string", got)
	}

	missingAudio := normalizeClientTestCloneRecords(normalizeClientTestCapture())
	missingAudio[2].Payload = []byte(`{"kind":"audio.frame"}`)
	if _, err := NormalizeClient(missingAudio); err == nil {
		t.Fatal("missing audio bytes were accepted")
	} else {
		normalizeClientTestAssertError(t, err, "records[2].payload.bytes", "is required")
	}

	emptyAudio := normalizeClientTestCloneRecords(normalizeClientTestCapture())
	emptyAudio[2].Payload = []byte(`{"kind":"audio.frame","bytes":[]}`)
	projection, err = NormalizeClient(emptyAudio)
	if err != nil {
		t.Fatalf("present empty audio: %v", err)
	}
	if projection.Audio.FrameCount != 2 || len(projection.Audio.Frames[1].Bytes) != 0 || projection.Audio.TotalBytes != 3 {
		t.Fatalf("present empty audio was not retained: %#v", projection.Audio)
	}
}

func TestNormalizeClientIsDeterministicConcurrentAndOwnsOutput(t *testing.T) {
	records := normalizeClientTestCapture()
	baseline := normalizeClientTestNormalize(t, records)
	for i := range records {
		records[i].Payload = append(records[i].Payload, ' ')
	}
	if reflect.DeepEqual(normalizeClientTestNormalize(t, records), baseline) {
		t.Fatal("mutated input unexpectedly matched original payload projection")
	}

	input := normalizeClientTestCapture()
	got := normalizeClientTestNormalize(t, input)
	concurrentInput := normalizeClientTestCloneRecords(input)
	for i := range input {
		for j := range input[i].Payload {
			input[i].Payload[j] = 'X'
		}
	}
	if got.Transcripts[0].Text != "  exact  " || !bytes.Equal(got.Audio.Frames[0].Bytes, []byte{1, 0, 255}) {
		t.Fatalf("projection aliases caller-owned input: %#v", got)
	}

	const workers = 16
	results := make(chan Projection, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer group.Done()
			projection, err := NormalizeClient(concurrentInput)
			if err != nil {
				t.Errorf("concurrent NormalizeClient: %v", err)
				return
			}
			results <- projection
		}()
	}
	group.Wait()
	close(results)
	for projection := range results {
		if !reflect.DeepEqual(projection, got) {
			t.Fatalf("concurrent projection differs\n got: %#v\nwant: %#v", projection, got)
		}
	}
}

func normalizeClientTestCapture() []transcript.Record {
	return []transcript.Record{
		normalizeClientTestRecord(1, transcript.StreamWS, `{"kind":"turn.start","id":"turn-1","role":"user"}`),
		normalizeClientTestRecord(2, transcript.StreamDeviceIn, `{"kind":"audio.frame","bytes":[1,0,255]}`),
		normalizeClientTestRecord(3, transcript.StreamDeviceOut, `{"kind":"audio.frame","bytes":"AgM="}`),
		normalizeClientTestRecord(4, transcript.StreamWS, `{"kind":"transcript","text":"  exact  "}`),
		normalizeClientTestRecord(5, transcript.StreamWS, `{"kind":"transcript","text":"  exact  "}`),
		normalizeClientTestRecord(6, transcript.StreamWS, `{"kind":"tool.call","id":"tool-1","name":"lookup","arguments":{"q":"a  b"}}`),
		normalizeClientTestRecord(7, transcript.StreamWS, `{"kind":"tool.result","id":"tool-1","result":{"value":"ok"}}`),
		normalizeClientTestRecord(8, transcript.StreamWS, `{"kind":"interrupt","reason":"barge-in","provenance":"client"}`),
		normalizeClientTestRecord(9, transcript.StreamWS, `{"kind":"turn.end","id":"turn-1","role":"user"}`),
		normalizeClientTestRecord(10, transcript.StreamWS, `{"kind":"terminal","reason":"provider_close","provenance":"provider"}`),
	}
}

func normalizeClientTestRecord(tick uint64, stream transcript.Stream, payload string) transcript.Record {
	direction := transcript.DirectionIn
	if stream == transcript.StreamDeviceOut || stream == transcript.StreamWS && tick%2 == 1 {
		direction = transcript.DirectionOut
	}
	return transcript.NewRecord(tick, time.Unix(int64(tick), 0), transcript.PeerClient, direction, stream, []byte(payload))
}

func normalizeClientTestCloneRecords(records []transcript.Record) []transcript.Record {
	cloned := make([]transcript.Record, len(records))
	copy(cloned, records)
	for i := range cloned {
		cloned[i].Payload = append([]byte(nil), records[i].Payload...)
	}
	return cloned
}

func normalizeClientTestNormalize(t *testing.T, records []transcript.Record) Projection {
	t.Helper()
	projection, err := NormalizeClient(records)
	if err != nil {
		t.Fatalf("NormalizeClient: %v", err)
	}
	return projection
}

func normalizeClientTestAssertError(t *testing.T, err error, field, reason string) {
	t.Helper()
	var normalizationErr *NormalizationError
	if !errors.As(err, &normalizationErr) {
		t.Fatalf("error type = %T, want *NormalizationError", err)
	}
	if normalizationErr.Interface != "client" || normalizationErr.Field != field || !strings.Contains(normalizationErr.Reason, reason) {
		t.Fatalf("normalization error = %+v, want client/%q/%q", normalizationErr, field, reason)
	}
	if !errors.Is(err, ErrNormalization) {
		t.Fatal("normalization error does not wrap ErrNormalization")
	}
}

func TestNormalizeClientPayloadIsJSONWhenExpected(t *testing.T) {
	records := []transcript.Record{normalizeClientTestRecord(1, transcript.StreamWS, `{"kind":"transcript","text":"kept"}`)}
	records[0].Payload = json.RawMessage(`not JSON`)
	_, err := NormalizeClient(records)
	if err == nil {
		t.Fatal("invalid WebSocket payload was accepted")
	}
	normalizeClientTestAssertError(t, err, "records[0].payload", "valid JSON")
}
