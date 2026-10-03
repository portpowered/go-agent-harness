package session_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

// Audited redacts the whole value before it bounds it: a secret that starts
// just before the cut is redacted, never kept as a fragment. A cut never
// splits a UTF-8 character, a short value is kept whole, and a nil redact
// only bounds.
func TestAuditedDelegationToolRedactsBeforeItBounds(t *testing.T) {
	const secret = "sk-straddle-0123456789"
	cut := session.LiveDelegationToolPayloadLimit - len(session.LiveDelegationToolTruncated)
	tool := session.LiveDelegationTool{
		Name:      "lookup",
		Arguments: `{"order":"42"}`,
		Result:    strings.Repeat("r", cut-4) + secret + strings.Repeat(" tail", 20),
	}
	audited := tool.Audited(func(value string) string { return strings.ReplaceAll(value, secret, "[X]") })
	if audited.Arguments != tool.Arguments || audited.Name != "lookup" {
		t.Fatalf("short values = %+v, want them whole", audited)
	}
	if strings.Contains(audited.Result, "sk-") || !strings.HasSuffix(audited.Result, session.LiveDelegationToolTruncated) || len(audited.Result) > session.LiveDelegationToolPayloadLimit {
		t.Fatalf("result = ...%q (%d bytes), want the secret redacted and the value bounded", audited.Result[len(audited.Result)-40:], len(audited.Result))
	}

	wide := session.LiveDelegationTool{Result: strings.Repeat("é", session.LiveDelegationToolPayloadLimit)}.Audited(nil)
	if !utf8.ValidString(wide.Result) || len(wide.Result) > session.LiveDelegationToolPayloadLimit {
		t.Fatalf("multi-byte result cut into %d bytes, valid UTF-8 %t", len(wide.Result), utf8.ValidString(wide.Result))
	}
}
