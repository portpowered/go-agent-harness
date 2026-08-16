package parity

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

func TestNormalizeRepresentativeBothSideProjection(t *testing.T) {
	const repeatedPayload = "{ \"kind\": \"transcript\", \"text\": \"  exact  \", \"extra\": [1, 1], \"extra\": [2, 2] }"
	turnStart := `{"kind":"turn.start","id":"turn-1","role":"user"}`
	audioClient := `{"kind":"audio.frame","bytes":[1,0,255]}`
	audioAgent := `{"kind":"audio.frame","bytes":"AgM="}`
	toolCall := `{"kind":"tool.call","id":"tool-1","name":"lookup","arguments":{"q":"a  b"}}`
	toolResult := `{"kind":"tool.result","id":"tool-1","result":{"value":"ok"}}`
	interrupt := `{"kind":"interrupt","reason":"barge-in","provenance":"client"}`
	turnEnd := `{"kind":"turn.end","id":"turn-1","role":"user"}`
	terminal := `{"kind":"terminal","reason":"provider_close","provenance":"provider"}`
	records := []transcript.Record{
		jsonRecord(1, transcript.PeerClient, transcript.DirectionOut, transcript.StreamWS, turnStart),
		jsonRecord(2, transcript.PeerClient, transcript.DirectionOut, transcript.StreamRTCAudio, audioClient),
		jsonRecord(3, transcript.PeerAgent, transcript.DirectionIn, transcript.StreamRTCAudio, audioAgent),
		jsonRecord(4, transcript.PeerClient, transcript.DirectionOut, transcript.StreamWS, repeatedPayload),
		jsonRecord(5, transcript.PeerAgent, transcript.DirectionIn, transcript.StreamWS, toolCall),
		jsonRecord(6, transcript.PeerClient, transcript.DirectionOut, transcript.StreamWS, toolResult),
		jsonRecord(7, transcript.PeerClient, transcript.DirectionOut, transcript.StreamWS, interrupt),
		jsonRecord(8, transcript.PeerAgent, transcript.DirectionIn, transcript.StreamWS, turnEnd),
		jsonRecord(9, transcript.PeerAgent, transcript.DirectionIn, transcript.StreamWS, terminal),
	}

	got, err := Normalize("client capture", records)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	want := Projection{
		Turns: []TurnBoundary{
			{Order: 1, Tick: 1, Kind: "turn.start", Boundary: "start", ID: "turn-1", Role: "user", Payload: []byte(turnStart)},
			{Order: 2, Tick: 8, Kind: "turn.end", Boundary: "end", ID: "turn-1", Role: "user", Payload: []byte(turnEnd)},
		},
		Audio: AudioSummary{
			FrameCount: 2,
			TotalBytes: 5,
			Frames: []AudioFrame{
				{Tick: 2, Bytes: []byte{1, 0, 255}, Payload: []byte(audioClient)},
				{Tick: 3, Bytes: []byte{2, 3}, Payload: []byte(audioAgent)},
			},
		},
		Transcripts: []TranscriptFact{
			{Order: 1, Tick: 4, Text: "  exact  ", Payload: []byte(repeatedPayload)},
		},
		ToolCalls: []ToolCallFact{
			{
				Order: 1, ID: "tool-1", Name: "lookup",
				Arguments: []byte(`{"q":"a  b"}`), Result: []byte(`{"value":"ok"}`),
				CallTick: 5, ResultTick: 6,
				CallPayload: []byte(toolCall), ResultPayload: []byte(toolResult),
			},
		},
		Interruptions: []InterruptionFact{
			{Order: 1, Tick: 7, Reason: "barge-in", Provenance: "client", Payload: []byte(interrupt)},
		},
		Terminal: &TerminalOutcome{
			Tick: 9, Reason: "provider_close", Provenance: "provider", Payload: []byte(terminal),
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projection mismatch\n got: %#v\nwant: %#v", got, want)
	}

	// Peer, direction, stream, and wall-clock timestamp are recorder or
	// transport mechanics. Changing them must not change the retained facts.
	view := make([]transcript.Record, len(records))
	copy(view, records)
	for i := range view {
		view[i].Peer = transcript.PeerClient
		view[i].Direction = transcript.DirectionIn
		view[i].Stream = transcript.StreamWS
		view[i].Timestamp = time.Unix(int64(1000+i), 0).UTC().Format(time.RFC3339Nano)
	}
	viewProjection, err := Normalize("agent capture", view)
	if err != nil {
		t.Fatalf("Normalize changed viewpoint metadata: %v", err)
	}
	if !reflect.DeepEqual(viewProjection, want) {
		t.Fatalf("viewpoint metadata changed projection\n got: %#v\nwant: %#v", viewProjection, want)
	}
}

func TestNormalizeRejectsInvalidRecords(t *testing.T) {
	validPayload := `{"kind":"transcript","text":"kept"}`
	tests := []struct {
		name       string
		record     transcript.Record
		field      string
		reasonPart string
	}{
		{"malformed payload", jsonRecord(1, transcript.PeerClient, transcript.DirectionIn, transcript.StreamWS, "{"), "records[0].payload", "valid JSON"},
		{"missing format version", transcript.Record{Peer: transcript.PeerClient, Direction: transcript.DirectionIn, Stream: transcript.StreamWS, Payload: []byte(validPayload)}, "records[0].version", "missing format version"},
		{"unsupported format version", func() transcript.Record {
			r := jsonRecord(1, transcript.PeerClient, transcript.DirectionIn, transcript.StreamWS, validPayload)
			r.Version = 2
			return r
		}(), "records[0].version", "unsupported format version"},
		{"unknown peer", jsonRecord(1, transcript.Peer("proxy"), transcript.DirectionIn, transcript.StreamWS, validPayload), "records[0].peer", "unknown peer"},
		{"unknown direction", jsonRecord(1, transcript.PeerClient, transcript.Direction("sideways"), transcript.StreamWS, validPayload), "records[0].direction", "unknown direction"},
		{"unknown record kind", jsonRecord(1, transcript.PeerClient, transcript.DirectionIn, transcript.StreamWS, `{"kind":"future.kind"}`), "records[0].kind", "unknown record kind"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Normalize("S4", []transcript.Record{tt.record})
			if err == nil {
				t.Fatalf("Normalize returned projection %+v without an error", got)
			}
			assertNormalizationError(t, err, "S4", tt.field, tt.reasonPart)
		})
	}
}

