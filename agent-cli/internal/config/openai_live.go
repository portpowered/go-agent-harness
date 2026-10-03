package config

import (
	"errors"
	"fmt"
	"strings"

	runtimeProviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
)

// GPT-Live models of ProviderOpenAILive, by the credential they run on.
const (
	// OpenAILiveAPIKeyModel runs on an OpenAI API key.
	OpenAILiveAPIKeyModel = runtimeProviders.OpenAILive1Model
	// OpenAILiveChatGPTModel runs on the ChatGPT sign-in.
	OpenAILiveChatGPTModel = runtimeProviders.OpenAILiveCodexModel
)

// OpenAILiveCredential is what an openai-live session runs on: an API key,
// or the ChatGPT auth store at ChatGPTAuthPath.
type OpenAILiveCredential struct {
	Model           string
	APIKey          string
	ChatGPTAuthPath string
}

// ChatGPTSignedIn reports whether the ChatGPT auth store under configDir
// holds a sign-in. A store that exists but cannot be used (for example one
// readable by other users) counts as signed in, so the session reports why
// it cannot use it instead of falling back silently.
func ChatGPTSignedIn(configDir string) bool {
	if strings.TrimSpace(configDir) == "" {
		return false
	}
	_, err := chatgptauth.NewFileStore(ChatGPTAuthStorePath(configDir)).Load()
	return !errors.Is(err, chatgptauth.ErrNotLoggedIn)
}

// ChatGPTLoginSecrets returns the secret tokens (access, refresh and ID) of
// the ChatGPT sign-in under configDir, for evidence redaction; none without a
// usable sign-in.
func ChatGPTLoginSecrets(configDir string) []string {
	if strings.TrimSpace(configDir) == "" {
		return nil
	}
	credential, err := chatgptauth.NewFileStore(ChatGPTAuthStorePath(configDir)).Load()
	if err != nil {
		return nil
	}
	var secrets []string
	for _, token := range []string{credential.AccessToken, credential.RefreshToken, credential.IDToken} {
		if strings.TrimSpace(token) != "" {
			secrets = append(secrets, token)
		}
	}
	return secrets
}

// DefaultOpenAILiveModel is the model an openai-live session uses when none
// is given: the credential found picks it, the ChatGPT sign-in first.
func DefaultOpenAILiveModel(configDir string) string {
	if ChatGPTSignedIn(configDir) {
		return OpenAILiveChatGPTModel
	}
	return OpenAILiveAPIKeyModel
}

// ResolveOpenAILiveCredential narrows the credential order to what model
// accepts and fails fast, before any session is built, when it is missing:
// gpt-live-1-codex runs only on the ChatGPT sign-in, and every other model on
// an OpenAI API key.
func ResolveOpenAILiveCredential(model, apiKey, configDir string) (OpenAILiveCredential, error) {
	if model == OpenAILiveChatGPTModel {
		if !ChatGPTSignedIn(configDir) {
			return OpenAILiveCredential{}, fmt.Errorf("%s %s runs on a ChatGPT sign-in: run `yui auth chatgpt` (an OpenAI API key is not accepted for it)", ProviderOpenAILive, model)
		}
		return OpenAILiveCredential{Model: model, ChatGPTAuthPath: ChatGPTAuthStorePath(configDir)}, nil
	}
	if strings.TrimSpace(apiKey) == "" {
		alternative := fmt.Sprintf("to use a ChatGPT sign-in instead, run `yui auth chatgpt` and use --model %s", OpenAILiveChatGPTModel)
		if ChatGPTSignedIn(configDir) {
			alternative = fmt.Sprintf("your ChatGPT sign-in runs %s: pass --model %s or omit --model", OpenAILiveChatGPTModel, OpenAILiveChatGPTModel)
		}
		return OpenAILiveCredential{}, fmt.Errorf("%s %s requires an OpenAI API key (set AGENT_MODEL__OPENAI__API_KEY, pass --api-key, or configure model.openai.api_key in %s); %s", ProviderOpenAILive, model, ConfigFileName, alternative)
	}
	return OpenAILiveCredential{Model: model, APIKey: apiKey}, nil
}
