package cli

import (
	"context"
	"os"
	"os/signal"
	"sync"

	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"

	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

// newSessionSignalContext keeps OS signal ownership at the CLI boundary while
// passing an explicit, run-scoped intent into services. Parent-context
// cancellation follows the normal cancellation path and never marks SIGINT.
func newSessionSignalContext(parent context.Context) (context.Context, func(), serviceSession.SessionCancellationIntent) {
	ctx, cancel := context.WithCancelCause(parent)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	intent := serviceSession.NewSessionCancellationIntent()
	stopped := make(chan struct{})
	watcherDone := make(chan struct{})
	var stopOnce sync.Once

	go func() {
		defer close(watcherDone)
		select {
		case <-signals:
			intent.MarkSIGINT()
			cancel(runtimeSession.ErrLiveUserCancellation)
		case <-parent.Done():
			cause := context.Cause(parent)
			if cause == nil {
				cause = parent.Err()
			}
			cancel(cause)
		case <-stopped:
		}
	}()

	stop := func() {
		stopOnce.Do(func() {
			signal.Stop(signals)
			close(stopped)
			cancel(context.Canceled)
			<-watcherDone
		})
	}
	return ctx, stop, intent
}
