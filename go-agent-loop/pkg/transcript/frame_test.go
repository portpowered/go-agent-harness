package transcript

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var updateFrameGolden = flag.Bool("update", false, "update transcript frame golden files")

// The fixtures are embedded so ordinary test runs only compare against
// committed data. The -update flag is the only path that writes them back.
//
//go:embed testdata/frame.jsonl testdata/fuzz-seeds.json
var frameFixtures embed.FS

type fuzzSeed struct {
	Name    string `json:"name"`
	Payload string `json:"payload"`
}

func TestEncodeDecodeS3Golden(t *testing.T) {
	records := goldenRecords()
	var encoded bytes.Buffer
	for index, want := range records {
		line, err := Encode(want)
		if err != nil {
			t.Fatalf("record %d: encode: %v", index, err)
		}
		got, err := Decode(line)
		if err != nil {
			t.Fatalf("record %d: decode: %v", index, err)
		}
		if got.Version != FormatVersion || got.Tick != want.Tick || got.Timestamp != want.Timestamp ||
			got.Peer != want.Peer || got.Direction != want.Direction || got.Stream != want.Stream {
			t.Fatalf("record %d metadata changed: got %+v, want %+v", index, got, want)
		}
		if len(got.Payload) == 0 || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("record %d payload changed: got %v, want %v", index, got.Payload, want.Payload)
		}
		encoded.Write(line)
	}

	path := filepath.FromSlash("testdata/frame.jsonl")
	if *updateFrameGolden {
		if err := os.WriteFile(path, encoded.Bytes(), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return
	}
	want, err := frameFixtures.ReadFile(filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if !bytes.Equal(want, encoded.Bytes()) {
		t.Errorf("golden differs; run with -update only after reviewing the format change\n got:\n%s\nwant:\n%s", want, encoded.Bytes())
	}
}

func TestNewRecordOwnsPayloadAndNormalizesTimestamp(t *testing.T) {
	payload := []byte{0xff, 0x00, 0x7f}
	record := NewRecord(
		9,
		time.Date(2026, time.August, 16, 12, 34, 56, 789, time.FixedZone("PDT", -7*60*60)),
		PeerAgent,
		DirectionOut,
		StreamRTCAudio,
		payload,
	)
	payload[0] = 0

	if record.Version != FormatVersion || record.Tick != 9 {
		t.Fatalf("record identity = %+v, want version %d and tick 9", record, FormatVersion)
	}
	if record.Timestamp != "2026-08-16T19:34:56.000000789Z" {
		t.Fatalf("timestamp = %q, want UTC RFC3339Nano value", record.Timestamp)
	}
	if !bytes.Equal(record.Payload, []byte{0xff, 0x00, 0x7f}) {
		t.Fatalf("payload = %v, want copied original bytes", record.Payload)
	}
}

func TestDecodeRejectsMissingUnsupportedAndMalformedRecords(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		want       error
		wantInText string
	}{
		{name: "missing", line: `{"tick":1,"payload":""}`, want: ErrMissingVersion, wantInText: "missing format version"},
		{name: "zero", line: `{"v":0,"payload":""}`, want: ErrMissingVersion, wantInText: "missing format version"},
		{name: "unsupported", line: `{"v":2,"payload":""}`, want: ErrUnsupportedVersion, wantInText: "unsupported format version"},
		{name: "invalid-json", line: `{"v":`},
		{name: "invalid-record-field", line: `{"v":1,"tick":"one","payload":""}`, wantInText: "decode record"},
		{name: "invalid-base64", line: `{"v":1,"payload":"%%%"}`, wantInText: "decode payload"},
		{name: "missing-payload", line: `{"v":1}`, wantInText: "missing payload"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Decode([]byte(test.line))
			if err == nil {
				t.Fatal("Decode returned nil error")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want errors.Is(..., %v)", err, test.want)
			}
			if test.wantInText != "" && !strings.Contains(err.Error(), test.wantInText) {
				t.Fatalf("error = %q, want text %q", err, test.wantInText)
			}
		})
	}
}

