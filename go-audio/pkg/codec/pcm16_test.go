package codec

import (
	"bytes"
	"errors"
	"testing"
)

func TestPCM16AndBase64RoundTrip(t *testing.T) {
	want := []int16{-32768, -1, 0, 1, 32767}
	encoded := EncodePCM16(want)
	if !bytes.Equal(encoded, []byte{0x00, 0x80, 0xff, 0xff, 0, 0, 1, 0, 0xff, 0x7f}) {
		t.Fatalf("PCM16 bytes = %v", encoded)
	}
	got, err := DecodePCM16(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("decoded %d samples, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d = %d, want %d", i, got[i], want[i])
		}
	}
	b64 := EncodePCM16Base64(want)
	got, err = DecodePCM16Base64(b64)
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("base64 sample %d = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestPayloadValidationIsBounded(t *testing.T) {
	if _, err := DecodeBase64WithLimit("AAAAAA==", 2); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("oversized payload error = %v", err)
	}
	if _, err := DecodeBase64("not base64!"); !errors.Is(err, ErrInvalidBase64) {
		t.Fatalf("invalid base64 error = %v", err)
	}
	if _, err := DecodePCM16([]byte{1}); !errors.Is(err, ErrPCM16OddLength) {
		t.Fatalf("odd PCM error = %v", err)
	}
	if err := ValidatePCM16([]byte{0, 0}, 1); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("oversized PCM validation error = %v", err)
	}
	if _, err := DecodePCM16WithLimit([]byte{0, 0}, 0); !errors.Is(err, ErrInvalidPayloadLimit) {
		t.Fatalf("invalid limit error = %v", err)
	}
	if err := EncodePCM16Into(make([]byte, 1), []int16{1}); !errors.Is(err, ErrPCM16BufferTooSmall) {
		t.Fatalf("short destination error = %v", err)
	}
	if _, err := EncodePCM16WithLimit([]int16{1, 2}, 2); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("oversized encode error = %v", err)
	}
	large := make([]int16, MaxPCM16Bytes/2+1)
	if encoded := EncodePCM16(large); len(encoded) != len(large)*2 {
		t.Fatalf("lossless encode length = %d, want %d", len(encoded), len(large)*2)
	}
	wide := make([]byte, MaxPCM16Bytes+2)
	if _, err := DecodePCM16WithLimit(wide, MaxPCM16Bytes+2); err != nil {
		t.Fatalf("explicit larger decode limit = %v", err)
	}
}

func TestLegacyBase64AcceptsHistoricalVariants(t *testing.T) {
	want := []byte{251, 255}
	variants := []string{"+//=", "+//", "-__=", "-__"}
	for _, encoded := range variants {
		got, err := DecodeLegacyBase64(encoded)
		if err != nil {
			t.Fatalf("DecodeLegacyBase64(%q): %v", encoded, err)
		}
		if string(got) != string(want) {
			t.Fatalf("decoded %q = %v, want %v", encoded, got, want)
		}
	}
	if _, err := DecodeLegacyBase64WithLimit("AAEC", 2); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("oversized legacy payload error = %v", err)
	}
}

func FuzzDecodePCM16Base64NeverPanics(f *testing.F) {
	f.Add("")
	f.Add("AAAA")
	f.Add("not base64")
	f.Fuzz(func(t *testing.T, encoded string) {
		_, _ = DecodePCM16Base64WithLimit(encoded, 1024)
	})
}
