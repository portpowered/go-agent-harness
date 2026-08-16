package parity

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

func TestNormalizeRepresentativeBothSideProjection(t *testing.T) {
	const repeatedPayload = "{ \"kind\": \"transcript\", \"text\": \"  exact  \", \"extra\": [1, 1], \"extra\": [2, 2] }"
	records := []transcript.Record{
		jsonRecord(1, transcript.PeerClient, transcript.DirectionOut, transcript.StreamWS, `{"kind":"turn.start","id":"turn-1","role":"user"}`),
		jsonRecord(2, transcript.PeerClient, transcript.DirectionOut, transcript.StreamRTCAudio, `{"kind":"audio.frame","bytes":[1,0,255]}`),
		jsonRecord(3, transcript.PeerAgent, transcript.DirectionIn, transcript.StreamRTCAudio, `{"kind":"audio.frame","bytes":"AgM="}`),
		jsonRecord(4, transcript.PeerClient, transcript.DirectionOut, transcript.StreamWS, repeatedPayload),
		jsonRecord(5, transcript.PeerAgent, transcript.DirectionIn, transcript.StreamWS, `{"kind":"tool.call","id":"tool-1","name":"lookup","arguments":{"q":"a  b"}}`),
		jsonRecord(6, transcript.PeerClient, transcript.DirectionOut, transcript.StreamWS, `{"kind":"tool.result","id":"tool-1","result":{"value":"ok"}}`),
		jsonRecord(7, transcript.PeerClient, transcript.DirectionOut, transcript.StreamWS, `{"kind":"interrupt","reason":"barge-in","provenance":"client"}`),
		jsonRecord(8, transcript.PeerAgent, transcript.DirectionIn, transcript.StreamWS, `{"kind":"turn.end","id":"turn-1","role":"user"}`),
		jsonRecord(9, transcript.PeerAgent, transcript.DirectionIn, transcript.StreamWS, `{"kind":"terminal","reason":"provider_close","provenance":"provider"}`),
	}

	got, err := Normalize("client capture", records)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(got.Turns) != 2 || got.Turns[0].Boundary != "start" || got.Turns[1].Boundary != "end" ||
		got.Turns[0].Tick != 1 || got.Turns[1].Tick != 8 || got.Turns[0].ID != "turn-1" {
		t.Fatalf("turn projection = %+v", got.Turns)
	}
	if got.Audio.FrameCount != 2 || got.Audio.TotalBytes != 5 ||
		!bytes.Equal(got.Audio.Frames[0].Bytes, []byte{1, 0, 255}) ||
		!bytes.Equal(got.Audio.Frames[1].Bytes, []byte{2, 3}) ||
		got.Audio.Frames[0].Tick != 2 || got.Audio.Frames[1].Tick != 3 {
		t.Fatalf("audio projection = %+v", got.Audio)
	}
	if len(got.Transcripts) != 1 || got.Transcripts[0].Text != "  exact  " ||
		!bytes.Equal(got.Transcripts[0].Payload, []byte(repeatedPayload)) {
		t.Fatalf("transcript projection = %+v", got.Transcripts)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "tool-1" || got.ToolCalls[0].Name != "lookup" ||
		got.ToolCalls[0].CallTick != 5 || got.ToolCalls[0].ResultTick != 6 ||
		!bytes.Contains(got.ToolCalls[0].Arguments, []byte(`"q":"a  b"`)) ||
		!bytes.Contains(got.ToolCalls[0].Result, []byte(`"value":"ok"`)) {
		t.Fatalf("tool projection = %+v", got.ToolCalls)
	}
	if len(got.Interruptions) != 1 || got.Interruptions[0].Tick != 7 || got.Interruptions[0].Reason != "barge-in" {
		t.Fatalf("interrupt projection = %+v", got.Interruptions)
	}
	if got.Terminal == nil || got.Terminal.Tick != 9 || got.Terminal.Reason != "provider_close" || got.Terminal.Provenance != "provider" {
		t.Fatalf("terminal projection = %+v", got.Terminal)
	}

	// The two observation viewpoints carry different transport metadata but the
	// retained semantic payloads are identical, so both reduce to useful facts.
	for _, peer := range []transcript.Peer{transcript.PeerClient, transcript.PeerAgent} {
		view := make([]transcript.Record, len(records))
		copy(view, records)
		for i := range view {
			view[i].Peer = peer
			view[i].Timestamp = time.Unix(int64(1000+i), 0).Format(time.RFC3339Nano)
		}
		if projection, err := Normalize("both-side", view); err != nil || len(projection.Transcripts) != 1 || projection.Audio.TotalBytes != 5 {
			t.Fatalf("peer %q projection = %+v, err = %v", peer, projection, err)
		}
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
			var normalizationErr *NormalizationError
			if !errors.As(err, &normalizationErr) {
				t.Fatalf("error type = %T, want *NormalizationError", err)
			}
			if normalizationErr.Interface != "S4" || normalizationErr.Field != tt.field || !strings.Contains(normalizationErr.Reason, tt.reasonPart) {
				t.Fatalf("normalization error = %+v, want interface S4, field %q, reason containing %q", normalizationErr, tt.field, tt.reasonPart)
			}
			if !errors.Is(err, ErrNormalization) {
				t.Fatal("normalization error does not wrap ErrNormalization")
			}
		})
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
		var normalizationErr *NormalizationError
		if !errors.As(err, &normalizationErr) || normalizationErr.Field != "records[1].kind" {
			t.Fatalf("error = %v, want indexed kind error", err)
		}
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
	if len(projection.Transcripts) != 3 || projection.Transcripts[0].Tick != 4 || projection.Transcripts[1].Tick != 2 || projection.Transcripts[2].Tick != 4 {
		t.Fatalf("order/duplicates = %+v", projection.Transcripts)
	}
	if !bytes.Equal(projection.Transcripts[0].Payload, []byte(`{"kind":"transcript","text":"one"}`)) {
		t.Fatalf("projected payload was aliased or rewritten: %q", projection.Transcripts[0].Payload)
	}
}

func jsonRecord(tick uint64, peer transcript.Peer, direction transcript.Direction, stream transcript.Stream, payload string) transcript.Record {
	return transcript.NewRecord(tick, time.Unix(int64(tick), 0), peer, direction, stream, []byte(payload))
}
