package service

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiocodec"
)

func TestFormatHintsCoverSupportedMediaTypes(t *testing.T) {
	tests := []struct {
		hint   string
		format audiocodec.InputFormat
	}{
		{"audio/wav", audiocodec.FormatWAV},
		{".mp3", audiocodec.FormatMP3},
		{"audio/flac", audiocodec.FormatFLAC},
		{"ogg", audiocodec.FormatOGG},
		{"audio/opus", audiocodec.FormatOpus},
		{"audio/aac", audiocodec.FormatAAC},
		{"audio/x-m4a", audiocodec.FormatM4A},
		{"video/webm", audiocodec.FormatWebM},
	}
	for _, test := range tests {
		got, ok := formatFromHint(test.hint)
		if !ok || got != test.format {
			t.Errorf("formatFromHint(%q) = %q, %v; want %q, true", test.hint, got, ok, test.format)
		}
	}
	if _, ok := formatFromHint("application/octet-stream"); ok {
		t.Fatal("unsupported format hint was accepted")
	}
}

func TestFormatDetectionCoversRawFrameSyncAndMIME(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  audiocodec.InputFormat
	}{
		{"mp3 sync", []byte{0xff, 0xfb}, audiocodec.FormatMP3},
		{"aac sync", []byte{0xff, 0xf1}, audiocodec.FormatAAC},
		{"wav mime", []byte("RIFF"), audiocodec.FormatWAV},
	}
	for _, test := range tests {
		got, ok := formatFromBytes(test.input)
		if !ok || got != test.want {
			t.Errorf("formatFromBytes(%s) = %q, %v; want %q, true", test.name, got, ok, test.want)
		}
	}
}
