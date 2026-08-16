package transcript

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
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
	const (
		segmentSize = 1
		maxBackups  = 2
		total       = 10
	)
	writer, err := NewWriter(path, WithSegmentSize(segmentSize), WithMaxBackups(maxBackups))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	want := make([]Record, 0, total)
	for index := 1; index <= total; index++ {
		record := NewRecord(uint64(index), time.Unix(int64(index), 0), PeerClient, DirectionIn, StreamRTCData,
			[]byte(fmt.Sprintf("payload-%02d-opaque", index)))
		want = append(want, record)
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
	if len(paths) != maxBackups {
		t.Fatalf("found %d backups, want exactly %d: %v", len(paths), maxBackups, paths)
	}
	for backup := 1; backup <= maxBackups; backup++ {
		backupPath := BackupPath(path, backup)
		if _, err := os.Stat(backupPath); err != nil {
			t.Fatalf("backup %d: %v", backup, err)
		}
	}
	if _, err := os.Stat(BackupPath(path, maxBackups+1)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oldest excess backup still exists: err=%v", err)
	}

	got := readRecordsFromSegments(t, path, maxBackups)
	want = want[total-(maxBackups+1):]
	if len(got) != len(want) {
		t.Fatalf("retained %d records, want exactly %d: %v", len(got), len(want), got)
	}
	for index := range want {
		if !recordsEqual(got[index], want[index]) {
			t.Fatalf("retained record %d = %+v, want %+v", index, got[index], want[index])
		}
	}
}

