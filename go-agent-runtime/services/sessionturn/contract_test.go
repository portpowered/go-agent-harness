package sessionturn

import (
	"errors"
	"strings"
	"testing"
)

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

func TestPublicFailureValuesPreserveCauseIdentity(t *testing.T) {
	if got := AllocatorFunc(func() string { return "allocated" }).Allocate(); got != "allocated" {
		t.Fatalf("allocator function = %q", got)
	}
	cause := errors.New("provider rejected the request")
	if got := sentinelError("sentinel").Error(); got != "sentinel" {
		t.Fatalf("sentinel error = %q", got)
	}

	publication := &PublicationError{Phase: "publish", Sequence: 7, Err: cause}
	if got := publication.Error(); !strings.Contains(got, "phase=publish sequence=7") {
		t.Fatalf("publication error = %q", got)
	}
	if !errors.Is(publication, ErrPublication) || !errors.Is(publication, cause) {
		t.Fatalf("publication error lost identity: %v", publication)
	}
	var nilPublication *PublicationError
	if nilPublication.Error() != ErrPublication.Error() || !errors.Is(nilPublication, ErrPublication) {
		t.Fatal("nil publication error did not preserve sentinel identity")
	}

	capability := &ImageCapabilityError{Model: "model", Capability: "image input"}
	if !errors.Is(capability, ErrImageCapability) || !strings.Contains(capability.Error(), "model") {
		t.Fatalf("capability error = %v", capability)
	}
	file := &ImageFileError{Message: "image failed", Kind: ErrImageInvalidContent, Cause: cause}
	if file.Error() != "image failed" || !errors.Is(file, ErrImageInvalidContent) || !errors.Is(file, cause) {
		t.Fatalf("image file error lost identity: %v", file)
	}
	var nilFile *ImageFileError
	if nilFile.Error() != "<nil>" || nilFile.Unwrap() != nil {
		t.Fatal("nil image file error was not safe")
	}
	empty := &ImageEmptyFileError{Path: "/tmp/empty.png"}
	if !errors.Is(empty, ErrImageEmptyFile) || !strings.Contains(empty.Error(), empty.Path) {
		t.Fatalf("empty image error = %v", empty)
	}
}
