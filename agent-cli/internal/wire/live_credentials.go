package wire

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
)

// liveCredentialVault is a process-local host adapter for command-line API
// key overrides. Session requests carry only the one-time selector; the
// provider edge consumes the value while constructing the provider session.
// Environment-backed room references continue to resolve without this vault.
type liveCredentialVault struct {
	mu     sync.Mutex
	values map[string]string
	next   atomic.Uint64
}

func provideLiveCredentialVault() *liveCredentialVault {
	return &liveCredentialVault{values: make(map[string]string)}
}

func provideLiveCredentialReference(vault *liveCredentialVault) cli.LiveCredentialReference {
	if vault == nil {
		return nil
	}
	return vault.Put
}

func (v *liveCredentialVault) Put(value string) string {
	if v == nil || value == "" {
		return ""
	}
	token := fmt.Sprintf("cli-credential:%d", v.next.Add(1))
	v.mu.Lock()
	v.values[token] = value
	v.mu.Unlock()
	return token
}

func (v *liveCredentialVault) Take(reference string) (string, bool) {
	if v == nil || reference == "" {
		return "", false
	}
	v.mu.Lock()
	value, ok := v.values[reference]
	if ok {
		delete(v.values, reference)
	}
	v.mu.Unlock()
	return value, ok
}
