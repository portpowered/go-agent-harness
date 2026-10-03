package transcript_test

import (
	"encoding/base64"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

// Each credential is listed raw, JSON-escaped, URL-query-escaped and in all
// four base64 encodings, once each, longest first; an empty credential adds
// nothing. Redaction replaces every listed form, so a credential that
// contains another is never left partly visible.
func TestCredentialFormsCoverEveryEncoding(t *testing.T) {
	const secret = "sk-<form>?~~~"
	forms := transcript.CredentialForms([]string{secret, "", "  ", secret})
	for _, want := range []string{
		secret, `sk-<form>?~~~`, url.QueryEscape(secret),
		base64.StdEncoding.EncodeToString([]byte(secret)), base64.RawStdEncoding.EncodeToString([]byte(secret)),
		base64.URLEncoding.EncodeToString([]byte(secret)), base64.RawURLEncoding.EncodeToString([]byte(secret)),
	} {
		if strings.Count(strings.Join(forms, "\n")+"\n", want+"\n") != 1 {
			t.Fatalf("forms = %q, want %q exactly once", forms, want)
		}
	}
	if !slices.IsSortedFunc(forms, func(a, b string) int { return len(b) - len(a) }) {
		t.Fatalf("forms = %q, want the longest first", forms)
	}
	nested := transcript.CredentialForms([]string{"sk-short", "sk-short-and-long"})
	if got := transcript.RedactCredentialForms("key sk-short-and-long and "+base64.StdEncoding.EncodeToString([]byte("sk-short")), nested, "X"); got != "key X and X" {
		t.Fatalf("redacted = %q, want every form replaced whole", got)
	}
}

// A credential inside a longer base64 stream (HTTP Basic credentials follow
// a user name, for example) is redacted at every byte alignment, in both
// alphabets, whatever follows it.
func TestCredentialFormsMatchBase64AtEveryAlignment(t *testing.T) {
	const secret = "sk-embedded-credential-42"
	forms := transcript.CredentialForms([]string{secret})
	for prefix := range 4 {
		for suffix := range 3 {
			raw := []byte(strings.Repeat("u", prefix) + secret + strings.Repeat(":", suffix))
			for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
				encoded := encoding.EncodeToString(raw)
				if redacted := transcript.RedactCredentialForms(encoded, forms, "X"); !strings.Contains(redacted, "X") {
					t.Fatalf("prefix %d suffix %d: %q was not redacted (forms %q)", prefix, suffix, encoded, forms)
				}
			}
		}
	}
}

// A dummy key such as "x", "none" or "ollama" is no secret: it lists no
// forms, so ordinary text and base64 audio ("eA" is the base64 of "x") are
// left as they are.
func TestShortDummyKeysAreNotRedacted(t *testing.T) {
	if forms := transcript.CredentialForms([]string{"x", "none", "ollama", "  ollama  "}); len(forms) != 0 {
		t.Fatalf("forms of dummy keys = %q, want none", forms)
	}
	if kept := transcript.RedactableCredentials([]string{"x", "ollama", "sk-real-key"}); len(kept) != 1 || kept[0] != "sk-real-key" {
		t.Fatalf("redactable credentials = %q, want only the real key", kept)
	}
	for _, form := range transcript.CredentialForms([]string{"sk-real-key"}) {
		if len(form) < transcript.MinRedactableCredentialLength {
			t.Fatalf("form %q is shorter than %d", form, transcript.MinRedactableCredentialLength)
		}
	}
}
