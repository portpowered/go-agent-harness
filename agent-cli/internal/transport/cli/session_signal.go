package cli

import serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"

import (
	"context"
	"os"
	"os/signal"
	"sync"
)

// newSessionSignalContext keeps OS signal ownership at the CLI boundary while
// passing an explicit, run-scoped intent into services. Parent-context
// cancellation follows the normal cancellation path and never marks SIGINT.
func newSessionSignalContext(parent context.Context) (context.Context, func(), *serviceSession.SessionCancellationIntent) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
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
			cancel()
		case <-parent.Done():
			cancel()
		case <-stopped:
		}
	}()

	stop := func() {
		stopOnce.Do(func() {
			signal.Stop(signals)
			close(stopped)
			cancel()
			<-watcherDone
		})
	}
	return ctx, stop, intent
}
