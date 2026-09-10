package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash"
	"os"
	"runtime"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const (
	scratchBytes       = 64 * 1024
	allocationBudget   = 256 * 1024
	smallSampleCount   = 1_048_579
	largeSampleCount   = 8_388_611
	patternSampleSize  = 5
	measurementVersion = "c33-v1"

	// The input pattern is intentionally separate from this immutable byte
	// oracle. The fixture hashes below were derived once from these literal
	// bytes, outside the allocation measurement interval.
	oraclePatternBytes    = "\x00\x80\xff\xff\x00\x00\x01\x00\xff\x7f"
	oraclePatternHex      = "0080ffff00000100ff7f"
	oraclePatternSHA256   = "556753b4da9b39610600e40b9673205bc62e4df0f649c9957c6282bd59ab42a0"
	oracleOneSampleBytes  = "\x00\x80"
	oracleOneSampleHex    = "0080"
	oracleOneSampleSHA256 = "085edad400785fca7e7e90b1fac4beb776fc2beee5aa24352d5f39b5d57efcad"
	oracleTailBytes       = "\x00\x80\xff\x7f\xff\xff"
	oracleTailHex         = "0080ff7fffff"
	oracleTailSHA256      = "d68491246ad239fd17f4917785722e6cab3280a1dc3239a5372adacd3981d2b1"
	smallExpectedSHA256   = "e030ba41539620d000c2f320e440a862bb12ae903a54b63e2b110006dd3ec1fb"
	largeExpectedSHA256   = "9fcc93121a4301d2fad4ce2ecae35f1ecb86cee5e7fc04dc1c80c15be36b7f03"
)

var inputPattern = [...]int16{-32768, -1, 0, 1, 32767}

type countingWriter struct {
	digest     hash.Hash
	bytes      uint64
	calls      int
	maxRequest int
}

func newCountingWriter() *countingWriter {
	return &countingWriter{digest: sha256.New()}
}

func (w *countingWriter) Write(payload []byte) (int, error) {
	w.calls++
	if len(payload) > w.maxRequest {
		w.maxRequest = len(payload)
	}
	w.bytes += uint64(len(payload))
	_, err := w.digest.Write(payload)
	return len(payload), err
}

func (w *countingWriter) reset() {
	w.digest.Reset()
	w.bytes = 0
	w.calls = 0
	w.maxRequest = 0
}

func (w *countingWriter) hash() string { return hex.EncodeToString(w.digest.Sum(nil)) }

type oracleWriter struct {
	*countingWriter
	payload []byte
}

func newOracleWriter() *oracleWriter {
	return &oracleWriter{countingWriter: newCountingWriter()}
}

func (w *oracleWriter) Write(payload []byte) (int, error) {
	written, err := w.countingWriter.Write(payload)
	if written > 0 {
		w.payload = append(w.payload, payload[:written]...)
	}
	return written, err
}

func (w *oracleWriter) hex() string { return hex.EncodeToString(w.payload) }

type measurement struct {
	Samples              int    `json:"samples"`
	AdditionalAllocBytes uint64 `json:"additional_alloc_bytes"`
	Bytes                uint64 `json:"bytes"`
	Calls                int    `json:"writer_calls"`
	MaxRequest           int    `json:"max_writer_request"`
	SHA256               string `json:"sha256"`
	ExpectedSHA256       string `json:"expected_sha256"`
	Error                string `json:"error,omitempty"`
}

type oracleReport struct {
	PatternHex        string `json:"pattern_hex"`
	PatternBytes      int    `json:"pattern_bytes"`
	PatternSHA256     string `json:"pattern_sha256"`
	OneSampleHex      string `json:"one_sample_hex"`
	TailHex           string `json:"short_tail_hex"`
	TailBytes         int    `json:"short_tail_bytes"`
	ExactOraclePassed bool   `json:"exact_oracle_passed"`
}