func TestNormalizeRejectsUnsupportedSemanticKinds(t *testing.T) {
	// These are recognizable lifecycle, usage, VAD, tool-stream, image, or
	// video events, but none is a retained parity fact or transport mechanic.
	for _, kind := range []string{
		"audio.start", "audio.end", "text.start", "text.end", "message.delta",
		"session.open", "session.created", "session.updated", "session.update", "usage.info",
		"pong", "ping", "vad.speech.started", "vad.speech.stopped",
		"toolcall.delta", "tool.call.delta", "image.start", "image.delta", "image.end",
		"video.start", "video.delta", "video.end",
	} {
		t.Run(strings.ReplaceAll(kind, ".", "_"), func(t *testing.T) {
			_, err := Normalize("S4", []transcript.Record{
				jsonRecord(1, transcript.PeerClient, transcript.DirectionIn, transcript.StreamWS, `{"kind":"`+kind+`"}`),
			})
			if err == nil {
				t.Fatalf("kind %q was silently dropped", kind)
			}
			assertNormalizationError(t, err, "S4", "records[0].kind", "unknown record kind")
		})
	}
}

func TestNormalizeOmitsOnlyExplicitTransportMechanics(t *testing.T) {
	records := []transcript.Record{
		jsonRecord(1, transcript.PeerClient, transcript.DirectionOut, transcript.StreamWS, `{"kind":"transport.id","connection":"conn-1"}`),
		jsonRecord(2, transcript.PeerAgent, transcript.DirectionIn, transcript.StreamRTCData, `{"kind":"transport.segment","packet":7,"part":1}`),
	}
	got, err := Normalize("S16", records)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	want := Projection{
		Turns:         []TurnBoundary{},
		Audio:         AudioSummary{Frames: []AudioFrame{}},
		Transcripts:   []TranscriptFact{},
		ToolCalls:     []ToolCallFact{},
		Interruptions: []InterruptionFact{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("transport mechanics affected projection\n got: %#v\nwant: %#v", got, want)
	}
}

func TestNormalizeRejectsUnknownKindBetweenValidRecords(t *testing.T) {
	records := []transcript.Record{
		jsonRecord(1, transcript.PeerClient, transcript.DirectionIn, transcript.StreamWS, `{"kind":"transcript","text":"before"}`),
		jsonRecord(2, transcript.PeerClient, transcript.DirectionIn, transcript.StreamWS, `{"kind":"not-supported"}`),
		jsonRecord(3, transcript.PeerClient, transcript.DirectionIn, transcript.StreamWS, `{"kind":"transcript","text":"after"}`),
	}
	if _, err := Normalize("S4", records); err == nil {
		t.Fatal("unknown record kind was silently skipped")
	} else {
		assertNormalizationError(t, err, "S4", "records[1].kind", "unknown record kind")
	}
	projection, err := Normalize("S16", []transcript.Record{records[0], records[2]})
	if err != nil || len(projection.Transcripts) != 2 || projection.Transcripts[0].Text != "before" || projection.Transcripts[1].Text != "after" {
		t.Fatalf("surrounding valid projection = %+v, err = %v", projection, err)
	}
}

func TestNormalizePreservesOrderDuplicatesAndInputOwnership(t *testing.T) {
	payload := []byte(`{"kind":"transcript","text":"one"}`)
	records := []transcript.Record{
		jsonRecord(4, transcript.PeerClient, transcript.DirectionIn, transcript.StreamWS, string(payload)),
		jsonRecord(2, transcript.PeerClient, transcript.DirectionIn, transcript.StreamWS, `{"kind":"transcript","text":"two"}`),
		jsonRecord(4, transcript.PeerClient, transcript.DirectionIn, transcript.StreamWS, string(payload)),
	}
	projection, err := Normalize("S16", records)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	payload[0] = 'X'
	records[0].Payload[0] = 'Y'
	want := []TranscriptFact{
		{Order: 1, Tick: 4, Text: "one", Payload: []byte(`{"kind":"transcript","text":"one"}`)},
		{Order: 2, Tick: 2, Text: "two", Payload: []byte(`{"kind":"transcript","text":"two"}`)},
		{Order: 3, Tick: 4, Text: "one", Payload: []byte(`{"kind":"transcript","text":"one"}`)},
	}
	if !reflect.DeepEqual(projection.Transcripts, want) {
		t.Fatalf("order/duplicates/ownership = %#v, want %#v", projection.Transcripts, want)
	}
	if bytes.Equal(projection.Transcripts[0].Payload, records[0].Payload) {
		t.Fatal("projected payload aliases input payload")
	}
}

func assertNormalizationError(t *testing.T, err error, interfaceName, field, reasonPart string) {
	t.Helper()
	var normalizationErr *NormalizationError
	if !errors.As(err, &normalizationErr) {
		t.Fatalf("error type = %T, want *NormalizationError", err)
	}
	if normalizationErr.Interface != interfaceName || normalizationErr.Field != field || !strings.Contains(normalizationErr.Reason, reasonPart) {
		t.Fatalf("normalization error = %+v, want interface %q, field %q, reason containing %q", normalizationErr, interfaceName, field, reasonPart)
	}
	if !errors.Is(err, ErrNormalization) {
		t.Fatal("normalization error does not wrap ErrNormalization")
	}
}

func jsonRecord(tick uint64, peer transcript.Peer, direction transcript.Direction, stream transcript.Stream, payload string) transcript.Record {
	return transcript.NewRecord(tick, time.Unix(int64(tick), 0), peer, direction, stream, []byte(payload))
}
