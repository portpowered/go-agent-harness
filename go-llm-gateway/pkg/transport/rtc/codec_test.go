package rtc

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"time"
)

func TestRTCOpusRoundTripProducesNovelPCM(t *testing.T) {
	encoder, err := NewRTCOpusEncoder()
	if err != nil {
		t.Fatal(err)
	}
	decoder, err := NewRTCOpusDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = encoder.Close() }()
	defer func() { _ = decoder.Close() }()

	var previousPayload []byte
	for frameIndex := range 3 {
		source := voicedFrame(frameIndex, 440)
		payload, err := encoder.Encode(context.Background(), source)
		if err != nil {
			t.Fatalf("encode frame %d: %v", frameIndex, err)
		}
		if len(payload) <= 3 {
			t.Fatalf("encode frame %d returned %d bytes, want genuine non-trivial Opus", frameIndex, len(payload))
		}
		if frameIndex > 0 && string(payload) == string(previousPayload) {
			t.Fatalf("encode frame %d reused a payload for distinct PCM", frameIndex)
		}
		previousPayload = append(previousPayload[:0], payload...)

		decoded, err := decoder.Decode(payload)
		if err != nil {
			t.Fatalf("decode frame %d: %v", frameIndex, err)
		}
		if len(decoded) != OpusFrameSamples {
			t.Fatalf("decode frame %d returned %d samples, want %d", frameIndex, len(decoded), OpusFrameSamples)
		}
		if got := codecNormalizedRMSError(source, decoded); got > 0.35 {
			t.Fatalf("frame %d normalized RMS error = %.4f, want <= 0.35", frameIndex, got)
		}
		if got := dbDifference(codecRMS(source), codecRMS(decoded)); math.Abs(got) > 3 {
			t.Fatalf("frame %d RMS difference = %.2f dB, want <= 3 dB", frameIndex, got)
		}
	}
}

func TestRTCOpusPLCUsesVoicedHistoryAndResumesDecode(t *testing.T) {
	encoder, err := NewRTCOpusEncoder()
	if err != nil {
		t.Fatal(err)
	}
	decoder, err := NewRTCOpusDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = encoder.Close() }()
	defer func() { _ = decoder.Close() }()

	first, err := encoder.Encode(context.Background(), voicedFrame(0, 330))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decoder.Decode(first); err != nil {
		t.Fatalf("establish decoder history: %v", err)
	}
	plc, err := decoder.DecodePLC()
	if err != nil {
		t.Fatalf("decode PLC: %v", err)
	}
	if len(plc) != OpusFrameSamples {
		t.Fatalf("PLC returned %d samples, want %d", len(plc), OpusFrameSamples)
	}
	if got := codecRMS(plc); !isFinitePositive(got) {
		t.Fatalf("PLC RMS = %v, want finite non-zero history-dependent energy", got)
	}
	if got := rmsDifference(plc, make([]int16, OpusFrameSamples)); got == 0 {
		t.Fatal("PLC returned an inserted zero frame")
	}

	second, err := encoder.Encode(context.Background(), voicedFrame(1, 330))
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := decoder.Decode(second)
	if err != nil {
		t.Fatalf("decode after PLC: %v", err)
	}
	if len(resumed) != OpusFrameSamples {
		t.Fatalf("resumed decode returned %d samples, want %d", len(resumed), OpusFrameSamples)
	}
}

