package audiorate

import "testing"

func TestErrorCodeRetainsStableMessage(t *testing.T) {
	if got, want := ErrPCM16Truncated.Error(), string(ErrPCM16Truncated); got != want {
		t.Fatalf("error message = %q, want %q", got, want)
	}
}
