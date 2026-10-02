package hermetic

import "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"

// RedactEvents applies a canonical policy to a complete event stream.
func RedactEvents(events []Event, policy RedactionPolicy, credentials ...[]string) ([]Event, error) {
	redactor, err := NewRedactor(policy, credentials...)
	if err != nil {
		return nil, err
	}
	return redactor.RedactEvents(events)
}

// MarshalRedactedEvents redacts before canonical JSONL serialization.
func MarshalRedactedEvents(events []Event, policy RedactionPolicy, credentials ...[]string) ([]byte, error) {
	redactor, err := NewRedactor(policy, credentials...)
	if err != nil {
		return nil, err
	}
	return redactor.MarshalEvents(events)
}

// RecordingArtifact adapts a redacted browser artifact to the existing
// transcript bundle writer. The conversion keeps the transcript package as
// the sole owner of manifest.json while retaining the package's redaction
// boundary as the source of the artifact bytes and effective policy.
func (a RedactedBrowserArtifact) RecordingArtifact(path string) transcript.BrowserArtifact {
	if path == "" {
		path = transcript.BrowserArtifactDefaultPath
	}
	return transcript.BrowserArtifact{
		Format: a.Format,
		Path:   path,
		Data:   append([]byte(nil), a.Data...),
		SHA256: a.SHA256,
		Redaction: transcript.BrowserRedactionPolicy{
			URLQuery:           a.Redaction.URLQuery,
			URLFragment:        a.Redaction.URLFragment,
			ToolArguments:      append([]string(nil), a.Redaction.ToolArguments...),
			ResultJSONPointers: append([]string(nil), a.Redaction.ResultJSONPointers...),
			DigestTools:        append([]string(nil), a.Redaction.DigestTools...),
			RawCDP:             a.Redaction.RawCDP,
		},
	}
}

// EnsureNoConfiguredCredentials is a small manifest-boundary helper. It
// validates arbitrary serialized metadata without exposing the values in an
// error message.
func EnsureNoConfiguredCredentials(data []byte, credentials []string) error {
	secretBytes, err := newRedactionCredentials(credentials)
	if err != nil {
		return err
	}
	if containsCredential(data, secretBytes) {
		return newRedactionError(ErrRedactionCredentialSurvived, "validate metadata", "metadata", nil, secretBytes)
	}
	return nil
}

// RedactRawDiagnostics applies only credential byte replacement to an
// explicitly separate diagnostic blob. It deliberately does not return an
// Event and therefore cannot be used as strict semantic replay input. The
// canonical event APIs reject raw CDP fields and raw_cdp=true.
func RedactRawDiagnostics(data []byte, credentials []string) ([]byte, error) {
	secretBytes, err := newRedactionCredentials(credentials)
	if err != nil {
		return nil, err
	}
	redacted := redactBytes(string(data), secretBytes)
	if containsCredential(redacted, secretBytes) {
		return nil, newRedactionError(ErrRedactionCredentialSurvived, "redact diagnostics", "diagnostic", nil, secretBytes)
	}
	return redacted, nil
}
