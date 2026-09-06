package codec

import "testing"

func TestDecodeRTPAudioPayloadPCM16Variants(t *testing.T) {
	for _, codecName := range []string{"L16", "PCM16", "RAW", "audio/l16"} {
		got := DecodeRTPAudioPayload(codecName, []byte{0x80, 0x01, 0xff, 0xfe, 0x7f})
		want := []int16{-32767, -2}
		if len(got) != len(want) {
			t.Fatalf("DecodeRTPAudioPayload(%q) length = %d, want %d", codecName, len(got), len(want))
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("DecodeRTPAudioPayload(%q)[%d] = %d, want %d", codecName, index, got[index], want[index])
			}
		}
	}
}

func TestDecodeRTPAudioPayloadG711Aliases(t *testing.T) {
	tests := []struct {
		codecName string
		payload   []byte
		want      []int16
	}{
		{codecName: "PCMA", payload: []byte{0xd4}, want: []int16{16}},
		{codecName: "G711A", payload: []byte{0xd4}, want: []int16{16}},
		{codecName: "PCMU", payload: []byte{0xff, 0x80, 0x00}, want: []int16{0, 32124, -32124}},
		{codecName: "G711U", payload: []byte{0xff, 0x80, 0x00}, want: []int16{0, 32124, -32124}},
	}
	for _, tt := range tests {
		got := DecodeRTPAudioPayload(tt.codecName, tt.payload)
		if len(got) != len(tt.want) {
			t.Fatalf("DecodeRTPAudioPayload(%q) = %v, want %v", tt.codecName, got, tt.want)
		}
		for index := range tt.want {
			if got[index] != tt.want[index] {
				t.Fatalf("DecodeRTPAudioPayload(%q)[%d] = %d, want %d", tt.codecName, index, got[index], tt.want[index])
			}
		}
	}
}

func TestDecodeRTPAudioPayloadMalformedAndUnknownCompatibility(t *testing.T) {
	if got := DecodeRTPAudioPayload("PCMU", nil); got != nil {
		t.Fatalf("empty payload = %v, want nil", got)
	}
	if got := DecodeRTPAudioPayload("unknown", []byte{0, 1, 2, 3, 4}); len(got) != 5 {
		t.Fatalf("unknown codec sample count = %d, want 5", len(got))
	}
	got := DecodeRTPAudioPayload("unknown", []byte{0, 1, 2})
	want := []int16{1, 256, 512}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("unknown codec malformed sample[%d] = %d, want %d", index, got[index], want[index])
		}
	}
}
