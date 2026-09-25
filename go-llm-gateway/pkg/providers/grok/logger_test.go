package grok

import (
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// testGrokSessionID is the provider session identifier used by fixtures.
const testGrokSessionID = "sess-xyz"

// closeForTest closes a test-owned resource and reports an unexpected close
// failure without stopping the test.
func closeForTest(t testing.TB, resource io.Closer) {
	t.Helper()
	if err := resource.Close(); err != nil {
		t.Errorf("close %T: %v", resource, err)
	}
}

// mustMarshalFixture encodes a fixture value built by the test itself; an
// encoding failure is a broken fixture, not a behavior under test.
func mustMarshalFixture(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal test fixture: %v", err))
	}
	return data
}

func grokSessionForTest(t testing.TB, session messages.Session) *grokSession {
	t.Helper()
	grok, ok := session.(*grokSession)
	if !ok {
		t.Fatalf("session type = %T, want *grokSession", session)
	}
	return grok
}
