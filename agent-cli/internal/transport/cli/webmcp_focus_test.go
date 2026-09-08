package cli

import (
	"context"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"testing"
)

type focusBridgeFixture struct {
	webmcp.TargetSession
	acquired, released int
}

func (s *focusBridgeFixture) AcquirePageFocus(context.Context) (func(context.Context) error, error) {
	s.acquired++
	return func(context.Context) error { s.released++; return nil }, nil
}
func TestProductionSessionForwardsFocusLeaseToExactRawTarget(t *testing.T) {
	raw := &focusBridgeFixture{}
	session := &productionTargetSession{raw: raw}
	release, err := session.AcquirePageFocus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	session.raw = &focusBridgeFixture{} // cleanup must not look up the new target
	if err := release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if raw.acquired != 1 || raw.released != 1 {
		t.Fatalf("raw=%+v", raw)
	}
}
