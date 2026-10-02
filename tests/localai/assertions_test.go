package localai

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// serverEventError is the realtime server event type that reports a failure.
const serverEventError = "error"

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
				return requireExactlyOneToolCall(nil, lookupWeatherTool().name)
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

const contextFact = "cobalt-17"

const imageFact = "ORBIT"

// lookupWeatherTool is the single tool offered to the model-chosen function
// call behavior and its controls.
func lookupWeatherTool() toolDefinition {
	return toolDefinition{
		name:        "lookup_weather",
		description: "Look up the weather for one city.",
		parameters: map[string]toolParameter{
			"city": {typeName: "string", description: "City name."},
		},
		required: []string{"city"},
	}
}

func openAIInputRate() int {
	// Keep the behavior body shared while allowing endpoint-specific audio
	// encoding details required by the two realtime services.
	return 24000
}
