package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const schema = "audio-runtime-c31-streaming-wav-source-consumer.v1"

type report struct {
	Schema        string            `json:"schema"`
	Case          string            `json:"case"`
	Source        string            `json:"source"`
	SourceInputs  string            `json:"source_inputs_sha256"`
	GOOS          string            `json:"goos"`
	GOARCH        string            `json:"goarch"`
	StartedAt     string            `json:"started_at"`
	DurationMS    int64             `json:"duration_ms"`
	CleanShutdown bool              `json:"clean_shutdown"`
	Allocations   *allocationReport `json:"allocations,omitempty"`
	IO            *ioReport         `json:"io,omitempty"`
	Checks        []checkReport     `json:"checks,omitempty"`
	Negative      *negativeReport   `json:"negative_control,omitempty"`
	Error         string            `json:"error,omitempty"`
}

type allocationReport struct {
	PayloadBytes          []int            `json:"payload_bytes"`
	Repetitions           int              `json:"repetitions"`
	Measurements          []allocationCase `json:"measurements"`
	SmallMaxBytes         uint64           `json:"small_max_bytes"`
	LargeMaxBytes         uint64           `json:"large_max_bytes"`
	SmallMedianBytes      uint64           `json:"small_median_bytes"`
	LargeMedianBytes      uint64           `json:"large_median_bytes"`
	LargeMinusSmallMedian int64            `json:"large_minus_small_median"`
	Oracle                allocationOracle `json:"oracle"`
}

type allocationCase struct {
	PayloadBytes []int    `json:"payload_bytes"`
	Bytes        []uint64 `json:"bytes"`
}

type allocationOracle struct {
	PerConstructorLimit uint64 `json:"per_constructor_limit"`
	GrowthLimit         int64  `json:"growth_limit"`
	Pass                bool   `json:"pass"`
}

type ioReport struct {
	OpenReadBytes             int         `json:"open_read_bytes"`
	OpenPayloadReadBytes      int         `json:"open_payload_read_bytes"`
	OpenReadRanges            []readRange `json:"open_read_ranges"`
	OpenSeekCount             int         `json:"open_seek_count"`
	SampleReadBytes           int         `json:"sample_read_bytes"`
	SamplePayloadReadBytes    int         `json:"sample_payload_read_bytes"`
	SampleReadRanges          []readRange `json:"sample_read_ranges"`
	SampleSeekCount           int         `json:"sample_seek_count"`
	FrameReadBytes            int         `json:"frame_read_bytes"`
	FramePayloadReadBytes     int         `json:"frame_payload_read_bytes"`
	FrameReadRanges           []readRange `json:"frame_read_ranges"`
	FrameSeekCount            int         `json:"frame_seek_count"`
	FrameSourceOpenReadRanges []readRange `json:"frame_source_open_read_ranges"`
	FrameSourceOpenSeekCount  int         `json:"frame_source_open_seek_count"`
	CloseCount                int         `json:"close_count"`
	CloseCounts               []int       `json:"close_counts"`
	OpenBoundPass             bool        `json:"open_bound_pass"`
	SampleBoundPass           bool        `json:"sample_bound_pass"`
	FrameBoundPass            bool        `json:"frame_bound_pass"`
	OpenTracePass             bool        `json:"open_trace_pass"`
	SampleTracePass           bool        `json:"sample_trace_pass"`
	FrameTracePass            bool        `json:"frame_trace_pass"`
	Pass                      bool        `json:"pass"`
}

type checkReport struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

type negativeReport struct {
	ExpectedSample int    `json:"expected_sample"`
	ActualSample   int    `json:"actual_sample"`
	Mismatch       string `json:"mismatch,omitempty"`
	Pass           bool   `json:"pass"`
}

type readRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

type countedReadSeekCloser struct {
	data   []byte
	pos    int64
	reads  []readRange
	seeks  int
	closed int
}

func (r *countedReadSeekCloser) Read(destination []byte) (int, error) {
	start := r.pos
	if r.pos >= int64(len(r.data)) {
		r.reads = append(r.reads, readRange{Start: start, End: start})
		return 0, io.EOF
	}
	n := copy(destination, r.data[r.pos:])
	r.pos += int64(n)
	r.reads = append(r.reads, readRange{Start: start, End: r.pos})
	if n < len(destination) {
		return n, io.EOF
	}
	return n, nil
}