type report struct {
	Version          string         `json:"version"`
	Mode             string         `json:"mode"`
	Source           string         `json:"source"`
	GoVersion        string         `json:"go_version"`
	ScratchBytes     int            `json:"scratch_bytes"`
	AllocationBudget int            `json:"allocation_budget_bytes"`
	ConstructorAlloc uint64         `json:"constructor_alloc_bytes"`
	Measurements     []measurement  `json:"measurements"`
	Oracle           oracleReport   `json:"oracle"`
	RawFileControl   rawFileControl `json:"raw_file_control"`
	ResourceBound    bool           `json:"resource_bound"`
	NegativeControl  bool           `json:"negative_control"`
	CleanShutdown    bool           `json:"clean_shutdown"`
	Error            string         `json:"error,omitempty"`
}

type rawFileControl struct {
	Path             string                 `json:"path"`
	AbsenceChecked   bool                   `json:"absence_checked"`
	ExistedBefore    bool                   `json:"existed_before"`
	ExistsAfter      bool                   `json:"exists_after"`
	Samples          int                    `json:"samples"`
	Bytes            int                    `json:"bytes"`
	SHA256           string                 `json:"sha256"`
	Hex              string                 `json:"hex"`
	FinalTailHex     string                 `json:"final_tail_hex"`
	ExpectedHex      string                 `json:"expected_hex"`
	ExpectedSHA256   string                 `json:"expected_sha256"`
	WriteError       string                 `json:"write_error,omitempty"`
	FirstCloseError  string                 `json:"first_close_error,omitempty"`
	SecondCloseError string                 `json:"second_close_error,omitempty"`
	ClosePassed      bool                   `json:"close_passed"`
	Stdout           stdoutOwnershipControl `json:"stdout"`
	Passed           bool                   `json:"passed"`
}

type stdoutOwnershipControl struct {
	CloseCalls          int    `json:"close_calls"`
	PostCloseWriteBytes int    `json:"post_close_write_bytes"`
	Bytes               int    `json:"bytes"`
	Hex                 string `json:"hex"`
	WriteError          string `json:"write_error,omitempty"`
	FirstCloseError     string `json:"first_close_error,omitempty"`
	SecondCloseError    string `json:"second_close_error,omitempty"`
	Passed              bool   `json:"passed"`
}

type ownershipWriter struct {
	bytes.Buffer
	closeCalls int
}

func (w *ownershipWriter) Close() error {
	w.closeCalls++
	return nil
}

