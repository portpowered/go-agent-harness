package transcript

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWriterWritesDecodableOpaqueRecordsInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writer, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	want := []Record{
		NewRecord(1, time.Unix(1, 0), PeerClient, DirectionIn, StreamWS, []byte{0xff, 0x00, 0x7f}),
		NewRecord(2, time.Unix(2, 0), PeerAgent, DirectionOut, StreamRTCAudio, []byte("{\n  opaque: true\n}")),
		NewRecord(3, time.Unix(3, 0), PeerClient, DirectionOut, StreamRTCData, []byte{0x00, 0xc3, 0x28}),
	}
	for index, record := range want {
		sequence, err := writer.Append(record)
		if err != nil {
			t.Fatalf("Append %d: %v", index, err)
		}
		if sequence != uint64(index+1) {
			t.Fatalf("Append %d sequence = %d, want %d", index, sequence, index+1)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got := readRecordsFromFile(t, path)
	if len(got) != len(want) {
		t.Fatalf("decoded %d records, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index].Tick != want[index].Tick || got[index].Peer != want[index].Peer ||
			got[index].Direction != want[index].Direction || got[index].Stream != want[index].Stream ||
			!bytes.Equal(got[index].Payload, want[index].Payload) {
			t.Errorf("record %d = %+v, want %+v", index, got[index], want[index])
		}
	}
}

func TestWriterNormalizesBoundedDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "defaults.jsonl")
	writer, err := NewWriterWithConfig(path, WriterConfig{SegmentSize: 0, MaxBackups: -1})
	if err != nil {
		t.Fatalf("NewWriterWithConfig: %v", err)
	}
	defer func() { _ = writer.Close() }()
	if writer.SegmentSize() != DefaultSegmentSize {
		t.Fatalf("SegmentSize = %d, want %d", writer.SegmentSize(), DefaultSegmentSize)
	}
	if writer.MaxBackups() != DefaultMaxBackups {
		t.Fatalf("MaxBackups = %d, want %d", writer.MaxBackups(), DefaultMaxBackups)
	}
}