func TestWriterConcurrentPeerWritesAreCompleteAndUnique(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.jsonl")
	const (
		producers          = 8
		recordsPerProducer = 25
		total              = producers * recordsPerProducer
		segmentSize        = 8 * 1024
		maxBackups         = 8
	)
	writer, err := NewWriter(path, WithSegmentSize(segmentSize), WithMaxBackups(maxBackups))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	type appendResult struct {
		sequence uint64
		record   Record
		err      error
	}
	results := make(chan appendResult, total)
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
				acceptedSequence, err := writer.Append(record)
				results <- appendResult{sequence: acceptedSequence, record: record, err: err}
			}
		}()
	}
	group.Wait()
	close(results)
	bySequence := make(map[uint64]Record, total)
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent Append: %v", result.err)
		}
		if result.sequence == 0 {
			t.Fatal("successful Append returned sequence zero")
		}
		if _, duplicate := bySequence[result.sequence]; duplicate {
			t.Fatalf("duplicate accepted sequence %d", result.sequence)
		}
		bySequence[result.sequence] = result.record
	}
	if len(bySequence) != total {
		t.Fatalf("captured %d accepted sequences, want %d", len(bySequence), total)
	}
	for sequence := uint64(1); sequence <= total; sequence++ {
		if _, ok := bySequence[sequence]; !ok {
			t.Fatalf("missing accepted sequence %d", sequence)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got := readRecordsFromSegments(t, path, maxBackups)
	if len(got) != total {
		t.Fatalf("decoded %d records, want %d", len(got), total)
	}
	if len(readExistingBackupPaths(t, path)) == 0 {
		t.Fatal("concurrent test did not force a rotated segment")
	}
	for index, record := range got {
		sequence := uint64(index + 1)
		want, ok := bySequence[sequence]
		if !ok {
			t.Fatalf("decoded record %d has no accepted sequence", index)
		}
		if !recordsEqual(record, want) {
			t.Fatalf("decoded record %d for sequence %d = %+v, want %+v", index, sequence, record, want)
		}
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
	beforeGoroutines := runtime.NumGoroutine()
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
	afterGoroutines := settleGoroutineCount(beforeGoroutines+2, 500*time.Millisecond)
	if afterGoroutines > beforeGoroutines+2 {
		t.Fatalf("goroutines after Close = %d, before = %d, want within bounded tolerance", afterGoroutines, beforeGoroutines)
	}
}

func TestWriterConcurrentWriteAndCloseLeavesCompleteRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent-close.jsonl")
	const (
		producers          = 6
		recordsPerProducer = 40
		total              = producers * recordsPerProducer
		segmentSize        = 8 * 1024
		maxBackups         = 8
	)
	writer, err := NewWriter(path, WithSegmentSize(segmentSize), WithMaxBackups(maxBackups))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	seed := NewRecord(1, time.Unix(0, 0), PeerClient, DirectionIn, StreamWS, []byte("seed"))
	if err := writer.Write(seed); err != nil {
		t.Fatalf("seed Write: %v", err)
	}

	type writeResult struct {
		record Record
		err    error
	}
	results := make(chan writeResult, total)
	start := make(chan struct{})
	var group sync.WaitGroup
	for producer := 0; producer < producers; producer++ {
		producer := producer
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			for sequence := 0; sequence < recordsPerProducer; sequence++ {
				record := NewRecord(uint64(producer*recordsPerProducer+sequence+2), time.Unix(0, 0),
					Peer(producerPeer(producer)), DirectionOut, StreamRTCData,
					[]byte(fmt.Sprintf("close producer=%d sequence=%d", producer, sequence)))
				results <- writeResult{record: record, err: writer.Write(record)}
			}
		}()
	}
	closeDone := make(chan error, 1)
	group.Add(1)
	go func() {
		defer group.Done()
		<-start
		runtime.Gosched()
		closeDone <- writer.Close()
	}()
	close(start)
	group.Wait()
	close(results)
	if err := <-closeDone; err != nil {
		t.Fatalf("concurrent Close: %v", err)
	}

	accepted := map[string]Record{string(seed.Payload): seed}
	for result := range results {
		if result.err == nil {
			key := string(result.record.Payload)
			if _, duplicate := accepted[key]; duplicate {
				t.Fatalf("duplicate successful payload %q", key)
			}
			accepted[key] = result.record
			continue
		}
		if !errors.Is(result.err, ErrWriterClosed) {
			t.Fatalf("concurrent Write = %v, want nil or ErrWriterClosed", result.err)
		}
	}
	if writer.AcceptedCount() != uint64(len(accepted)) {
		t.Fatalf("AcceptedCount = %d, want %d", writer.AcceptedCount(), len(accepted))
	}
	for index := 0; index < 3; index++ {
		if err := writer.Write(NewRecord(uint64(total+index+2), time.Unix(0, 0), PeerAgent, DirectionIn, StreamWS,
			[]byte(fmt.Sprintf("post-close-%d", index)))); !errors.Is(err, ErrWriterClosed) {
			t.Fatalf("post-close Write %d = %v, want ErrWriterClosed", index, err)
		}
	}

	got := readRecordsFromSegments(t, path, maxBackups)
	if len(got) != len(accepted) {
		t.Fatalf("decoded %d complete records, want %d", len(got), len(accepted))
	}
	seen := make(map[string]bool, len(got))
	for _, record := range got {
		key := string(record.Payload)
		if seen[key] {
			t.Fatalf("duplicate decoded payload %q", key)
		}
		seen[key] = true
		want, ok := accepted[key]
		if !ok {
			t.Fatalf("decoded unexpected payload %q", key)
		}
		if !recordsEqual(record, want) {
			t.Fatalf("decoded record for payload %q = %+v, want %+v", key, record, want)
		}
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

func readExistingBackupPaths(t *testing.T, path string) []string {
	t.Helper()
	paths, err := filepath.Glob(path + ".*")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	return paths
}

func readRecordsFromSegments(t *testing.T, path string, maxBackups int) []Record {
	t.Helper()
	var records []Record
	for backup := maxBackups; backup >= 1; backup-- {
		segment := BackupPath(path, backup)
		if _, err := os.Stat(segment); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			t.Fatalf("stat %s: %v", segment, err)
		}
		records = append(records, readRecordsFromFile(t, segment)...)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat active segment %s: %v", path, err)
	}
	return append(records, readRecordsFromFile(t, path)...)
}

func settleGoroutineCount(maximum int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	count := runtime.NumGoroutine()
	for count > maximum && time.Now().Before(deadline) {
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
		count = runtime.NumGoroutine()
	}
	return count
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
