package chatgptauth

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestFormattingNeverPrintsSecrets(t *testing.T) {
	const secret = "s3cr3t"
	cred := Credential{AccountID: "acct_1", Email: "user@example.com", IDToken: secret + "-id", AccessToken: secret + "-access", RefreshToken: secret + "-refresh", ExpiresAt: testEpoch()}
	server := &CallbackServer{state: secret + "-state", port: 1455}
	values := []any{
		cred, &cred,
		PKCE{Verifier: secret + "-verifier", Challenge: "challenge"},
		DeviceCode{UserCode: "ABCD-1234", deviceAuthID: secret + "-device"},
		server,
		callbackResult{code: secret + "-code", err: errors.New("e")},
		tokenResponse{IDToken: secret, AccessToken: secret, RefreshToken: secret},
		deviceUserCodeResponse{DeviceAuthID: secret, UserCode: "ABCD-1234"},
		deviceTokenResponse{AuthorizationCode: secret, CodeVerifier: secret},
		storedTokens{AccessToken: secret, RefreshToken: secret},
		storedCredential{Tokens: storedTokens{AccessToken: secret}},
	}
	for _, value := range values {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			if got := fmt.Sprintf(verb, value); strings.Contains(got, secret) {
				t.Errorf("Sprintf(%q, %T) leaked a secret: %s", verb, value, got)
			}
		}
	}
	got := fmt.Sprintf("%+v", cred)
	for _, want := range []string{"acct_1", "user@example.com", redactedMark, testEpoch().Format(time.RFC3339)} {
		if !strings.Contains(got, want) {
			t.Errorf("%%+v = %s, want it to keep %q", got, want)
		}
	}
	if got := fmt.Sprintf("%v", Credential{}); !strings.Contains(got, `AccessToken:""`) || !strings.Contains(got, "ExpiresAt:unknown") {
		t.Errorf("empty credential = %s, want empty tokens shown as empty", got)
	}
}