func TestJSONMarshalRoundTripAndJSONLBoundaries(t *testing.T) {
	want := NewRecord(11, time.Unix(1_750_000_000, 123).UTC(), PeerClient, DirectionIn, StreamRTCData, []byte{0x00, 0xff})
	object, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var got Record
	if err := json.Unmarshal(object, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got.Tick != want.Tick || got.Timestamp != want.Timestamp || got.Peer != want.Peer ||
		got.Direction != want.Direction || got.Stream != want.Stream || !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("JSON round trip = %+v, want %+v", got, want)
	}

	line, err := Encode(want)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if line[len(line)-1] != '\n' {
		t.Fatalf("encoded line = %q, want JSONL newline", line)
	}
	withWhitespace := append([]byte(" \t"), line...)
	withWhitespace = append(withWhitespace, []byte(" \n")...)
	if decoded, err := Decode(withWhitespace); err != nil || !bytes.Equal(decoded.Payload, want.Payload) {
		t.Fatalf("Decode surrounding whitespace = (%+v, %v), want payload %v", decoded, err, want.Payload)
	}
	if _, err := Decode(append(line, []byte(`{"v":1,"payload":""}`)...)); err == nil {
		t.Fatal("Decode accepted two JSON values as one JSONL record")
	}
}

func TestRecordUnmarshalNilReceiver(t *testing.T) {
	var record *Record
	if err := record.UnmarshalJSON([]byte(`{"v":1,"payload":""}`)); err == nil {
		t.Fatal("nil receiver returned nil error")
	}
}

func FuzzRecordPayloadRoundTripS7(f *testing.F) {
	seedData, err := frameFixtures.ReadFile("testdata/fuzz-seeds.json")
	if err != nil {
		f.Fatalf("read fuzz seeds: %v", err)
	}
	var seeds []fuzzSeed
	if err := json.Unmarshal(seedData, &seeds); err != nil {
		f.Fatalf("decode fuzz seeds: %v", err)
	}
	for _, seed := range seeds {
		payload, err := base64.StdEncoding.DecodeString(seed.Payload)
		if err != nil {
			f.Fatalf("decode seed %q: %v", seed.Name, err)
		}
		f.Add(payload)
	}

	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) > 64*1024 {
			payload = payload[:64*1024]
		}
		want := NewRecord(17, time.Unix(1_750_000_123, 456).UTC(), PeerAgent, DirectionOut, StreamWS, payload)
		line, err := Encode(want)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		got, err := Decode(line)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Version != want.Version || got.Tick != want.Tick || got.Timestamp != want.Timestamp ||
			got.Peer != want.Peer || got.Direction != want.Direction || got.Stream != want.Stream {
			t.Fatalf("metadata changed: got %+v, want %+v", got, want)
		}
		if len(got.Payload) != len(payload) || !bytes.Equal(got.Payload, payload) {
			t.Fatalf("payload changed: got length %d bytes %v, want length %d bytes %v", len(got.Payload), got.Payload, len(payload), payload)
		}
	})
}

func goldenRecords() []Record {
	base := time.Date(2026, time.August, 16, 19, 0, 0, 123456789, time.UTC)
	peers := []Peer{PeerClient, PeerAgent}
	directions := []Direction{DirectionIn, DirectionOut}
	streams := []Stream{StreamWS, StreamRTCAudio, StreamRTCData, StreamDeviceIn, StreamDeviceOut}
	payloads := [][]byte{
		[]byte(`{"z":1,"unknown":{"nested":true},"a":2}`),
		[]byte("{ \n  \"a\" : 1, \"b\" : [true, null] \t}"),
		[]byte(`{"b":2,"a":1}`),
		{0xff, 0xfe, 0x00, 0xc3, 0x28},
		{'R', 'I', 'F', 'F', 0x00, 0x01, 0x80, 0xff, 0x00, 0x7f},
	}

	records := make([]Record, 0, len(peers)*len(directions)*len(streams))
	index := 0
	for _, peer := range peers {
		for _, direction := range directions {
			for _, stream := range streams {
				records = append(records, NewRecord(
					uint64(index+1),
					base.Add(time.Duration(index)*time.Millisecond),
					peer,
					direction,
					stream,
					payloads[index%len(payloads)],
				))
				index++
			}
		}
	}
	return records
}
