package bareadmission

import (
	"errors"
	"strings"
	"testing"
)

func TestContractErrorsPreserveTypedAndNilBehavior(t *testing.T) {
	var modelErr *UnsupportedRealtimeModelError
	if modelErr.Error() != "<nil>" || modelErr.Unwrap() != nil {
		t.Fatalf("nil model error = %q/%v, want nil-safe behavior", modelErr.Error(), modelErr.Unwrap())
	}
	modelErr = &UnsupportedRealtimeModelError{Provider: ProviderOpenAI, Model: "unknown", SupportedModels: []string{"known"}}
	if !errors.Is(modelErr, ErrUnsupportedRealtimeModel) || modelErr.Error() == "" {
		t.Fatalf("typed model error = %v, want stable identity and message", modelErr)
	}

	var credentialErr *CredentialError
	if credentialErr.Error() != ErrCredentialMissing.Error() {
		t.Fatalf("nil credential error = %q, want sentinel text", credentialErr.Error())
	}
	for _, testCase := range []struct {
		name     string
		error    *CredentialError
		contains string
	}{
		{name: "default provider", error: &CredentialError{ConfigPath: "config.yaml"}, contains: "OpenAI API key"},
		{name: "grok provider", error: &CredentialError{Provider: ProviderGrok, ConfigPath: "config.yaml"}, contains: "GROK"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if !errors.Is(testCase.error, ErrCredentialMissing) || !strings.Contains(testCase.error.Error(), testCase.contains) {
				t.Fatalf("credential error = %q, want identity and %q", testCase.error, testCase.contains)
			}
		})
	}

	var transportErr *InvalidTransportError
	if transportErr.Error() != ErrInvalidTransport.Error() || !errors.Is(transportErr, ErrInvalidTransport) {
		t.Fatalf("nil transport error = %q/%v, want nil-safe sentinel behavior", transportErr.Error(), transportErr.Unwrap())
	}
	transportErr = &InvalidTransportError{Transport: "tcp"}
	if !errors.Is(transportErr, ErrInvalidTransport) || !strings.Contains(transportErr.Error(), "tcp") {
		t.Fatalf("typed transport error = %v, want stable identity and transport", transportErr)
	}
}
