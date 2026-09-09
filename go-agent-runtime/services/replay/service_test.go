package replay

import "testing"

func TestStrictPreparedIsOpaque(t *testing.T) {
	var prepared StrictPrepared
	if prepared != nil {
		t.Fatal("zero StrictPrepared interface is unexpectedly non-nil")
	}
}

func TestReplayPublicValueContracts(t *testing.T) {
	if got := ErrBundleIncomplete.Error(); got != "replay bundle is incomplete" {
		t.Fatalf("error code = %q, want stable text", got)
	}
	if !(CaptureInspection{Kind: CaptureKindRealtime}).IsRealtime() {
		t.Fatal("realtime capture was not classified as realtime")
	}
	if (CaptureInspection{Kind: CaptureKindTurn}).IsRealtime() {
		t.Fatal("turn capture was classified as turn")
	}
}
