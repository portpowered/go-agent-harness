package webmcptest

import "testing"

// closeForTest closes closer and reports a close failure without stopping the
// test.
func closeForTest(tb testing.TB, closer interface{ Close() error }) {
	tb.Helper()
	if err := closer.Close(); err != nil {
		tb.Errorf("close: %v", err)
	}
}

// requireNoError fails the test when a required step returns an error.
func requireNoError(tb testing.TB, err error, step string) {
	tb.Helper()
	if err != nil {
		tb.Fatalf("%s: %v", step, err)
	}
}

// mustType asserts the dynamic type of value and fails the test otherwise.
func mustType[T any](tb testing.TB, value any) T {
	tb.Helper()
	typed, ok := value.(T)
	if !ok {
		tb.Fatalf("value has type %T, want %T", value, typed)
	}
	return typed
}
