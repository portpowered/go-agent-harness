package transcript

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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

func TestTeePreservesLiveResultAndRecordsAcceptedFrames(t *testing.T) {
	wantError := errors.New("live consumer stopped")
	inputs := []Record{
		NewRecord(1, time.Unix(1, 0), PeerClient, DirectionIn, StreamWS, []byte("one")),
		NewRecord(2, time.Unix(2, 0), PeerAgent, DirectionOut, StreamRTCAudio, []byte{0xff, 0x00}),
		NewRecord(3, time.Unix(3, 0), PeerClient, DirectionOut, StreamRTCData, []byte("three")),
	}

	baseline := &capturingConsumer{failAt: 2, failErr: wantError}
	for index, input := range inputs {
		if _, err := baseline.write(input); err != nil && !errors.Is(err, wantError) {
			t.Fatalf("baseline input %d error = %v, want nil or %v", index, err, wantError)
		}
	}

	sink := &capturingSink{}
	teeConsumer := &capturingConsumer{failAt: 2, failErr: wantError}
	tee := NewTee(teeConsumer, sink)
	for index, input := range inputs {
		gotCount, gotErr := tee.Write(input)
		wantCount, wantErr := baseline.results[index].count, baseline.results[index].err
		if gotCount != wantCount || !errors.Is(gotErr, wantErr) {
			t.Fatalf("input %d result = (%d, %v), want (%d, %v)", index, gotCount, gotErr, wantCount, wantErr)
		}
	}

	if len(teeConsumer.records) != len(baseline.records) {
		t.Fatalf("live call count = %d, want %d", len(teeConsumer.records), len(baseline.records))
	}
	for index := range baseline.records {
		if !recordsEqual(teeConsumer.records[index], baseline.records[index]) {
			t.Fatalf("live record %d = %+v, want %+v", index, teeConsumer.records[index], baseline.records[index])
		}
	}
	if len(sink.records) != 2 || !recordsEqual(sink.records[0], inputs[0]) || !recordsEqual(sink.records[1], inputs[2]) {
		t.Fatalf("transcript records = %+v, want accepted first and third inputs", sink.records)
	}
}

func TestTeeTranscriptFailureDoesNotChangeLiveResultOrReportRepeatedly(t *testing.T) {
	sinkErr := errors.New("transcript unavailable")
	sink := &errorSink{err: sinkErr}
	var reports []error
	consumer := RecordConsumerFunc(func(record Record) (int, error) {
		return int(record.Tick), nil
	})
	tee := NewTeeWithReporter(consumer, sink, func(err error) {
		reports = append(reports, err)
	})

	for tick := uint64(1); tick <= 3; tick++ {
		count, err := tee.Write(NewRecord(tick, time.Unix(0, 0), PeerClient, DirectionIn, StreamWS, []byte("payload")))
		if count != int(tick) || err != nil {
			t.Fatalf("Write tick %d = (%d, %v), want (%d, nil)", tick, count, err, tick)
		}
	}
	if len(reports) != 1 || !errors.Is(reports[0], sinkErr) {
		t.Fatalf("reports = %v, want one report retaining sink cause", reports)
	}
}

func TestTeeCopiesPayloadBeforeLiveMutation(t *testing.T) {
	sink := &capturingSink{}
	input := NewRecord(1, time.Unix(0, 0), PeerClient, DirectionIn, StreamWS, []byte("original"))
	tee := NewTee(RecordConsumerFunc(func(record Record) (int, error) {
		record.Payload[0] = 'X'
		return 1, nil
	}), sink)
	if count, err := tee.Write(input); count != 1 || err != nil {
		t.Fatalf("Write = (%d, %v), want (1, nil)", count, err)
	}
	if len(sink.records) != 1 || !bytes.Equal(sink.records[0].Payload, []byte("original")) {
		t.Fatalf("transcript payload = %q, want original bytes", sink.records[0].Payload)
	}
}

func TestTeeSupportsByteOrientedLiveWriter(t *testing.T) {
	var live bytes.Buffer
	sink := &capturingSink{}
	input := NewRecord(1, time.Unix(0, 0), PeerAgent, DirectionOut, StreamRTCData, []byte{0x00, 0xff})
	tee := NewTee(&live, sink)
	count, err := tee.Write(input)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	encoded, err := Encode(input)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if count != len(encoded) || !bytes.Equal(live.Bytes(), encoded) {
		t.Fatalf("live bytes/count = (%q, %d), want (%q, %d)", live.Bytes(), count, encoded, len(encoded))
	}
	if len(sink.records) != 1 || !recordsEqual(sink.records[0], input) {
		t.Fatalf("transcript = %+v, want input", sink.records)
	}
}

