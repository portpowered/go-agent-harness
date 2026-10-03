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
