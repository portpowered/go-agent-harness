package audiocodec

import (
	"errors"
	"testing"
	"time"
)

func TestDefaultLimitsArePositiveAndIndependent(t *testing.T) {
	first := testLimits()
	second := testLimits()
	if err := first.Validate(); err != nil {
		t.Fatalf("testLimits().Validate() = %v", err)
	}
	first.MaxInputBytes = 1
	if second.MaxInputBytes == first.MaxInputBytes {
		t.Fatal("test limits unexpectedly share mutable state")
	}
}

func TestTypedErrorMatchesByKindAndUnwrapsCause(t *testing.T) {
	cause := errors.New("cause")
	err := &Error{Kind: ErrorDecode, Cause: cause, Detail: "fixture"}
	if !errors.Is(err, ErrDecode) {
		t.Fatalf("errors.Is(%v, ErrDecode) = false", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is(%v, cause) = false", err)
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != ErrorDecode || typed.Detail != "fixture" {
		t.Fatalf("errors.As() = %#v", typed)
	}
}

func TestErrorFormattingAndNonMatchingTargets(t *testing.T) {
	var nilError *Error
	if nilError.Error() != "<nil>" || nilError.Unwrap() != nil || nilError.Is(ErrDecode) {
		t.Fatal("nil Error methods did not remain inert")
	}
	err := &Error{Kind: ErrorDecode, Format: FormatWAV, Detail: "fixture", Cause: errors.New("cause")}
	if got := err.Error(); got != "audiocodec: decoder rejected input: fixture (format wav)" {
		t.Fatalf("Error() = %q", got)
	}
	if !err.Is(ErrDecode) || !err.Is(&Error{Kind: ErrorDecode}) || err.Is(ErrCanceled) || err.Is(errors.New("other")) {
		t.Fatal("Error.Is did not distinguish stable error identities")
	}
	if got := ErrDecode.Error(); got != "audiocodec: decoder rejected input" {
		t.Fatalf("ErrorCode.Error() = %q", got)
	}
}

func TestLimitsRejectInvalidBounds(t *testing.T) {
	limits := testLimits()
	limits.MaxInputBytes = 0
	if err := limits.Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid limits error = %v", err)
	}
	limits = testLimits()
	limits.MaxDuration = -time.Nanosecond
	if err := limits.Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("negative duration error = %v", err)
	}
}

func testLimits() Limits {
	return Limits{
		MaxInputBytes:  DefaultMaxInputBytes,
		MaxOutputBytes: DefaultMaxOutputBytes,
		MaxStderrBytes: DefaultMaxStderrBytes,
		MaxDuration:    2 * time.Minute,
	}
}