func main() {
	mode := flag.String("mode", "characterize", "characterize or verify")
	negative := flag.Bool("negative-control", false, "mutate the independent oracle and require failure")
	rawFile := flag.String("raw-file", "", "write and verify a fresh raw PCM file at this path")
	output := flag.String("output", "", "write the JSON report to this path")
	flag.Parse()

	result, err := run(*mode, *negative, *rawFile)
	if result == nil {
		result = &report{Version: measurementVersion, Mode: *mode, GoVersion: runtime.Version()}
	}
	if err != nil {
		result.Error = err.Error()
	}
	if *output != "" {
		if writeErr := writeReport(*output, result); writeErr != nil {
			fmt.Fprintln(os.Stderr, writeErr)
			os.Exit(1)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(mode string, negative bool, rawPath string) (*report, error) {
	if mode != "characterize" && mode != "verify" {
		return nil, fmt.Errorf("unsupported mode %q", mode)
	}
	result := &report{
		Version:          measurementVersion,
		Mode:             mode,
		Source:           getenv("C33_SOURCE_REVISION", "working-tree"),
		GoVersion:        runtime.Version(),
		ScratchBytes:     scratchBytes,
		AllocationBudget: allocationBudget,
		NegativeControl:  negative,
		CleanShutdown:    false,
	}

	measurements, constructorAlloc, err := measureSizes()
	result.ConstructorAlloc = constructorAlloc
	result.Measurements = measurements
	if err != nil {
		return result, err
	}
	result.RawFileControl, err = runRawFileControl(rawPath)
	result.CleanShutdown = result.RawFileControl.Passed
	if err != nil {
		return result, err
	}
	result.Oracle, err = runOracle(negative)
	if err != nil {
		return result, err
	}
	if mode == "verify" {
		if err := verifyMeasurements(measurements); err != nil {
			return result, err
		}
		if !result.Oracle.ExactOraclePassed {
			return result, errors.New("literal oracle did not pass")
		}
		result.ResourceBound = true
	} else {
		result.ResourceBound = false
	}
	return result, nil
}

func runRawFileControl(path string) (rawFileControl, error) {
	control := rawFileControl{
		Path:           path,
		Samples:        3,
		ExpectedHex:    oracleTailHex,
		ExpectedSHA256: oracleTailSHA256,
	}
	if path == "" {
		return control, errors.New("raw-file is required for the public file control")
	}
	if _, err := os.Stat(path); err == nil {
		control.ExistedBefore = true
		return control, fmt.Errorf("raw output already exists before the public control: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return control, fmt.Errorf("check raw output absence: %w", err)
	}
	control.AbsenceChecked = true

	sink, err := audio.NewFileSink(path, nil)
	if err != nil {
		return control, err
	}
	writeErr := sink.WriteSamples(context.Background(), []int16{-32768, 32767, -1})
	firstCloseErr := sink.Close()
	secondCloseErr := sink.Close()
	if writeErr != nil {
		control.WriteError = writeErr.Error()
	}
	if firstCloseErr != nil {
		control.FirstCloseError = firstCloseErr.Error()
	}
	if secondCloseErr != nil {
		control.SecondCloseError = secondCloseErr.Error()
	}
	control.ClosePassed = firstCloseErr == nil && secondCloseErr == nil

	data, err := os.ReadFile(path)
	if err != nil {
		return control, fmt.Errorf("read raw output: %w", err)
	}
	control.ExistsAfter = true
	control.Bytes = len(data)
	control.SHA256 = hashBytes(data)
	control.Hex = hex.EncodeToString(data)
	tailStart := len(data) - len(oracleTailBytes)
	if tailStart < 0 {
		tailStart = 0
	}
	control.FinalTailHex = hex.EncodeToString(data[tailStart:])

	stdout := &ownershipWriter{}
	stdoutSink, err := audio.NewFileSink("-", stdout)
	if err != nil {
		return control, err
	}
	stdoutWriteErr := stdoutSink.WriteSamples(context.Background(), []int16{-32768})
	stdoutFirstCloseErr := stdoutSink.Close()
	stdoutSecondCloseErr := stdoutSink.Close()
	postCloseWriteBytes, postCloseWriteErr := stdout.Write([]byte{0xaa})
	if stdoutWriteErr != nil {
		control.Stdout.WriteError = stdoutWriteErr.Error()
	}
	if stdoutFirstCloseErr != nil {
		control.Stdout.FirstCloseError = stdoutFirstCloseErr.Error()
	}
	if stdoutSecondCloseErr != nil {
		control.Stdout.SecondCloseError = stdoutSecondCloseErr.Error()
	}
	control.Stdout.CloseCalls = stdout.closeCalls
	control.Stdout.PostCloseWriteBytes = postCloseWriteBytes
	control.Stdout.Bytes = stdout.Len()
	control.Stdout.Hex = hex.EncodeToString(stdout.Bytes())
	control.Stdout.Passed = stdoutWriteErr == nil && stdoutFirstCloseErr == nil && stdoutSecondCloseErr == nil &&
		postCloseWriteErr == nil && stdout.closeCalls == 0 && postCloseWriteBytes == 1 && control.Stdout.Hex == oracleOneSampleHex+"aa"
	if postCloseWriteErr != nil {
		control.Stdout.WriteError = postCloseWriteErr.Error()
	}

	control.Passed = writeErr == nil && control.ClosePassed && !control.ExistedBefore && control.ExistsAfter &&
		control.Bytes == len(oracleTailBytes) && control.Hex == oracleTailHex &&
		control.SHA256 == oracleTailSHA256 && control.FinalTailHex == oracleTailHex && control.Stdout.Passed
	if !control.Passed {
		return control, fmt.Errorf("raw file control mismatch: %+v", control)
	}
	return control, nil
}

func measureSizes() ([]measurement, uint64, error) {
	measurements := make([]measurement, 0, 2)
	var constructorAlloc uint64
	for _, count := range []int{smallSampleCount, largeSampleCount} {
		samples := makeSamples(count)
		writer := newCountingWriter()
		runtime.GC()
		before := totalAlloc()
		sink, err := audio.NewFileSink("-", writer)
		constructorAlloc = totalAlloc() - before
		if err != nil {
			return measurements, constructorAlloc, err
		}
		writer.reset()
		runtime.GC()
		before = totalAlloc()
		err = sink.WriteSamples(context.Background(), samples)
		allocated := totalAlloc() - before
		measurement := measurement{
			Samples:              count,
			AdditionalAllocBytes: allocated,
			Bytes:                writer.bytes,
			Calls:                writer.calls,
			MaxRequest:           writer.maxRequest,
			SHA256:               writer.hash(),
			ExpectedSHA256:       expectedHash(count),
		}
		if err != nil {
			measurement.Error = err.Error()
		}
		if closeErr := sink.Close(); err == nil && closeErr != nil {
			measurement.Error = closeErr.Error()
		}
		measurements = append(measurements, measurement)
		if err != nil {
			return measurements, constructorAlloc, err
		}
	}
	return measurements, constructorAlloc, nil
}

func runOracle(negative bool) (oracleReport, error) {
	pattern := []int16{inputPattern[0], inputPattern[1], inputPattern[2], inputPattern[3], inputPattern[4]}
	patternWriter := newOracleWriter()
	patternSink, err := audio.NewFileSink("-", patternWriter)
	if err != nil {
		return oracleReport{}, err
	}
	if err := patternSink.WriteSamples(context.Background(), pattern); err != nil {
		return oracleReport{}, err
	}
	if err := patternSink.Close(); err != nil {
		return oracleReport{}, err
	}

	one := newOracleWriter()
	oneSink, err := audio.NewFileSink("-", one)
	if err != nil {
		return oracleReport{}, err
	}
	if err := oneSink.WriteSamples(context.Background(), []int16{-32768}); err != nil {
		return oracleReport{}, err
	}
	if err := oneSink.Close(); err != nil {
		return oracleReport{}, err
	}

	tail := newOracleWriter()
	tailSink, err := audio.NewFileSink("-", tail)
	if err != nil {
		return oracleReport{}, err
	}
	tailSamples := []int16{-32768, 32767, -1}
	if err := tailSink.WriteSamples(context.Background(), tailSamples); err != nil {
		return oracleReport{}, err
	}
	if err := tailSink.Close(); err != nil {
		return oracleReport{}, err
	}

	oracle := oracleReport{
		PatternHex:    patternWriter.hex(),
		PatternBytes:  int(patternWriter.bytes),
		PatternSHA256: patternWriter.hash(),
		OneSampleHex:  one.hex(),
		TailHex:       tail.hex(),
		TailBytes:     int(tail.bytes),
	}
	expectedPatternHex := oraclePatternHex
	expectedPatternHash := oraclePatternSHA256
	expectedOneSampleHex := oracleOneSampleHex
	expectedOneSampleHash := oracleOneSampleSHA256
	expectedTailHex := oracleTailHex
	expectedTailHash := oracleTailSHA256
	if negative {
		// Mutate the expected values, rather than forcing the result, so this
		// control proves the oracle can reject an incorrect sample/hash.
		expectedPatternHex = mutateExpectedOracle(expectedPatternHex)
		expectedPatternHash = mutateExpectedOracle(expectedPatternHash)
	}
	oracle.ExactOraclePassed = patternWriter.bytes == uint64(len(oraclePatternBytes)) &&
		patternWriter.hex() == expectedPatternHex && patternWriter.hash() == expectedPatternHash &&
		one.bytes == uint64(len(oracleOneSampleBytes)) && one.hex() == expectedOneSampleHex && one.hash() == expectedOneSampleHash &&
		tail.bytes == uint64(len(oracleTailBytes)) && tail.hex() == expectedTailHex && tail.hash() == expectedTailHash
	if !oracle.ExactOraclePassed {
		if negative {
			return oracle, errors.New("literal oracle mismatch: negative-control mutated expected sample/hash")
		}
		return oracle, fmt.Errorf("literal oracle mismatch: pattern=%s/%s one=%s/%s tail=%s/%s", patternWriter.hex(), patternWriter.hash(), one.hex(), one.hash(), tail.hex(), tail.hash())
	}
	return oracle, nil
}

func verifyMeasurements(measurements []measurement) error {
	if len(measurements) != 2 {
		return fmt.Errorf("measurement count=%d, want 2", len(measurements))
	}
	for _, item := range measurements {
		if item.Error != "" {
			return fmt.Errorf("write %d samples: %s", item.Samples, item.Error)
		}
		if item.Bytes != uint64(item.Samples*2) {
			return fmt.Errorf("write %d samples emitted %d bytes, want %d", item.Samples, item.Bytes, item.Samples*2)
		}
		if item.SHA256 != item.ExpectedSHA256 {
			return fmt.Errorf("literal hash mismatch for %d samples: got %s want %s", item.Samples, item.SHA256, item.ExpectedSHA256)
		}
		if item.MaxRequest > scratchBytes {
			return fmt.Errorf("max writer request=%d, exceeds documented scratch=%d", item.MaxRequest, scratchBytes)
		}
		if item.AdditionalAllocBytes > allocationBudget {
			return fmt.Errorf("additional allocation=%d, exceeds budget=%d for %d samples", item.AdditionalAllocBytes, allocationBudget, item.Samples)
		}
	}
	if measurements[1].AdditionalAllocBytes > measurements[0].AdditionalAllocBytes+128*1024 {
		return fmt.Errorf("allocation grew with input: small=%d large=%d", measurements[0].AdditionalAllocBytes, measurements[1].AdditionalAllocBytes)
	}
	return nil
}

func makeSamples(count int) []int16 {
	samples := make([]int16, count)
	for index := range samples {
		samples[index] = inputPattern[index%len(inputPattern)]
	}
	return samples
}

func expectedHash(count int) string {
	switch count {
	case smallSampleCount:
		return smallExpectedSHA256
	case largeSampleCount:
		return largeExpectedSHA256
	case patternSampleSize:
		return oraclePatternSHA256
	default:
		return hashRepeatedOracle(count)
	}
}

func hashRepeatedOracle(count int) string {
	digest := sha256.New()
	full := count / patternSampleSize
	for index := 0; index < full; index++ {
		_, _ = digest.Write([]byte(oraclePatternBytes))
	}
	remaining := count % patternSampleSize
	_, _ = digest.Write([]byte(oraclePatternBytes[:remaining*2]))
	return hex.EncodeToString(digest.Sum(nil))
}

func mutateExpectedOracle(value string) string {
	if value == "" {
		return "0"
	}
	replacement := byte('0')
	if value[len(value)-1] == replacement {
		replacement = '1'
	}
	return value[:len(value)-1] + string(replacement)
}

func totalAlloc() uint64 {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.TotalAlloc
}

func hashBytes(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func writeReport(path string, result *report) error {
	payload, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return err
	}
	return nil
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
