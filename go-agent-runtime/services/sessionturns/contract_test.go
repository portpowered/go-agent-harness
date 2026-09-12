package sessionturns

import "testing"

func TestInputConstructorsCopyAudioAndDetectEmptyValues(t *testing.T) {
	text := NewTextTurnInput("text")
	if text.Empty() || text.Text != "text" {
		t.Fatal("text constructor did not retain text")
	}
	if !NewTextTurnInput(" \n\t").Empty() {
		t.Fatal("whitespace text should be empty")
	}
	audio := []byte{1, 2, 3}
	input := NewAudioTurnInput(audio, "audio/pcm")
	audio[0] = 9
	if input.Empty() || input.Audio[0] != 1 || input.MediaType != "audio/pcm" {
		t.Fatalf("audio input = %#v", input)
	}
	if !(TurnInput{}).Empty() {
		t.Fatal("zero input should be empty")
	}
}
