package codec

import (
	"errors"
	"testing"
)

func TestSampleFormatValidateMatchesByteWidth(t *testing.T) {
	if err := (SampleFormat{Encoding: SampleEncodingPCM, BitsPerSample: 16, ValidBitsPerSample: 16}).Validate(); err != nil {
		t.Fatalf("Validate(PCM16) = %v", err)
	}
	if err := (SampleFormat{Encoding: SampleEncodingPCM, BitsPerSample: 12, ValidBitsPerSample: 12}).Validate(); !errors.Is(err, ErrInvalidSampleFormat) {
		t.Fatalf("Validate(12-bit container) = %v, want ErrInvalidSampleFormat", err)
	}
}
