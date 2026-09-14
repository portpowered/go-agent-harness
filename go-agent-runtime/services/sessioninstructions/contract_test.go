package sessioninstructions

import (
	"errors"
	"testing"
)

func TestContractErrorIdentitiesAndResolutionCause(t *testing.T) {
	if got := ErrMalformedInstruction.Error(); got != "instruction value is malformed" {
		t.Fatalf("error identity = %q", got)
	}

	cause := errors.New("read failed")
	withPath := &ResolutionError{Phase: PhasePromptRead, Path: "/workspace/prompt.md", Err: cause}
	if got := withPath.Error(); got != "instruction resolution prompt-read /workspace/prompt.md: read failed" {
		t.Fatalf("path error = %q", got)
	}
	if !errors.Is(withPath, cause) || !errors.Is(withPath.Unwrap(), cause) {
		t.Fatalf("resolution cause was not preserved: %v", withPath)
	}

	withoutPath := &ResolutionError{Phase: PhaseValidation, Err: ErrLoaderRequired}
	if got := withoutPath.Error(); got != "instruction resolution validation: instruction loader is required" {
		t.Fatalf("validation error = %q", got)
	}
	if !errors.Is(withoutPath.Unwrap(), ErrLoaderRequired) {
		t.Fatalf("validation cause = %v", withoutPath.Unwrap())
	}

	var nilResolutionError *ResolutionError
	if got := nilResolutionError.Error(); got != "<nil>" {
		t.Fatalf("nil resolution error = %q", got)
	}
	if nilResolutionError.Unwrap() != nil {
		t.Fatalf("nil resolution cause = %v", nilResolutionError.Unwrap())
	}
}
