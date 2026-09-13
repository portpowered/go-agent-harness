package sessionconfig_test

import (
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionconfig"
)

func TestPublicTypedErrorsHandleNilAndEmptyCauses(t *testing.T) {
	var runtimeErr *sessionconfig.RuntimeSelectionError
	if runtimeErr.Error() != "invalid session runtime selection" || runtimeErr.Unwrap() != nil {
		t.Fatalf("nil RuntimeSelectionError = %q / %v, want stable nil behavior", runtimeErr.Error(), runtimeErr.Unwrap())
	}
	if got := (&sessionconfig.RuntimeSelectionError{}).Error(); got != "invalid session runtime selection: <nil>" {
		t.Fatalf("empty RuntimeSelectionError = %q, want explicit nil cause", got)
	}
	if got := (&sessionconfig.RuntimeSelectionError{Fields: []string{"transport"}}).Error(); got != "invalid session runtime selection (transport)" {
		t.Fatalf("field-only RuntimeSelectionError = %q, want deterministic field attribution", got)
	}
	if err := (&sessionconfig.RuntimeSelectionError{Err: errors.New("cause")}); err.Error() != "invalid session runtime selection: cause" || !errors.Is(err, sessionconfig.ErrInvalidSessionRuntimeSelection) {
		t.Fatalf("cause-only RuntimeSelectionError = %q, want stable sentinel chain", err.Error())
	}

	var voiceErr *sessionconfig.InvalidOpenAIRealtimeVoiceError
	if voiceErr.Error() != "invalid OpenAI Realtime voice" || voiceErr.Unwrap() != nil {
		t.Fatalf("nil voice error = %q / %v, want stable nil behavior", voiceErr.Error(), voiceErr.Unwrap())
	}
	if got := (&sessionconfig.InvalidOpenAIRealtimeVoiceError{Voice: "fable"}).Error(); got != `invalid OpenAI Realtime voice "fable"; supported voices: ` {
		t.Fatalf("empty voice registry error = %q, want literal formatting", got)
	}
}
