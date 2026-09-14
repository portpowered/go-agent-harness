package livehost

import (
	"context"
	"errors"
	"io"
	"reflect"

	clioutput "github.com/portpowered/go-agent-harness/agent-cli/internal/output"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
)

// newTerminalEventRenderer creates the invocation-scoped presentation state.
func newTerminalEventRenderer(replay bool) *clioutput.LiveEventRenderer {
	return clioutput.NewLiveEventRenderer(replay)
}

// renderTerminalEventWithRenderer is a presentation-only bridge at the CLI boundary.
func renderTerminalEventWithRenderer(ctx context.Context, out io.Writer, renderer *clioutput.LiveEventRenderer, event session.LiveEvent) error {
	return renderer.Render(ctx, out, event)
}

// AdaptLiveTerminalError replaces the live runner's legacy unresolved-result
// identity with the sessiontrace contract at the CLI boundary. Rebuilding
// joins keeps independent shutdown causes while avoiding duplicate terminal
// text from a compatibility projection.
func AdaptLiveTerminalError(err error) error {
	if err == nil {
		return nil
	}
	var unresolved *session.LiveUnresolvedToolResultsError
	if !errors.As(err, &unresolved) {
		return err
	}
	return replaceLiveUnresolvedError(err)
}

func replaceLiveUnresolvedError(err error) error {
	if reflect.TypeOf(err) == reflect.TypeOf((*session.LiveUnresolvedToolResultsError)(nil)) {
		var typed *session.LiveUnresolvedToolResultsError
		if errors.As(err, &typed) {
			return &sessiontrace.UnresolvedToolResultsError{CallIDs: typed.UnresolvedCallIDs()}
		}
	}
	if typed, ok := err.(interface{ Unwrap() []error }); ok {
		causes := typed.Unwrap()
		replaced := make([]error, len(causes))
		for index, cause := range causes {
			replaced[index] = replaceLiveUnresolvedError(cause)
		}
		return errors.Join(replaced...)
	}
	if typed, ok := err.(interface{ Unwrap() error }); ok {
		cause := typed.Unwrap()
		if cause == nil {
			return err
		}
		return liveTerminalError{message: err.Error(), cause: replaceLiveUnresolvedError(cause)}
	}
	return err
}

type liveTerminalError struct {
	message string
	cause   error
}

func (e liveTerminalError) Error() string { return e.message }
func (e liveTerminalError) Unwrap() error { return e.cause }
