package browserconversation

import (
	"context"
	"testing"
)

func TestWaitInvocationRejectsNilAdapter(t *testing.T) {
	var adapter *Adapter
	if _, err := adapter.WaitInvocation(context.Background(), "invocation-1"); err == nil || err.Error() != "browser conversation adapter has no broker" {
		t.Fatalf("WaitInvocation error = %v, want missing-broker error", err)
	}
}
