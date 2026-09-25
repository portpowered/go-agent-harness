package sessionturn_test

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const truncationMarker = "..."

func TestErrorsKeepStableText(t *testing.T) {
	if sessionturn.ErrTurnAlreadyActive.Error() != "turn start while another turn is active" || sessionturn.ErrToolTimeout.Error() != "tool execution timed out" {
		t.Fatal("sentinel text changed")
	}
	if !(sessionturn.TurnInput{Text: " "}).Empty() || (sessionturn.TurnInput{Audio: []byte{1}}).Empty() {
		t.Fatal("TurnInput.Empty misclassified input")
	}
}

func TestPublicationErrorIsBoundedAndClassified(t *testing.T) {
	cause := errors.New(strings.Repeat("x", sessionturn.MaxPublicationErrorText*2))
	err := &sessionturn.PublicationError{Phase: "broker_event_refresh", Sequence: 7, Err: cause}
	if !errors.Is(err, sessionturn.ErrPublication) || !errors.Is(err, cause) || !strings.HasSuffix(err.Error(), truncationMarker) {
		t.Fatalf("publication error = %v", err)
	}
	if !strings.Contains((&sessionturn.PublicationError{}).Error(), "unknown error") || (&sessionturn.PublicationError{}).CauseText() != "" {
		t.Fatal("empty cause was not reported as unknown")
	}
	var nilErr *sessionturn.PublicationError
	if nilErr.Error() != sessionturn.ErrPublication.Error() || !errors.Is(nilErr, sessionturn.ErrPublication) {
		t.Fatal("nil publication error lost its identity")
	}
}

func TestImageErrorsExposeKindAndCause(t *testing.T) {
	fileErr := &sessionturn.ImageFileError{Path: "a.png", Kind: sessionturn.ErrImageMissingFile, Cause: fs.ErrNotExist, Message: "session image missing"}
	if !errors.Is(fileErr, sessionturn.ErrImageMissingFile) || !errors.Is(fileErr, fs.ErrNotExist) || fileErr.Error() != "session image missing" {
		t.Fatalf("file error = %v", fileErr)
	}
	capabilityErr := &sessionturn.ImageCapabilityError{Model: "m", Capability: sessionturn.ImageInputCapability}
	if !errors.Is(capabilityErr, sessionturn.ErrImageCapability) || capabilityErr.Error() != `model "m" does not support image input capability` {
		t.Fatalf("capability error = %v", capabilityErr)
	}
	emptyErr := &sessionturn.ImageEmptyFileError{Path: "e.png"}
	if !errors.Is(emptyErr, sessionturn.ErrImageEmptyFile) || emptyErr.Error() != `session image "e.png" is empty` {
		t.Fatalf("empty error = %v", emptyErr)
	}
}
