package transcript

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTeePreservesLiveResultAndRecordsAcceptedFrames(t *testing.T) {
	wantError := errors.New("live consumer stopped")
	inputs := []Record{
		NewRecord(1, time.Unix(1, 0), PeerClient, DirectionIn, StreamWS, []byte("one")),
		NewRecord(2, time.Unix(2, 0), PeerAgent, DirectionOut, StreamRTCAudio, []byte{0xff, 0x00}),
		NewRecord(3, time.Unix(3, 0), PeerClient, DirectionOut, StreamRTCData, []byte("three")),
	}

	baseline := &capturingConsumer{failAt: 2, failErr: wantError}
	for _, input := range inputs {
		_, _ = baseline.write(input)
	}

	sink := &capturingSink{}
	teeConsumer := &capturingConsumer{failAt: 2, failErr: wantError}
	tee := NewTee(teeConsumer, sink)
	for index, input := range inputs {
		gotCount, gotErr := tee.Write(input)
		wantCount, wantErr := baseline.results[index].count, baseline.results[index].err
		if gotCount != wantCount || gotErr != wantErr {
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
		if gotCount != wantCount || gotErr != wantErr {
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

	gotTranscript := readRecordsFromSegments(t, path, maxBackups)
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
