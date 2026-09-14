package sessionturns

import "testing"

func TestTurnInputValuesDetectEmptyContent(t *testing.T) {
	text := TurnInput{Text: "text"}
	if text.Empty() || text.Text != "text" {
		t.Fatal("text constructor did not retain text")
	}
	if !(TurnInput{Text: " \n\t"}).Empty() {
		t.Fatal("whitespace text should be empty")
	}
	audio := []byte{1, 2, 3}
	input := TurnInput{Audio: append([]byte(nil), audio...), MediaType: "audio/pcm"}
	audio[0] = 9
	if input.Empty() || input.Audio[0] != 1 || input.MediaType != "audio/pcm" {
		t.Fatalf("audio input = %#v", input)
	}
	if !(TurnInput{}).Empty() {
		t.Fatal("zero input should be empty")
	}
	if ErrEmptyTurn.Error() != "turn content must not be empty" {
		t.Fatal("error code text changed")
	}
}
