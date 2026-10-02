package openaichatgpt

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
)

var (
	// ErrSignInAgain reports that the ChatGPT backend rejected the stored
	// sign-in (HTTP 401) even after a forced token refresh. It also matches
	// providers.ErrAuthentication. A 403 is not a sign-in failure: Codex
	// reads it as a Cloudflare block or a policy violation.
	ErrSignInAgain = errors.New("ChatGPT rejected the sign-in: run `yui auth chatgpt` again")
	// ErrPolicyViolation reports a request the backend refused on policy
	// grounds (misalignment_policy_violation, cyber_policy, bio_policy,
	// invalid_prompt). It also matches providers.ErrInvalidRequest.
	ErrPolicyViolation = errors.New("ChatGPT refused the request on policy grounds")
	// ErrServerOverloaded reports server_is_overloaded or slow_down, which
	// Codex surfaces as ServerOverloaded. It also matches
	// providers.ErrRateLimited, so a caller may retry later.
	ErrServerOverloaded = errors.New("ChatGPT Codex backend is overloaded")
	// ErrBlocked reports a 403 that is not a policy code, such as the
	// Cloudflare block Codex describes for restricted regions.
	ErrBlocked = errors.New("ChatGPT Codex backend refused access (HTTP 403); this usually means the network or region is blocked")
	// ErrUsageLimitReached reports that the plan's Codex allowance is used up
	// (`usage_limit_reached`). It also matches providers.ErrRateLimited.
	ErrUsageLimitReached = errors.New("ChatGPT plan usage limit reached")
	// ErrUsageNotIncluded reports a plan without Codex usage
	// (`usage_not_included`). It is not retryable.
	ErrUsageNotIncluded = errors.New("ChatGPT plan does not include Codex usage")
	// ErrNoModels reports an account whose model list is empty.
	ErrNoModels = errors.New("the ChatGPT account lists no Codex models; pass --model")
)

// Backend error codes with a typed mapping. Codex reads them from
// error.type (codex-rs/codex-api/src/api_bridge.rs) and OpenClaw from
// error.code or error.type (openai-chatgpt-responses.ts parseErrorResponse).
const (
	codeUsageLimitReached = "usage_limit_reached"
	codeUsageNotIncluded  = "usage_not_included"
	codeRateLimitExceeded = "rate_limit_exceeded"
	codeContextLength     = "context_length_exceeded"
	codeInsufficientQuota = "insufficient_quota"
	codeServerOverloaded  = "server_is_overloaded"
	codeSlowDown          = "slow_down"
	codeMisalignment      = "misalignment_policy_violation"
	codeCyberPolicy       = "cyber_policy"
	codeBioPolicy         = "bio_policy"
	codeInvalidPrompt     = "invalid_prompt"
)

// maxErrorBodyBytes bounds how much of a failed response is read to find its
// error code (OpenClaw reads at most 16 KiB).
const maxErrorBodyBytes = 16 << 10

// safeCode matches the short identifiers backends use as error codes. Only a
// code that matches is ever copied into an error; response text never is.
var safeCode = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,64}$`)

// apiError is the error object of a failed response, an `error` event or a
// `response.failed` event.
type apiError struct {
	Code string `json:"code"`
	Type string `json:"type"`
}

func (e *apiError) identifier() string {
	if e == nil {
		return ""
	}
	if e.Code != "" {
		return e.Code
	}
	return e.Type
}

// sanitizedCode returns code when it is a short identifier and "" otherwise.
func sanitizedCode(code string) string {
	if safeCode.MatchString(code) {
		return code
	}
	return ""
}

// readErrorCode reads at most maxErrorBodyBytes of a failed response and
// returns its error code. The body itself is discarded.
func readErrorCode(body io.Reader) string {
	payload, err := io.ReadAll(io.LimitReader(body, maxErrorBodyBytes))
	if err != nil {
		return ""
	}
	var envelope struct {
		Error *apiError `json:"error"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return ""
	}
	return sanitizedCode(envelope.Error.identifier())
}

// statusError classifies a non-2xx backend response from its status and
// error code. It never carries the response body or a token. Only a 401
// means "sign in again"; a 403 goes through the code mapping like any other
// status (Codex codex-api/src/api_bridge.rs maps a 403 to a policy violation
// or a Cloudflare block, never to re-authentication).
func statusError(status int, code string) error {
	if status == http.StatusUnauthorized {
		return &providers.ProviderError{
			Provider: ProviderName, StatusCode: status, Detail: ErrSignInAgain.Error(),
			Err: errors.Join(providers.ErrProviderRejected, providers.ErrAuthentication, ErrSignInAgain),
		}
	}
	if typed := codeError(status, code); typed != nil {
		return typed
	}
	if status == http.StatusForbidden {
		detail := ErrBlocked.Error()
		if code != "" {
			detail += " (" + code + ")"
		}
		return &providers.ProviderError{Provider: ProviderName, StatusCode: status, Detail: detail, Err: errors.Join(providers.ErrProviderRejected, ErrBlocked)}
	}
	return providers.NewProviderHTTPError(ProviderName, status, code)
}

