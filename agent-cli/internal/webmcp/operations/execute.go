package operations

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

// Execution bounds one direct command run. CommandTimeout is the end-to-end
// deadline (zero selects direct.DefaultCommandTimeout); Timeout is the
// operation's own bound. Watch and Once describe a watch command, which has
// a terminal result even when it is canceled before it starts.
type Execution struct {
	CommandTimeout time.Duration
	Timeout        time.Duration
	Watch          bool
	Once           bool
}

// Execute validates the bounds and runs one command under the end-to-end
// deadline, preferring a browser_disconnected cause in the result.
func Execute(ctx context.Context, execution Execution, run func(context.Context) (any, error)) (any, error) { //nolint:contextcheck // A nil context from legacy callers falls back to Background.
	if execution.CommandTimeout < 0 {
		return nil, direct.InvalidInputError("--command-timeout must not be negative", "/command_timeout")
	}
	if execution.Timeout < 0 {
		return nil, direct.InvalidInputError("--timeout must be positive", "/timeout")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	commandTimeout := execution.CommandTimeout
	if commandTimeout == 0 {
		commandTimeout = direct.DefaultCommandTimeout
	}
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	if execution.Watch && commandCtx.Err() != nil {
		// A canceled watch has a terminal data result and does not need to
		// construct a runtime just to observe that its stream is canceled.
		// This also keeps a legacy, non-cooperative factory off the critical
		// path after an interrupt-before-setup.
		return RunWatchStream(commandCtx, nil, execution.Once)
	}
	data, err := run(commandCtx)
	return data, direct.PreferBrowserDisconnected(err)
}

// WatchRequest is one bounded watch: Timeout bounds only event consumption
// and Once stops after the first event.
type WatchRequest struct {
	Selector Selector
	Timeout  time.Duration
	Once     bool
}

// Watch subscribes before selecting, so the stream observes the selected
// and initial catalog events. The target session must outlive the bounded
// watch stream: passing the watch context to selection would make it the
// target-context parent, and the target would detach at the watch deadline
// before broker cleanup could issue its explicit detach. Selection therefore
// uses the command lifetime, while only event consumption is timed.
func Watch(ctx context.Context, broker webmcp.Broker, request WatchRequest) (WatchData, error) {
	watchCtx := ctx
	if request.Timeout > 0 {
		var cancel context.CancelFunc
		watchCtx, cancel = context.WithTimeout(ctx, request.Timeout)
		defer cancel()
	}
	stream := broker.Watch(watchCtx)
	if _, err := EnsureSelection(ctx, broker, request.Selector); err != nil {
		return WatchData{}, err
	}
	return RunWatchStream(watchCtx, stream, request.Once)
}

// RunWatchStream collects events until the stream ends, ctx is done, the
// first event arrives in once mode, or bounded delivery is lost.
func RunWatchStream(ctx context.Context, stream <-chan webmcp.BrokerEvent, once bool) (WatchData, error) { //nolint:contextcheck // A nil context from legacy callers falls back to Background.
	if ctx == nil {
		ctx = context.Background()
	}
	data := WatchData{Status: WatchStatusEnded, Events: []Event{}}
	for {
		if canceled(ctx) {
			data.Status = WatchStatusCanceled
			return data, nil
		}
		select {
		case <-ctx.Done():
			data.Status = WatchStatusCanceled
			return data, nil
		case event, ok := <-stream:
			if !ok {
				data.Status = closedStreamStatus(ctx)
				return data, nil
			}
			data.Events = append(data.Events, eventFrom(event))
			if status, done := watchEventStatus(event, once); done {
				data.Status = status
				return data, nil
			}
		}
	}
}

func canceled(ctx context.Context) bool {
	return ctx.Err() != nil
}

// closedStreamStatus distinguishes a stream the broker ended from one closed
// because the watch itself was canceled.
func closedStreamStatus(ctx context.Context) string {
	if canceled(ctx) {
		return WatchStatusCanceled
	}
	return WatchStatusEnded
}

func watchEventStatus(event webmcp.BrokerEvent, once bool) (string, bool) {
	if event.Type == webmcp.BrokerEventSessionClosed &&
		(event.Reason == webmcp.BrokerWatchBufferFullReason || event.Reason == webmcp.BrowserEventBufferFullReason) {
		return WatchStatusFailed, true
	}
	if once {
		return WatchStatusOnce, true
	}
	return "", false
}

func eventFrom(event webmcp.BrokerEvent) Event {
	return Event{
		Version:      event.Version,
		Type:         string(event.Type),
		Sequence:     event.Sequence,
		BrowserID:    string(event.BrowserID),
		TargetID:     string(event.TargetID),
		Generation:   event.Generation,
		InvocationID: string(event.InvocationID),
		ToolRef:      string(event.ToolRef),
		State:        string(event.State),
		Reason:       normalize.BoundedText(event.Reason, MaxTextLength),
	}
}