func TestRTCOpusRejectsInvalidStateAndInputs(t *testing.T) {
	encoder, err := NewRTCOpusEncoder()
	if err != nil {
		t.Fatal(err)
	}
	decoder, err := NewRTCOpusDecoder()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := encoder.Encode(context.Background(), nil); !errors.Is(err, ErrOpusEmptyPCM) {
		t.Fatalf("empty PCM error = %v, want ErrOpusEmptyPCM", err)
	}
	if _, err := encoder.Encode(context.Background(), make([]int16, OpusFrameSamples-1)); !errors.Is(err, ErrOpusInvalidPCM) {
		t.Fatalf("short PCM error = %v, want ErrOpusInvalidPCM", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := encoder.Encode(canceled, voicedFrame(0, 440)); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrOpusEncode) {
		t.Fatalf("canceled encode error = %v, want context.Canceled and ErrOpusEncode", err)
	}
	if _, err := decoder.DecodePLC(); !errors.Is(err, ErrOpusNoHistory) {
		t.Fatalf("cold PLC error = %v, want ErrOpusNoHistory", err)
	}
	if _, err := decoder.Decode(nil); !errors.Is(err, ErrOpusEmptyPayload) {
		t.Fatalf("empty payload error = %v, want ErrOpusEmptyPayload", err)
	}
	if _, err := decoder.Decode([]byte{0xff}); !errors.Is(err, ErrOpusDecode) {
		t.Fatalf("corrupt payload error = %v, want ErrOpusDecode", err)
	}
	if err := decoder.Reset(); err != nil {
		t.Fatalf("decoder reset: %v", err)
	}
	if _, err := decoder.DecodePLC(); !errors.Is(err, ErrOpusNoHistory) {
		t.Fatalf("PLC after reset error = %v, want ErrOpusNoHistory", err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := encoder.Encode(context.Background(), voicedFrame(0, 440)); !errors.Is(err, ErrOpusClosed) {
		t.Fatalf("encode after close error = %v, want ErrOpusClosed", err)
	}
	if err := decoder.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.DecodePLC(); !errors.Is(err, ErrOpusClosed) {
		t.Fatalf("PLC after close error = %v, want ErrOpusClosed", err)
	}
}

func TestRTCOpusConstructorsValidateFormat(t *testing.T) {
	cases := []struct {
		name   string
		config OpusCodecConfig
	}{
		{name: "sample rate", config: OpusCodecConfig{SampleRate: 24000}},
		{name: "channels", config: OpusCodecConfig{Channels: 2}},
		{name: "frame duration", config: OpusCodecConfig{FrameDuration: 10 * time.Millisecond}},
		{name: "bitrate", config: OpusCodecConfig{Bitrate: 1000}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewRTCOpusEncoder(tc.config); !errors.Is(err, ErrOpusInvalidConfiguration) {
				t.Fatalf("encoder error = %v, want ErrOpusInvalidConfiguration", err)
			}
			if _, err := NewRTCOpusDecoder(tc.config); !errors.Is(err, ErrOpusInvalidConfiguration) {
				t.Fatalf("decoder error = %v, want ErrOpusInvalidConfiguration", err)
			}
		})
	}
	if _, err := NewRTCOpusEncoder(OpusCodecConfig{}, OpusCodecConfig{}); !errors.Is(err, ErrOpusInvalidConfiguration) {
		t.Fatalf("multiple configs error = %v, want ErrOpusInvalidConfiguration", err)
	}
}

func TestRTCOpusOutputOwnershipAndIndependentHistory(t *testing.T) {
	encoder, err := NewRTCOpusEncoder()
	if err != nil {
		t.Fatal(err)
	}
	decoderA, err := NewRTCOpusDecoder()
	if err != nil {
		t.Fatal(err)
	}
	decoderB, err := NewRTCOpusDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = encoder.Close() }()
	defer func() { _ = decoderA.Close() }()
	defer func() { _ = decoderB.Close() }()

	source := voicedFrame(0, 440)
	payload, err := encoder.Encode(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decoderA.Decode(payload)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := append([]int16(nil), decoded...)
	if _, err = decoderA.Decode(payload); err != nil {
		t.Fatal(err)
	}
	if string(int16Bytes(decoded)) != string(int16Bytes(snapshot)) {
		t.Fatal("a later decode mutated an earlier returned PCM frame")
	}
	if _, err = decoderB.DecodePLC(); !errors.Is(err, ErrOpusNoHistory) {
		t.Fatalf("independent decoder PLC error = %v, want ErrOpusNoHistory", err)
	}

	inputCopy := append([]int16(nil), source...)
	for i := range source {
		source[i] = 0
	}
	if string(int16Bytes(inputCopy)) == string(int16Bytes(source)) {
		t.Fatal("test did not mutate the caller buffer")
	}
}

func TestRTCOpusLifecycleAliasesAndOperationErrors(t *testing.T) {
	encoder, err := NewOpusEncoder()
	if err != nil {
		t.Fatal(err)
	}
	decoder, err := NewOpusDecoder()
	if err != nil {
		t.Fatal(err)
	}

	if err := encoder.Reset(); err != nil {
		t.Fatalf("encoder reset: %v", err)
	}
	payload, err := encoder.Encode(context.Background(), voicedFrame(0, 440))
	if err != nil {
		t.Fatalf("encode after reset: %v", err)
	}
	if _, err := decoder.Decode(payload); err != nil {
		t.Fatalf("decode through alias: %v", err)
	}
	if err := decoder.Reset(); err != nil {
		t.Fatalf("decoder reset: %v", err)
	}
	if _, err := decoder.DecodePLC(); !errors.Is(err, ErrOpusNoHistory) {
		t.Fatalf("PLC after decoder reset error = %v, want ErrOpusNoHistory", err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Reset(); !errors.Is(err, ErrOpusClosed) {
		t.Fatalf("encoder reset after close error = %v, want ErrOpusClosed", err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatalf("second encoder close: %v", err)
	}
	if err := decoder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Reset(); !errors.Is(err, ErrOpusClosed) {
		t.Fatalf("decoder reset after close error = %v, want ErrOpusClosed", err)
	}
	if err := decoder.Close(); err != nil {
		t.Fatalf("second decoder close: %v", err)
	}

	cause := errors.New("codec cause")
	wrapped := wrapOpus("test", ErrOpusEncode, cause)
	if wrapped == nil || wrapped.Error() == "" {
		t.Fatal("wrapOpus returned an empty error")
	}
	if !errors.Is(wrapped, cause) || !errors.Is(wrapped, ErrOpusEncode) {
		t.Fatalf("wrapped error = %v, want cause and ErrOpusEncode", wrapped)
	}
	var operationErr *OpusOperationError
	if !errors.As(wrapped, &operationErr) || operationErr.Unwrap() != cause {
		t.Fatalf("wrapped error type/unwrap = %#v, want OpusOperationError with cause", operationErr)
	}
	if wrapOpus("test", ErrOpusEncode, nil) != nil {
		t.Fatal("wrapOpus(nil) should return nil")
	}
}

func voicedFrame(frameIndex, frequency int) []int16 {
	frame := make([]int16, OpusFrameSamples)
	for i := range frame {
		t := float64(frameIndex*OpusFrameSamples+i) / OpusSampleRate
		sample := 0.48*math.Sin(2*math.Pi*float64(frequency)*t) + 0.12*math.Sin(2*math.Pi*880*t)
		frame[i] = int16(sample * 32767)
	}
	return frame
}

func codecRMS(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, sample := range samples {
		value := float64(sample)
		sum += value * value
	}
	return math.Sqrt(sum / float64(len(samples)))
}

func codecNormalizedRMSError(want, got []int16) float64 {
	if len(want) != len(got) || len(want) == 0 {
		return math.Inf(1)
	}
	var sum float64
	for i := range want {
		difference := float64(want[i]) - float64(got[i])
		sum += difference * difference
	}
	return math.Sqrt(sum/float64(len(want))) / 32768
}

func dbDifference(source, decoded float64) float64 {
	if source == 0 || decoded == 0 {
		return math.Inf(1)
	}
	return 20 * math.Log10(decoded/source)
}

func isFinitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func rmsDifference(a, b []int16) float64 {
	if len(a) != len(b) {
		return math.Inf(1)
	}
	var sum float64
	for i := range a {
		difference := float64(a[i]) - float64(b[i])
		sum += difference * difference
	}
	return math.Sqrt(sum / float64(len(a)))
}

func int16Bytes(samples []int16) []byte {
	bytes := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(bytes[i*2:], uint16(sample))
	}
	return bytes
}