func (r *countedReadSeekCloser) Seek(offset int64, whence int) (int64, error) {
	r.seeks++
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = r.pos + offset
	case io.SeekEnd:
		next = int64(len(r.data)) + offset
	default:
		return r.pos, errors.New("invalid seek whence")
	}
	if next < 0 {
		return r.pos, errors.New("negative seek")
	}
	r.pos = next
	return r.pos, nil
}

func (r *countedReadSeekCloser) Close() error {
	r.closed++
	return nil
}

func main() {
	caseName := flag.String("case", "verify", "characterize, verify, or negative-control")
	outputPath := flag.String("output", "", "optional JSON report path")
	flag.Parse()

	started := time.Now()
	result := &report{
		Schema:        schema,
		Case:          *caseName,
		Source:        sourceRevision(),
		SourceInputs:  os.Getenv("C31_SOURCE_INPUTS_SHA256"),
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		StartedAt:     started.UTC().Format(time.RFC3339Nano),
		CleanShutdown: true,
	}
	var runErr error
	switch *caseName {
	case "characterize":
		result.Allocations, runErr = characterizeAllocations()
	case "verify":
		result.IO, runErr = characterizeIO()
		verifyErr := verifySources(result)
		if runErr == nil {
			runErr = verifyErr
		} else if verifyErr != nil {
			runErr = errors.Join(runErr, verifyErr)
		}
	case "negative-control":
		result.Negative, runErr = runNegativeControl()
	default:
		runErr = fmt.Errorf("unsupported case %q", *caseName)
	}
	result.DurationMS = time.Since(started).Milliseconds()
	if runErr != nil {
		result.Error = runErr.Error()
	}

	encoded, marshalErr := json.MarshalIndent(result, "", "  ")
	if marshalErr != nil {
		fmt.Fprintln(os.Stderr, marshalErr)
		os.Exit(1)
	}
	encoded = append(encoded, '\n')
	if *outputPath != "" {
		if writeErr := os.WriteFile(*outputPath, encoded, 0o600); writeErr != nil {
			fmt.Fprintln(os.Stderr, writeErr)
			os.Exit(1)
		}
	}
	if _, writeErr := os.Stdout.Write(encoded); writeErr != nil {
		fmt.Fprintln(os.Stderr, writeErr)
		os.Exit(1)
	}
	if runErr != nil {
		fmt.Fprintln(os.Stderr, runErr)
		os.Exit(1)
	}
}

func sourceRevision() string {
	if source := os.Getenv("C31_SOURCE_REVISION"); source != "" {
		return source
	}
	return "working-tree"
}