func TestWriterRotatesOnlyCompleteRecordsAndRemovesOldestBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rolling.jsonl")
	writer, err := NewWriter(path, WithSegmentSize(150), WithMaxBackups(2))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for index := 1; index <= 10; index++ {
		record := NewRecord(uint64(index), time.Unix(int64(index), 0), PeerClient, DirectionIn, StreamRTCData,
			[]byte(fmt.Sprintf("payload-%02d-opaque", index)))
		if err := writer.Write(record); err != nil {
			t.Fatalf("Write %d: %v", index, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	paths, err := filepath.Glob(path + ".*")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(paths) > 2 {
		t.Fatalf("found %d backups, want at most 2: %v", len(paths), paths)
	}
	sort.Strings(paths)
	orderedPaths := make([]string, 0, len(paths)+1)
	for index := len(paths); index >= 1; index-- {
		orderedPaths = append(orderedPaths, BackupPath(path, index))
	}
	orderedPaths = append(orderedPaths, path)
	var got []Record
	for _, segment := range orderedPaths {
		if _, err := os.Stat(segment); errors.Is(err, os.ErrNotExist) {
			continue
		}
		got = append(got, readRecordsFromFile(t, segment)...)
	}
	if len(got) == 0 || len(got) >= 10 {
		t.Fatalf("retained %d records, want a non-empty bounded suffix of 10", len(got))
	}
	for index, record := range got {
		wantTick := uint64(10 - len(got) + index + 1)
		if record.Tick != wantTick {
			t.Fatalf("retained record %d tick = %d, want %d; records=%v", index, record.Tick, wantTick, got)
		}
	}
}

func TestWriterConcurrentPeerWritesAreCompleteAndUnique(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.jsonl")
	writer, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	const producers = 8
	const recordsPerProducer = 25
	errs := make(chan error, producers*recordsPerProducer)
	var group sync.WaitGroup
	for producer := 0; producer < producers; producer++ {
		producer := producer
		group.Add(1)
		go func() {
			defer group.Done()
			for sequence := 0; sequence < recordsPerProducer; sequence++ {
				record := NewRecord(uint64(producer*recordsPerProducer+sequence+1), time.Unix(0, 0),
					Peer(producerPeer(producer)), DirectionOut, StreamRTCData,
					[]byte(fmt.Sprintf("producer=%d sequence=%d", producer, sequence)))
				if err := writer.Write(record); err != nil {
					errs <- err
				}
			}
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got := readRecordsFromFile(t, path)
	wantCount := producers * recordsPerProducer
	if len(got) != wantCount {
		t.Fatalf("decoded %d records, want %d", len(got), wantCount)
	}
	seen := make(map[string]bool, len(got))
	for _, record := range got {
		key := string(record.Payload)
		if seen[key] {
			t.Fatalf("duplicate payload %q", key)
		}
		seen[key] = true
	}
}

func TestWriterSinkFailureIsOneWayAndReportedOnce(t *testing.T) {
	sinkErr := errors.New("disk full")
	sink := &failingTranscriptSink{failAfter: 1, err: sinkErr}
	var reports []error
	writer, err := NewWriterOn(sink, WithDegradationReporter(func(err error) {
		reports = append(reports, err)
	}))
	if err != nil {
		t.Fatalf("NewWriterOn: %v", err)
	}
	record := NewRecord(1, time.Unix(0, 0), PeerClient, DirectionIn, StreamWS, []byte("first"))
	if err := writer.Write(record); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	secondErr := writer.Write(NewRecord(2, time.Unix(0, 0), PeerAgent, DirectionOut, StreamWS, []byte("second")))
	if !errors.Is(secondErr, ErrTranscriptDegraded) || !errors.Is(secondErr, sinkErr) {
		t.Fatalf("second Write error = %v, want degradation and sink cause", secondErr)
	}
	if writer.State() != WriterDegraded || writer.AcceptedCount() != 1 {
		t.Fatalf("status = %+v, want degraded with one accepted record", writer.Status())
	}
	thirdErr := writer.Write(NewRecord(3, time.Unix(0, 0), PeerClient, DirectionIn, StreamWS, []byte("third")))
	if !errors.Is(thirdErr, sinkErr) || thirdErr != secondErr {
		t.Fatalf("third Write error = %v, want stable first degradation %v", thirdErr, secondErr)
	}
	if len(reports) != 1 || !errors.Is(reports[0], sinkErr) {
		t.Fatalf("reports = %v, want one report retaining sink cause", reports)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestWriterCloseReleasesFileAndStabilizesPostCloseWrites(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "close.jsonl")
	writer, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := writer.Write(NewRecord(1, time.Unix(0, 0), PeerClient, DirectionIn, StreamWS, []byte("close"))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
	if writer.State() != WriterClosed {
		t.Fatalf("State = %q, want closed", writer.State())
	}
	if err := writer.Write(NewRecord(2, time.Unix(0, 0), PeerAgent, DirectionOut, StreamWS, []byte("after"))); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("post-close Write = %v, want ErrWriterClosed", err)
	}
	renamed := filepath.Join(directory, "renamed.jsonl")
	if err := os.Rename(path, renamed); err != nil {
		t.Fatalf("rename after Close: %v", err)
	}
}

type failingTranscriptSink struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	writeCall int
	failAfter int
	err       error
	closed    bool
}

func (s *failingTranscriptSink) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeCall++
	if s.writeCall > s.failAfter {
		return 0, s.err
	}
	return s.buffer.Write(data)
}

func (s *failingTranscriptSink) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func producerPeer(producer int) string {
	if producer%2 == 0 {
		return string(PeerClient)
	}
	return string(PeerAgent)
}

func readRecordsFromFile(t *testing.T, path string) []Record {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = file.Close() }()
	var records []Record
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || strings.TrimSpace(string(line)) == "" {
			t.Fatalf("empty JSONL line in %s", path)
		}
		record, err := Decode(append(append([]byte(nil), line...), '\n'))
		if err != nil {
			t.Fatalf("decode %s line %d: %v", path, len(records), err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return records
}

var _ io.WriteCloser = (*failingTranscriptSink)(nil)
