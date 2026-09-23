package agentruntime_test

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	providers "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	providerswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
)

func testModelCatalog() providers.ModelCatalog { return providerswire.NewModelCatalog() }

type countingSessionInferencer struct {
	connects int
}

func (i *countingSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	i.connects++
	return nil, errors.New("provider connection should not be attempted")
}

func assertGoroutinesSettled(t *testing.T, baseline int, operation string) {
	t.Helper()
	deadline := time.Now().Add(750 * time.Millisecond)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline+2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutines after %s = %d, baseline = %d; lifecycle did not settle", operation, runtime.NumGoroutine(), baseline)
}
