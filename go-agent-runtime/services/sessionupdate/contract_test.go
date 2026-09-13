package sessionupdate

import (
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestUpdateSendErrorPreservesStatusAndCause(t *testing.T) {
	cause := errors.New("cancelled")
	err := &UpdateSendError{Status: messages.SessionSendCancelled, Err: cause}
	if err.Error() != "session update send failed: cancelled: cancelled" {
		t.Fatalf("Error() = %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("UpdateSendError did not unwrap cause")
	}
	var nilErr *UpdateSendError
	if nilErr.Error() != "<nil>" || nilErr.Unwrap() != nil {
		t.Fatal("nil UpdateSendError behavior changed")
	}
}