func characterizeAllocations() (*allocationReport, error) {
	temporary, err := os.MkdirTemp("", "audio-runtime-c31-alloc-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temporary)

	payloads := []int{4096, 4194304}
	paths := make([]string, len(payloads))
	for index, payloadBytes := range payloads {
		paths[index] = filepath.Join(temporary, fmt.Sprintf("payload-%d.wav", payloadBytes))
		if err := writePayloadWAV(paths[index], payloadBytes); err != nil {
			return nil, err
		}
	}
	for _, path := range paths {
		warm, err := audio.NewFileSource(path, nil)
		if err != nil {
			return nil, fmt.Errorf("warm %s: %w", path, err)
		}
		if err := warm.Close(); err != nil {
			return nil, fmt.Errorf("warm close %s: %w", path, err)
		}
	}

	measurements := make([]allocationCase, 0, len(paths))
	for index, path := range paths {
		bytesPerConstructor := make([]uint64, 0, 5)
		for repetition := 0; repetition < 5; repetition++ {
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			source, openErr := audio.NewFileSource(path, nil)
			if openErr != nil {
				return nil, fmt.Errorf("measure payload %d open: %w", payloads[index], openErr)
			}
			closeErr := source.Close()
			runtime.ReadMemStats(&after)
			if closeErr != nil {
				return nil, fmt.Errorf("measure payload %d close: %w", payloads[index], closeErr)
			}
			bytesPerConstructor = append(bytesPerConstructor, after.TotalAlloc-before.TotalAlloc)
		}
		measurements = append(measurements, allocationCase{PayloadBytes: []int{payloads[index]}, Bytes: bytesPerConstructor})
	}

	small := measurements[0].Bytes
	large := measurements[1].Bytes
	smallMedian := median(small)
	largeMedian := median(large)
	largeMinusSmall := int64(largeMedian) - int64(smallMedian)
	smallMax := maxUint64(small)
	largeMax := maxUint64(large)
	oracle := allocationOracle{
		PerConstructorLimit: 65536,
		GrowthLimit:         16384,
		Pass:                smallMax <= 65536 && largeMax <= 65536 && largeMinusSmall <= 16384,
	}
	return &allocationReport{
		PayloadBytes: payloads, Repetitions: 5, Measurements: measurements,
		SmallMaxBytes: smallMax, LargeMaxBytes: largeMax,
		SmallMedianBytes: smallMedian, LargeMedianBytes: largeMedian,
		LargeMinusSmallMedian: largeMinusSmall, Oracle: oracle,
	}, nil
}

func characterizeIO() (*ioReport, error) {
	samples := []int16{-32768, -12345, -1, 0, 1, 12345, 32767, 8, -9, 10}
	encoded, err := encodedWAV(wavio.Rate16kHz, samples)
	if err != nil {
		return nil, err
	}
	first := &countedReadSeekCloser{data: encoded}
	stream, err := audio.NewWAVSource("counted.wav", first)
	if err != nil {
		return nil, fmt.Errorf("counted source open: %w", err)
	}
	openReadBytes := readBytes(first.reads)
	openPayloadBytes := payloadReadBytes(first.reads, 44, int64(len(encoded)))
	openReadRanges := copyReadRanges(first.reads)
	openSeekCount := first.seeks
	buffer := make([]int16, 7)
	beforeRead := len(first.reads)
	beforeReadSeeks := first.seeks
	count, readErr := stream.ReadSamples(context.Background(), buffer)
	if readErr != nil || count != 7 || !reflect.DeepEqual(buffer, samples[:7]) {
		_ = stream.Close()
		return nil, fmt.Errorf("counted ReadSamples = count %d err %v samples %v", count, readErr, buffer)
	}
	sampleReadBytes := readBytes(first.reads[beforeRead:])
	samplePayloadBytes := payloadReadBytes(first.reads[beforeRead:], 44, int64(len(encoded)))
	sampleReadRanges := copyReadRanges(first.reads[beforeRead:])
	sampleSeekCount := first.seeks - beforeReadSeeks
	if err := stream.Close(); err != nil {
		return nil, fmt.Errorf("counted source close: %w", err)
	}
	if err := stream.Close(); err != nil {
		return nil, fmt.Errorf("counted source repeated close: %w", err)
	}

	second := &countedReadSeekCloser{data: encoded}
	frameSource, err := audio.NewWAVSource("frame-counted.wav", second)
	if err != nil {
		return nil, fmt.Errorf("frame counted source open: %w", err)
	}
	beforeFrame := len(second.reads)
	frameSourceOpenReadRanges := copyReadRanges(second.reads[:beforeFrame])
	frameSourceOpenSeekCount := second.seeks
	frame := make([]int16, audio.FrameSize)
	beforeFrameSeeks := second.seeks
	if err := frameSource.ReadFrame(context.Background(), frame); err != nil {
		_ = frameSource.Close()
		return nil, fmt.Errorf("counted ReadFrame: %w", err)
	}
	frameReadBytes := readBytes(second.reads[beforeFrame:])
	framePayloadBytes := payloadReadBytes(second.reads[beforeFrame:], 44, int64(len(encoded)))
	frameReadRanges := copyReadRanges(second.reads[beforeFrame:])
	frameSeekCount := second.seeks - beforeFrameSeeks
	if err := frameSource.Close(); err != nil {
		return nil, fmt.Errorf("frame counted source close: %w", err)
	}

	result := &ioReport{
		OpenReadBytes: openReadBytes, OpenPayloadReadBytes: openPayloadBytes,
		OpenReadRanges: openReadRanges, OpenSeekCount: openSeekCount,
		SampleReadBytes: sampleReadBytes, SamplePayloadReadBytes: samplePayloadBytes,
		SampleReadRanges: sampleReadRanges, SampleSeekCount: sampleSeekCount,
		FrameReadBytes: frameReadBytes, FramePayloadReadBytes: framePayloadBytes,
		FrameReadRanges: frameReadRanges, FrameSeekCount: frameSeekCount,
		FrameSourceOpenReadRanges: frameSourceOpenReadRanges,
		FrameSourceOpenSeekCount:  frameSourceOpenSeekCount,
		CloseCount:                first.closed + second.closed, CloseCounts: []int{first.closed, second.closed},
	}
	wantOpenRanges := []readRange{{Start: 0, End: 12}, {Start: 12, End: 20}, {Start: 20, End: 36}, {Start: 36, End: 44}}
	wantSampleRanges := []readRange{{Start: 44, End: 58}}
	wantFrameRanges := []readRange{{Start: 44, End: 64}}
	result.OpenBoundPass = openReadBytes <= 64 && openPayloadBytes == 0
	result.SampleBoundPass = samplePayloadBytes == 14 && sampleReadBytes == 14
	result.FrameBoundPass = framePayloadBytes <= audio.FrameSize*2 && frameReadBytes <= audio.FrameSize*2
	result.OpenTracePass = reflect.DeepEqual(openReadRanges, wantOpenRanges) && openSeekCount == 6 &&
		reflect.DeepEqual(frameSourceOpenReadRanges, wantOpenRanges) && frameSourceOpenSeekCount == 6
	result.SampleTracePass = reflect.DeepEqual(sampleReadRanges, wantSampleRanges) && sampleSeekCount == 0
	result.FrameTracePass = reflect.DeepEqual(frameReadRanges, wantFrameRanges) && frameSeekCount == 0
	result.Pass = result.OpenBoundPass && result.SampleBoundPass && result.FrameBoundPass &&
		result.OpenTracePass && result.SampleTracePass && result.FrameTracePass &&
		reflect.DeepEqual(result.CloseCounts, []int{1, 1})
	if !result.Pass {
		return result, fmt.Errorf("streaming IO oracle failed: %+v", result)
	}
	return result, nil
}

func verifySources(result *report) error {
	temporary, err := os.MkdirTemp("", "audio-runtime-c31-source-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)

	fixture := []int16{-32768, -12345, -1, 0, 1, 12345, 32767}
	long := make([]int16, audio.FrameSize+3)
	for index := range long {
		long[index] = int16(index - 240)
	}
	copy(long, fixture)

	checks := make([]checkReport, 0, 10)
	runCheck := func(name string, test func() (bool, string)) {
		passed, detail := test()
		checks = append(checks, checkReport{Name: name, Pass: passed, Detail: detail})
	}
	runCheck("literal-samples", func() (bool, string) { return checkLiteralSamples(filepath.Join(temporary, "literal.wav"), fixture) })
	runCheck("mixed-read-cursor", func() (bool, string) { return checkMixedCursor(filepath.Join(temporary, "mixed.wav"), long) })
	runCheck("sample-tail-and-untouched-buffer", func() (bool, string) { return checkSampleTail(filepath.Join(temporary, "tail.wav")) })
	runCheck("frame-tail-zero-padding", func() (bool, string) { return checkFrameTail(filepath.Join(temporary, "frame-tail.wav"), long) })
	runCheck("empty-wav-eof", func() (bool, string) { return checkEmpty(filepath.Join(temporary, "empty.wav")) })
	runCheck("cancellation-before-read", func() (bool, string) { return checkCancellation(filepath.Join(temporary, "cancel.wav"), fixture) })
	runCheck("upfront-error-identities", func() (bool, string) { return checkUpfrontErrors(temporary, fixture) })
	runCheck("streaming-mutation-visible", func() (bool, string) { return checkMutation(filepath.Join(temporary, "mutation.wav")) })
	runCheck("post-open-truncation-errors", func() (bool, string) { return checkPostOpenTruncation(filepath.Join(temporary, "truncate.wav")) })
	runCheck("caller-owned-stdin", func() (bool, string) { return checkCallerOwnedStdin(fixture) })
	result.Checks = checks
	for _, item := range checks {
		if !item.Pass {
			return fmt.Errorf("source check %s failed: %s", item.Name, item.Detail)
		}
	}
	if result.IO == nil || !result.IO.Pass {
		return errors.New("streaming IO report failed")
	}
	return nil
}

func runNegativeControl() (*negativeReport, error) {
	temporary, err := os.MkdirTemp("", "audio-runtime-c31-negative-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temporary)
	path := filepath.Join(temporary, "negative.wav")
	if err := writeSamplesWAV(path, []int16{1, 12345, -1}); err != nil {
		return nil, err
	}
	source, err := audio.NewFileSource(path, nil)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	buffer := make([]int16, 3)
	count, readErr := source.ReadSamples(context.Background(), buffer)
	if readErr != nil || count != len(buffer) {
		return nil, fmt.Errorf("negative control read count=%d err=%v", count, readErr)
	}
	negative := &negativeReport{ExpectedSample: 12346, ActualSample: int(buffer[1])}
	if negative.ActualSample == negative.ExpectedSample {
		negative.Pass = true
		return negative, nil
	}
	negative.Mismatch = fmt.Sprintf("sample[1] = %d, want mutated expectation %d", negative.ActualSample, negative.ExpectedSample)
	return negative, errors.New(negative.Mismatch)
}

func check(name string, pass bool, detail ...string) checkReport {
	item := checkReport{Name: name, Pass: pass}
	if len(detail) > 0 {
		item.Detail = detail[0]
	}
	return item
}

func checkLiteralSamples(path string, want []int16) (bool, string) {
	if err := writeSamplesWAV(path, want); err != nil {
		return false, err.Error()
	}
	source, err := audio.NewFileSource(path, nil)
	if err != nil {
		return false, err.Error()
	}
	defer source.Close()
	buffer := make([]int16, len(want))
	count, readErr := source.ReadSamples(context.Background(), buffer)
	if readErr != nil || count != len(want) || !reflect.DeepEqual(buffer, want) {
		return false, fmt.Sprintf("count=%d err=%v samples=%v want=%v", count, readErr, buffer, want)
	}
	return true, ""
}

func checkMixedCursor(path string, want []int16) (bool, string) {
	if err := writeSamplesWAV(path, want); err != nil {
		return false, err.Error()
	}
	source, err := audio.NewFileSource(path, nil)
	if err != nil {
		return false, err.Error()
	}
	defer source.Close()
	frame := make([]int16, audio.FrameSize)
	if err := source.ReadFrame(context.Background(), frame); err != nil || !reflect.DeepEqual(frame, want[:audio.FrameSize]) {
		return false, fmt.Sprintf("first frame err=%v", err)
	}
	tail := make([]int16, 3)
	count, readErr := source.ReadSamples(context.Background(), tail)
	if readErr != nil || count != 3 || !reflect.DeepEqual(tail, want[audio.FrameSize:]) {
		return false, fmt.Sprintf("tail count=%d err=%v samples=%v want=%v", count, readErr, tail, want[audio.FrameSize:])
	}
	count, readErr = source.ReadSamples(context.Background(), tail)
	if !errors.Is(readErr, io.EOF) || count != 0 {
		return false, fmt.Sprintf("after cursor count=%d err=%v", count, readErr)
	}
	return true, ""
}

func checkSampleTail(path string) (bool, string) {
	if err := writeSamplesWAV(path, []int16{10, -20, 30}); err != nil {
		return false, err.Error()
	}
	source, err := audio.NewFileSource(path, nil)
	if err != nil {
		return false, err.Error()
	}
	defer source.Close()
	buffer := []int16{0, 0, 777, 888}
	count, readErr := source.ReadSamples(context.Background(), buffer[:2])
	if readErr != nil || count != 2 || !reflect.DeepEqual(buffer, []int16{10, -20, 777, 888}) {
		return false, fmt.Sprintf("first count=%d err=%v buffer=%v", count, readErr, buffer)
	}
	count, readErr = source.ReadSamples(context.Background(), buffer[:2])
	if readErr != nil || count != 1 || buffer[0] != 30 || buffer[1] != -20 {
		return false, fmt.Sprintf("tail count=%d err=%v buffer=%v", count, readErr, buffer)
	}
	count, readErr = source.ReadSamples(context.Background(), buffer[:2])
	if !errors.Is(readErr, io.EOF) || count != 0 {
		return false, fmt.Sprintf("EOF count=%d err=%v", count, readErr)
	}
	return true, ""
}

func checkFrameTail(path string, want []int16) (bool, string) {
	if err := writeSamplesWAV(path, want); err != nil {
		return false, err.Error()
	}
	source, err := audio.NewFileSource(path, nil)
	if err != nil {
		return false, err.Error()
	}
	defer source.Close()
	frame := make([]int16, audio.FrameSize)
	if err := source.ReadFrame(context.Background(), frame); err != nil {
		return false, fmt.Sprintf("first frame: %v", err)
	}
	for index := range frame {
		frame[index] = 777
	}
	if err := source.ReadFrame(context.Background(), frame); err != nil {
		return false, fmt.Sprintf("tail frame: %v", err)
	}
	if !reflect.DeepEqual(frame[:3], want[audio.FrameSize:]) || !reflect.DeepEqual(frame[3:], make([]int16, audio.FrameSize-3)) {
		return false, fmt.Sprintf("tail frame=%v", frame)
	}
	if err := source.ReadFrame(context.Background(), frame); !errors.Is(err, io.EOF) {
		return false, fmt.Sprintf("after tail=%v", err)
	}
	return true, ""
}

func checkEmpty(path string) (bool, string) {
	if err := writeSamplesWAV(path, nil); err != nil {
		return false, err.Error()
	}
	source, err := audio.NewFileSource(path, nil)
	if err != nil {
		return false, err.Error()
	}
	defer source.Close()
	if err := source.ReadFrame(context.Background(), make([]int16, audio.FrameSize)); !errors.Is(err, io.EOF) {
		return false, fmt.Sprintf("empty ReadFrame=%v", err)
	}
	return true, ""
}

func checkCancellation(path string, want []int16) (bool, string) {
	if err := writeSamplesWAV(path, want); err != nil {
		return false, err.Error()
	}
	source, err := audio.NewFileSource(path, nil)
	if err != nil {
		return false, err.Error()
	}
	defer source.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	buffer := make([]int16, 2)
	count, readErr := source.ReadSamples(ctx, buffer)
	if !errors.Is(readErr, context.Canceled) || count != 0 {
		return false, fmt.Sprintf("count=%d err=%v", count, readErr)
	}
	return true, ""
}

func checkUpfrontErrors(directory string, samples []int16) (bool, string) {
	malformed := filepath.Join(directory, "malformed.wav")
	if err := os.WriteFile(malformed, []byte("RIFF\x04\x00\x00\x00WAVE"), 0o600); err != nil {
		return false, err.Error()
	}
	if _, err := audio.NewFileSource(malformed, nil); err == nil || !errors.Is(err, wavio.ErrMalformed) {
		return false, fmt.Sprintf("malformed err=%v", err)
	}

	truncatedPath := filepath.Join(directory, "truncated.wav")
	encoded, err := encodedWAV(wavio.Rate16kHz, samples)
	if err != nil {
		return false, err.Error()
	}
	if err := os.WriteFile(truncatedPath, encoded[:len(encoded)-1], 0o600); err != nil {
		return false, err.Error()
	}
	if _, err := audio.NewFileSource(truncatedPath, nil); err == nil || !errors.Is(err, wavio.ErrTruncated) {
		return false, fmt.Sprintf("truncated err=%v", err)
	}

	ratePath := filepath.Join(directory, "unsupported-rate.wav")
	rateEncoded, err := encodedWAV(44100, []int16{1})
	if err != nil {
		return false, err.Error()
	}
	if err := os.WriteFile(ratePath, rateEncoded, 0o600); err != nil {
		return false, err.Error()
	}
	_, err = audio.NewFileSource(ratePath, nil)
	var formatErr *audio.FormatError
	var unsupported *wavio.UnsupportedError
	var streamErr *audio.StreamError
	if err == nil || !errors.As(err, &formatErr) || !errors.As(err, &unsupported) || errors.As(err, &streamErr) || !errors.Is(err, wavio.ErrUnsupportedRate) || unsupported.Observed != 44100 {
		return false, fmt.Sprintf("unsupported rate err=%v", err)
	}
	return true, ""
}

func checkMutation(path string) (bool, string) {
	if err := writeSamplesWAV(path, []int16{11, 22}); err != nil {
		return false, err.Error()
	}
	source, err := audio.NewFileSource(path, nil)
	if err != nil {
		return false, err.Error()
	}
	defer source.Close()
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return false, err.Error()
	}
	var changed [2]byte
	binary.LittleEndian.PutUint16(changed[:], uint16(321))
	if _, err := file.WriteAt(changed[:], 44); err != nil {
		_ = file.Close()
		return false, err.Error()
	}
	if err := file.Close(); err != nil {
		return false, err.Error()
	}
	buffer := make([]int16, 1)
	count, readErr := source.ReadSamples(context.Background(), buffer)
	if readErr != nil || count != 1 || buffer[0] != 321 {
		return false, fmt.Sprintf("count=%d err=%v sample=%d", count, readErr, buffer[0])
	}
	return true, "streaming mutation observed (snapshot isolation is intentionally not promised)"
}

func checkPostOpenTruncation(path string) (bool, string) {
	if err := writeSamplesWAV(path, []int16{11, 22}); err != nil {
		return false, err.Error()
	}
	source, err := audio.NewFileSource(path, nil)
	if err != nil {
		return false, err.Error()
	}
	defer source.Close()
	if err := os.Truncate(path, 45); err != nil {
		return false, err.Error()
	}
	count, readErr := source.ReadSamples(context.Background(), make([]int16, 2))
	var truncErr *audio.TruncatedPCMError
	if count != 0 || !errors.As(readErr, &truncErr) || !errors.Is(readErr, audio.ErrTruncatedPCM) || truncErr.Bytes != 1 {
		return false, fmt.Sprintf("count=%d err=%v", count, readErr)
	}
	return true, fmt.Sprintf("first read reports %T; subsequent reads are terminal", readErr)
}

func checkCallerOwnedStdin(samples []int16) (bool, string) {
	reader := &trackingReadCloser{Reader: bytes.NewReader(pcmBytes(samples))}
	source, err := audio.NewFileSource("-", reader)
	if err != nil {
		return false, err.Error()
	}
	if err := source.Close(); err != nil {
		return false, err.Error()
	}
	if reader.closed {
		return false, "caller-owned stdin was closed"
	}
	return true, ""
}

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return nil
}

func writePayloadWAV(path string, payloadBytes int) error {
	if payloadBytes%2 != 0 {
		return errors.New("payload must be even")
	}
	header, err := wavio.PCM16Header(wavio.Rate16kHz, uint64(payloadBytes))
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(header[:]); err != nil {
		_ = file.Close()
		return err
	}
	chunk := make([]byte, 32*1024)
	for written := 0; written < payloadBytes; {
		count := min(len(chunk), payloadBytes-written)
		for index := 0; index < count; index += 2 {
			binary.LittleEndian.PutUint16(chunk[index:index+2], uint16((written+index)/2))
		}
		if _, err := file.Write(chunk[:count]); err != nil {
			_ = file.Close()
			return err
		}
		written += count
	}
	return file.Close()
}

func writeSamplesWAV(path string, samples []int16) error {
	encoded, err := encodedWAV(wavio.Rate16kHz, samples)
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o600)
}

