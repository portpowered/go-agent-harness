package livehost

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"

	clioutput "github.com/portpowered/go-agent-harness/agent-cli/internal/output"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
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

// redactedMarker replaces a provider credential in rendered run output. It
// matches the marker used by session and room evidence redaction.
const redactedMarker = "[REDACTED]"

// redactedRunError presents a run error with provider credentials replaced
// while keeping the original chain for errors.Is/As classification.
type redactedRunError struct {
	message string
	cause   error
}

func (e *redactedRunError) Error() string { return e.message }

func (e *redactedRunError) Unwrap() error { return e.cause }

// runRedactor removes the session's provider credentials — the set live
// evidence redacts — from failure text the CLI renders. The set is resolved
// once, on first use, so successful runs never resolve credentials.
type runRedactor struct{ secrets func() []string }

func newRunRedactor(request serviceSession.Request, credentialValues func(serviceSession.Request) ([]string, error)) runRedactor {
	return runRedactor{secrets: sync.OnceValue(func() []string { return runSecrets(request, credentialValues) })}
}

func (r runRedactor) text(value string) string {
	if value == "" {
		return value
	}
	for _, secret := range r.secrets() {
		value = strings.ReplaceAll(value, secret, redactedMarker)
	}
	return value
}

func (r runRedactor) error(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	redacted := r.text(message)
	if redacted == message {
		return err
	}
	return &redactedRunError{message: redacted, cause: err}
}

// event redacts the failure text a live event carries before rendering.
func (r runRedactor) event(event session.LiveEvent) session.LiveEvent {
	if event.Error == nil && event.Reason == "" {
		return event
	}
	event.Error = r.error(event.Error)
	event.Reason = r.text(event.Reason)
	return event
}

func runSecrets(request serviceSession.Request, credentialValues func(serviceSession.Request) ([]string, error)) []string {
	values := []string{request.APIKey}
	if credentialValues != nil {
		// A resolution failure leaves only the explicit flag value to redact:
		// the session cannot have used a credential it could not resolve.
		if resolved, err := credentialValues(request); err == nil {
			values = append(values, resolved...)
		}
	}
	seen := make(map[string]struct{}, len(values))
	secrets := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		secrets = append(secrets, value)
	}
	// Longer values first so a secret containing another is never left
	// partially visible.
	sort.SliceStable(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	return secrets
}