func TestTeeRotationPreservesLiveResultAndTranscript(t *testing.T) {
	const (
		total      = 24
		maxBackups = 4
	)
	inputs := alternatingRotationRecords(total)
	liveError := errors.New("live consumer sentinel")
	results := make([]consumerResult, total)
	for index := range results {
		results[index] = consumerResult{count: 1}
	}
	results[6] = consumerResult{err: liveError}
	results[13] = consumerResult{count: 2, err: liveError}

	baselineLive := &scriptedByteConsumer{results: results}
	teeLive := &scriptedByteConsumer{results: results}
	path := filepath.Join(t.TempDir(), "tee-rolling.jsonl")
	writer, err := NewWriter(path, WithSegmentSize(8*1024), WithMaxBackups(maxBackups))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	tee := NewTee(teeLive, writer)
	var expectedTranscript []Record
	for index, input := range inputs {
		wantCount, wantErr := baselineLive.Write(input)
		gotCount, gotErr := tee.Write(input)
		if gotCount != wantCount || !errors.Is(gotErr, wantErr) {
			t.Fatalf("input %d result = (%d, %v), want (%d, %v)", index, gotCount, gotErr, wantCount, wantErr)
		}
		if wantCount > 0 {
			expectedTranscript = append(expectedTranscript, input)
		}
	}
	if baselineLive.calls != total || teeLive.calls != total {
		t.Fatalf("live calls = (%d, %d), want (%d, %d)", baselineLive.calls, teeLive.calls, total, total)
	}
	if !bytes.Equal(teeLive.buffer.Bytes(), baselineLive.buffer.Bytes()) {
		t.Fatalf("teed live bytes differ from baseline")
	}
	if writer.AcceptedCount() != uint64(len(expectedTranscript)) {
		t.Fatalf("transcript accepted count = %d, want %d", writer.AcceptedCount(), len(expectedTranscript))
	}
	if err := tee.Close(); err != nil {
		t.Fatalf("Tee.Close: %v", err)
	}
	if _, err := os.Stat(BackupPath(path, 1)); err != nil {
		t.Fatalf("forced rotation backup: %v", err)
	}

	assertTranscriptRecords(t, readRecordsFromSegments(t, path, maxBackups), expectedTranscript)
}

// alternatingRotationRecords builds client/agent records large enough to
// force segment rotation.
func alternatingRotationRecords(total int) []Record {
	inputs := make([]Record, 0, total)
	for index := 0; index < total; index++ {
		peer := PeerClient
		direction := DirectionIn
		if index%2 == 1 {
			peer = PeerAgent
			direction = DirectionOut
		}
		inputs = append(inputs, NewRecord(uint64(index+1), time.Unix(int64(index+1), 0),
			peer, direction, StreamRTCData,
			bytes.Repeat([]byte{byte(index), 0x00, 0xff}, 80)))
	}
	return inputs
}

func assertTranscriptRecords(t *testing.T, gotTranscript, expectedTranscript []Record) {
	t.Helper()
	if len(gotTranscript) != len(expectedTranscript) {
		t.Fatalf("transcript records = %d, want %d", len(gotTranscript), len(expectedTranscript))
	}
	for index := range expectedTranscript {
		if !recordsEqual(gotTranscript[index], expectedTranscript[index]) {
			t.Fatalf("transcript record %d = %+v, want %+v", index, gotTranscript[index], expectedTranscript[index])
		}
	}
}

type capturingConsumer struct {
	records []Record
	results []consumerResult
	failAt  int
	failErr error
}

type consumerResult struct {
	count int
	err   error
}

func (c *capturingConsumer) Write(record Record) (int, error) {
	return c.write(record)
}

func (c *capturingConsumer) write(record Record) (int, error) {
	c.records = append(c.records, cloneRecord(record))
	if len(c.records) == c.failAt {
		c.results = append(c.results, consumerResult{err: c.failErr})
		return 0, c.failErr
	}
	c.results = append(c.results, consumerResult{count: 1})
	return 1, nil
}

type capturingSink struct {
	records []Record
}

func (s *capturingSink) Write(record Record) error {
	s.records = append(s.records, cloneRecord(record))
	return nil
}

type errorSink struct{ err error }

func (s *errorSink) Write(Record) error { return s.err }

type scriptedByteConsumer struct {
	buffer  bytes.Buffer
	results []consumerResult
	calls   int
}

func (c *scriptedByteConsumer) Write(record Record) (int, error) {
	encoded, err := Encode(record)
	if err != nil {
		return 0, fmt.Errorf("encode scripted live record: %w", err)
	}
	if _, err := c.buffer.Write(encoded); err != nil {
		return 0, err
	}
	if c.calls >= len(c.results) {
		return 0, errors.New("scripted live consumer called too many times")
	}
	result := c.results[c.calls]
	c.calls++
	return result.count, result.err
}

func cloneRecord(record Record) Record {
	record.Payload = append([]byte(nil), record.Payload...)
	return record
}

func recordsEqual(left, right Record) bool {
	return left.Version == right.Version && left.Tick == right.Tick && left.Timestamp == right.Timestamp &&
		left.Peer == right.Peer && left.Direction == right.Direction && left.Stream == right.Stream &&
		bytes.Equal(left.Payload, right.Payload)
}
