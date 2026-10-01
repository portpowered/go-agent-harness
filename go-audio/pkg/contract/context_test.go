package contract_test

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/contract"
)

func TestContextOrBackgroundKeepsCallerContextAndTreatsNilAsUncancelled(t *testing.T) {
	caller, cancel := context.WithCancel(t.Context())
	if got := contract.ContextOrBackground(caller); got != caller {
		t.Fatalf("ContextOrBackground(caller) = %v, want the caller context", got)
	}
	cancel()
	if err := contract.ContextOrBackground(caller).Err(); err == nil {
		t.Fatal("ContextOrBackground(cancelled caller) lost the caller's cancellation")
	}

	var nilContext context.Context
	root := contract.ContextOrBackground(nilContext)
	if root == nil || root.Err() != nil || root.Done() != nil {
		t.Fatalf("ContextOrBackground(nil) = %v, want an uncancelled root context", root)
	}
}
