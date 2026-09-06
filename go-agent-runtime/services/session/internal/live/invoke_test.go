package live

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func TestMediaPumpProviderCloseIsAnExpectedStop(t *testing.T) {
	err := fmt.Errorf("device write: %w", session.ErrLiveClosed)
	if !isExpectedMediaPumpError(err) {
		t.Fatal("provider close error was not classified as an expected pump stop")
	}
	if shouldCancelMediaPump(err, context.Background()) {
		t.Fatal("provider close error requested a second session cancellation")
	}
	if isExpectedMediaPumpError(errors.New("device write failed")) {
		t.Fatal("unrelated device failure was classified as an expected stop")
	}
}
