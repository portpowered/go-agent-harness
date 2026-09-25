package direct

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const phaseRuntimeFactory = "runtime_factory"

// InvalidInputError classifies an invalid command input at the JSON path.
func InvalidInputError(message, path string) error {
	return webmcp.NewClassifiedError(webmcp.ErrorInvalidToolInput, message, map[string]any{
		"issues": []webmcp.ToolResultIssue{{Path: path, Code: "invalid"}},
	})
}

// RuntimeUnavailableError reports that no browser runtime is available for
// phase.
func RuntimeUnavailableError(phase string) error {
	return webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "the WebMCP browser runtime is unavailable", map[string]any{
		"phase": phase,
	})
}

// RuntimeFactoryError keeps an already classified factory error and
// otherwise replaces it with a classified construction failure that carries
// no factory-specific detail.
func RuntimeFactoryError(err error) error {
	if err == nil {
		return nil
	}
	var classified *webmcp.ClassifiedError
	if errors.As(err, &classified) && classified != nil {
		return err
	}
	return webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "the WebMCP browser runtime could not be constructed", map[string]any{
		"phase": phaseRuntimeFactory,
	})
}

// RuntimeFactoryFailure is RuntimeFactoryError except that cancellation and
// deadline errors pass through unchanged.
func RuntimeFactoryFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return RuntimeFactoryError(err)
}

// BrowserDisconnectedError walks classified and joined errors instead of
// relying on errors.As alone. A generic target_attach_failed wrapper may
// contain the more important C0 browser_disconnected cause.
func BrowserDisconnectedError(err error) error {
	if err == nil {
		return nil
	}
	var classified *webmcp.ClassifiedError
	if errors.As(err, &classified) && classified != nil && classified.Code == webmcp.ErrorBrowserDisconnected {
		return classified
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if failure := BrowserDisconnectedError(cause); failure != nil {
				return failure
			}
		}
		return nil
	}
	return BrowserDisconnectedError(errors.Unwrap(err))
}

// PreferBrowserDisconnected returns the browser_disconnected cause inside
// err when there is one, and err otherwise.
func PreferBrowserDisconnected(err error) error {
	if failure := BrowserDisconnectedError(err); failure != nil {
		return failure
	}
	return err
}
