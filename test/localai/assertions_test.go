package localai

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func requireNonSilentAudio(audio []byte) error {
	rms, err := pcm16RMS(audio)
	if err != nil {
		return err
	}
	if rms <= silenceRMSThreshold {
		return fmt.Errorf("decoded PCM16 RMS %.6f is at or below silence threshold %.6f", rms, silenceRMSThreshold)
	}
	return nil
}

func requireContextFact(reply, fact string) error {
	if !strings.Contains(strings.ToLower(reply), strings.ToLower(fact)) {
		return fmt.Errorf("reply %q does not contain retained turn-one fact %q", reply, fact)
	}
	return nil
}

func requireExactlyOneToolCall(calls []toolCallObservation, expectedName string) error {
	if len(calls) != 1 {
		return fmt.Errorf("function-call assertion: got %d invocations, want exactly one %q", len(calls), expectedName)
	}
	if calls[0].name != expectedName {
		return fmt.Errorf("function-call assertion: got tool %q, want %q", calls[0].name, expectedName)
	}
	return nil
}

func requireImageFact(reply, fact string) error {
	if !strings.Contains(strings.ToUpper(reply), strings.ToUpper(fact)) {
		return fmt.Errorf("reply %q does not contain image-only fact %q", reply, fact)
	}
	return nil
}

func TestNegativeControlsRejectFalsePositives(t *testing.T) {
	tests := []struct {
		name  string
		check func() error
	}{
		{
			name: "silence",
			check: func() error {
				return requireNonSilentAudio(bytes.Repeat([]byte{0}, 1600))
			},
		},
		{
			name: "withheld-history",
			check: func() error {
				return requireContextFact("UNKNOWN", contextFact)
			},
		},
		{
			name: "no-tools",
			check: func() error {
				return requireExactlyOneToolCall(nil, lookupWeatherTool.name)
			},
		},
		{
			name: "no-image",
			check: func() error {
				return requireImageFact("UNKNOWN", imageFact)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.check(); err == nil {
				t.Fatal("negative control unexpectedly satisfied the positive assertion")
			} else {
				t.Logf("intentional negative-control failure: %v", err)
			}
		})
	}
}

func TestPlaybackFlushAssertionRejectsQueuedPlayback(t *testing.T) {
	playback := &playbackConsumer{}
	playback.enqueue([]byte{1, 2, 3, 4})

	if err := requirePlaybackFlushed(playback); err == nil {
		t.Fatal("queued playback without a cancellation flush unexpectedly passed")
	} else {
		t.Logf("intentional negative-control failure: %v", err)
	}

	if flushed := playback.flush(); flushed != 4 {
		t.Fatalf("flushed bytes = %d, want 4", flushed)
	}
	if err := requirePlaybackFlushed(playback); err != nil {
		t.Fatalf("flushed playback rejected: %v", err)
	}
}

func fixtureImageDataURI() (string, error) {
	const (
		glyphWidth  = 5
		glyphHeight = 7
		scale       = 5
		padding     = 10
		spacing     = 1
	)
	patterns := map[rune][]string{
		'O': {"01110", "10001", "10001", "10001", "10001", "10001", "01110"},
		'R': {"11110", "10001", "10001", "11110", "10100", "10010", "10001"},
		'B': {"11110", "10001", "10001", "11110", "10001", "10001", "11110"},
		'I': {"11111", "00100", "00100", "00100", "00100", "00100", "11111"},
		'T': {"11111", "00100", "00100", "00100", "00100", "00100", "00100"},
	}
	word := imageFact
	width := padding*2 + (glyphWidth*scale+spacing*scale)*(len(word)-1) + glyphWidth*scale
	height := padding*2 + glyphHeight*scale
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			canvas.Set(x, y, color.RGBA{R: 245, G: 249, B: 255, A: 255})
		}
	}
	for charIndex, char := range word {
		pattern, ok := patterns[char]
		if !ok {
			return "", fmt.Errorf("missing fixture glyph %q", char)
		}
		for row, line := range pattern {
			for column, bit := range line {
				if bit != '1' {
					continue
				}
				for y := 0; y < scale; y++ {
					for x := 0; x < scale; x++ {
						canvas.Set(padding+charIndex*(glyphWidth+spacing)*scale+column*scale+x, padding+row*scale+y, color.RGBA{R: 15, G: 55, B: 95, A: 255})
					}
				}
			}
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		return "", fmt.Errorf("encode fixture image: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes()), nil
}

func TestFixtureImageIsNonEmptyPNG(t *testing.T) {
	dataURI, err := fixtureImageDataURI()
	if err != nil {
		t.Fatal(err)
	}
	encoded := strings.TrimPrefix(dataURI, "data:image/png;base64,")
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode data URI: %v", err)
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode fixture PNG: %v", err)
	}
	if decoded.Bounds().Empty() {
		t.Fatal("fixture image has empty bounds")
	}
	if decoded.Bounds().Dx() < 100 || decoded.Bounds().Dy() < 40 {
		t.Fatalf("fixture image bounds = %v, want readable dimensions", decoded.Bounds())
	}
}
