//go:build stress

package stress

import (
	"bytes"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// inferenceEntry is the minimal package-local response fixture needed by the
// stress-only inferencer. The ordinary media harness remains shared through
// internal/support; stress keeps its own concurrency-oriented double isolated.
type inferenceEntry struct {
	result messages.InferenceResult
	chunks []string
}

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