// codeError maps the plan-allowance, overload, policy and request-size codes
// to typed errors. It returns nil for codes without a typed mapping.
func codeError(status int, code string) error {
	var typed, class error
	switch code {
	case codeUsageLimitReached:
		typed, class = ErrUsageLimitReached, providers.ErrRateLimited
	case codeUsageNotIncluded:
		typed, class = ErrUsageNotIncluded, providers.ErrProviderRejected
	case codeServerOverloaded, codeSlowDown:
		typed, class = ErrServerOverloaded, providers.ErrRateLimited
	case codeMisalignment, codeCyberPolicy, codeBioPolicy, codeInvalidPrompt:
		typed, class = ErrPolicyViolation, providers.ErrInvalidRequest
	case codeRateLimitExceeded, codeInsufficientQuota:
		return &providers.ProviderError{Provider: ProviderName, StatusCode: status, Detail: code, Err: errors.Join(providers.ErrProviderRejected, providers.ErrRateLimited)}
	case codeContextLength:
		return &providers.ProviderError{Provider: ProviderName, StatusCode: status, Detail: code, Err: errors.Join(providers.ErrProviderRejected, providers.ErrInvalidRequest)}
	default:
		return nil
	}
	return &providers.ProviderError{
		Provider: ProviderName, StatusCode: status, Detail: typed.Error() + " (" + code + ")",
		Err: errors.Join(providers.ErrProviderRejected, class, typed),
	}
}

// streamFailure classifies an `error` or `response.failed` event, which
// arrive after a 200 response, by its code alone.
func streamFailure(event string, failure *apiError) error {
	code := sanitizedCode(failure.identifier())
	if code != "" {
		if typed := codeError(0, code); typed != nil {
			return typed
		}
	}
	detail := event
	if code != "" {
		detail += ": " + code
	}
	return &providers.ProviderError{Provider: ProviderName, Detail: detail, Err: providers.ErrProviderRejected}
}

// credentialError classifies a failure to obtain the ChatGPT credential. A
// missing or expired sign-in is an authentication error whose text tells the
// user to run `yui auth chatgpt`; anything else (for example a refresh that
// could not reach the issuer) is a transport error.
func credentialError(err error) error {
	// The detail is the sentinel's fixed text, never the wrapped issuer reply.
	for _, sentinel := range []error{chatgptauth.ErrNotLoggedIn, chatgptauth.ErrReauthRequired, chatgptauth.ErrInsecureStore} {
		if errors.Is(err, sentinel) {
			return &providers.ProviderError{Provider: ProviderName, Detail: sentinel.Error(), Err: errors.Join(providers.ErrAuthentication, err)}
		}
	}
	if cancelled := gateway.CancellationErrorOrNil(ProviderName+": credential cancelled", err); cancelled != nil {
		return cancelled
	}
	var tokenErr *chatgptauth.TokenError
	if errors.As(err, &tokenErr) {
		// An issuer error may echo request text; keep only status and code.
		detail := fmt.Sprintf("ChatGPT token %s failed", tokenErr.Operation)
		if tokenErr.Status != 0 {
			detail += fmt.Sprintf(" (HTTP %d)", tokenErr.Status)
		}
		if code := sanitizedCode(tokenErr.Code); code != "" {
			detail += ": " + code
		}
		return &providers.ProviderError{Provider: ProviderName, Detail: detail, Err: errors.Join(providers.ErrTransport, tokenErr)}
	}
	return transportError("ChatGPT credential", err)
}

// requestError classifies an HTTP client failure before any response.
func requestError(operation string, err error) error {
	if cancelled := gateway.CancellationErrorOrNil(fmt.Sprintf("%s: %s cancelled", ProviderName, operation), err); cancelled != nil {
		return cancelled
	}
	return transportError(operation, err)
}

// transportError classifies a failure before or while reading a response as
// both the gateway's and the provider taxonomy's transport error, so stream
// classification reports "transport".
func transportError(operation string, err error) error {
	return &providers.ProviderError{
		Provider: ProviderName,
		Detail:   operation + " transport failed: " + err.Error(),
		Err:      errors.Join(providers.ErrTransport, gateway.NewTransportError(ProviderName, operation, err)),
	}
}
