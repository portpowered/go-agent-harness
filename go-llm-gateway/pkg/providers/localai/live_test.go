package localai

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// TestLiveRealtimeSession is optional and skips promptly when LocalAI is absent.
func TestLiveRealtimeSession(t *testing.T) {
	provider := New()
	endpoint, err := provider.endpoint()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1750*time.Millisecond)
	defer cancel()
	session, err := provider.ConnectSession(ctx, models.SessionConfig{Model: ModelID})
	if err != nil {
		var connectionErr *ConnectionError
		if errors.As(err, &connectionErr) {
			t.Skipf("endpoint-unreachable: %s: %v", endpoint, err)
		}
		t.Fatalf("connect to LocalAI %s: %v", endpoint, err)
	}
	defer func() { _ = session.Close() }()
	t.Logf("localai model=%s endpoint=%s session=connected", ModelID, endpoint)
}
