package live

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func TestCapabilityEventRequiresRefreshForSemanticCatalogMutations(t *testing.T) {
	tests := []struct {
		name  string
		event session.LiveCapabilityEvent
		want  bool
	}{
		{name: "tools added", event: session.LiveCapabilityEvent{Type: "tools_added"}, want: true},
		{name: "tools removed", event: session.LiveCapabilityEvent{Type: "tools_removed"}, want: true},
		{name: "page navigated", event: session.LiveCapabilityEvent{Type: "page_navigated"}, want: true},
		{name: "frame navigated", event: session.LiveCapabilityEvent{Type: "frame_navigated"}, want: true},
		{name: "catalog ready", event: session.LiveCapabilityEvent{CatalogReady: true}, want: true},
		{name: "invocation completed", event: session.LiveCapabilityEvent{Type: "invocation_completed"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := capabilityEventRequiresRefresh(test.event); got != test.want {
				t.Fatalf("capabilityEventRequiresRefresh(%+v) = %t, want %t", test.event, got, test.want)
			}
		})
	}
}
