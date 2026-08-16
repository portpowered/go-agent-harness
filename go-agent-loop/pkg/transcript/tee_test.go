package transcript

import (
	"bytes"
	"errors"
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

func cloneRecord(record Record) Record {
	record.Payload = append([]byte(nil), record.Payload...)
	return record
}

func recordsEqual(left, right Record) bool {
	return left.Version == right.Version && left.Tick == right.Tick && left.Timestamp == right.Timestamp &&
		left.Peer == right.Peer && left.Direction == right.Direction && left.Stream == right.Stream &&
		bytes.Equal(left.Payload, right.Payload)
}
