package codexrtc_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc/fakecodex"
)

func TestCredentialNeverFormatsItsToken(t *testing.T) {
	credential := codexrtc.Credential{AccessToken: "secret-access-token", AccountID: "acct_1"}
	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("dial", "credential", credential)
	for _, out := range []string{
		fmt.Sprint(credential), fmt.Sprintf("%+v", credential), fmt.Sprintf("%#v", credential), credential.String(), logged.String(),
	} {
		if strings.Contains(out, "secret-access-token") || strings.Contains(out, "acct_1") || !strings.Contains(out, "[REDACTED]") {
			t.Fatalf("formatted credential %q", out)
		}
	}
	if empty := (codexrtc.Credential{}).String(); strings.Contains(empty, "[REDACTED]") {
		t.Fatalf("empty credential %q claims a token", empty)
	}
}

// The source is asked before every request, so a token refreshed between
// call creation and the sideband dial is the one the sideband sends.
func TestCredentialIsFetchedForEveryRequest(t *testing.T) {
	var fetches atomic.Int32
	tokens := []string{fakecodex.DefaultToken, "rotated-token"}
	rotating := codexrtc.CredentialFunc(func(context.Context) (codexrtc.Credential, error) {
		n := int(fetches.Add(1)) - 1
		return codexrtc.Credential{AccessToken: tokens[min(n, len(tokens)-1)], AccountID: fakecodex.DefaultAccountID}, nil
	})
	h := newHarness(t, rotating, fakecodex.WithAnswer(answerSDP))
	ctx := deadline(t)
	call := h.create(t, ctx, "v=offer\r\n")
	if got := h.backend.Calls()[0].Header.Get("Authorization"); got != "Bearer "+fakecodex.DefaultToken {
		t.Fatalf("call bearer %q", got)
	}
	// The fake requires the sideband to repeat the call's bearer, so the
	// rotated token is refused: proof that the sideband fetched it afresh.
	if _, err := h.client.DialSideband(ctx, call); err == nil {
		t.Fatal("sideband reused the call's token instead of fetching a fresh one")
	}
	if got := fetches.Load(); got != 2 {
		t.Fatalf("credential fetched %d times, want once per request", got)
	}
}
