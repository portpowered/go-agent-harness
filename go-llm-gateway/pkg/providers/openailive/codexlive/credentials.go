package codexlive

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc"
)

// ChatGPTCredentials adapts the ChatGPT token manager (`yui auth chatgpt`) to
// the route's credential source. Every call and sideband dial asks the
// manager, which refreshes a token that is about to expire; a sign-in that
// can no longer be refreshed fails with chatgptauth.ErrReauthRequired, which
// the session reports as a credential failure.
func ChatGPTCredentials(manager *chatgptauth.Manager) codexrtc.CredentialSource {
	return codexrtc.CredentialFunc(func(ctx context.Context) (codexrtc.Credential, error) {
		credential, err := manager.Credential(ctx)
		if err != nil {
			return codexrtc.Credential{}, err
		}
		return codexrtc.Credential{AccessToken: credential.AccessToken, AccountID: credential.AccountID}, nil
	})
}