func encodedWAV(rate int, samples []int16) ([]byte, error) {
	header, err := wavio.PCM16Header(rate, uint64(len(samples)*2))
	if err != nil {
		return nil, err
	}
	encoded := make([]byte, len(header)+len(samples)*2)
	copy(encoded, header[:])
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(encoded[len(header)+index*2:], uint16(sample))
	}
	return encoded, nil
}

func pcmBytes(samples []int16) []byte {
	encoded := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(encoded[index*2:], uint16(sample))
	}
	return encoded
}

func readBytes(ranges []readRange) int {
	total := 0
	for _, item := range ranges {
		total += int(item.End - item.Start)
	}
	return total
}

func payloadReadBytes(ranges []readRange, payloadStart, payloadEnd int64) int {
	total := int64(0)
	for _, item := range ranges {
		start, end := item.Start, item.End
		if start < payloadStart {
			start = payloadStart
		}
		if end > payloadEnd {
			end = payloadEnd
		}
		if end > start {
			total += end - start
		}
	}
	return int(total)
}

func copyReadRanges(ranges []readRange) []readRange {
	return append([]readRange{}, ranges...)
}

func median(values []uint64) uint64 {
	copyOf := append([]uint64(nil), values...)
	sort.Slice(copyOf, func(left, right int) bool { return copyOf[left] < copyOf[right] })
	return copyOf[len(copyOf)/2]
}

func maxUint64(values []uint64) uint64 {
	var maximum uint64
	for _, value := range values {
		if value > maximum {
			maximum = value
		}
	}
	return maximum
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
