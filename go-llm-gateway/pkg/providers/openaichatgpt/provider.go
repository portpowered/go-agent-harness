// Package openaichatgpt is the `openai-chatgpt` text provider: the OpenAI
// Responses API over the ChatGPT Codex backend
// (POST https://chatgpt.com/backend-api/codex/responses), authorized by a
// ChatGPT login (package chatgptauth) instead of a Platform API key.
//
// The request matches Codex CLI and OpenClaw: store false, stream true,
// separate instructions, include ["reasoning.encrypted_content"], function
// tools with strict false, and the headers Authorization, chatgpt-account-id,
// originator, OpenAI-Beta: responses=experimental, accept: text/event-stream,
// session-id and x-client-request-id. The Responses SSE stream is translated
// to the gateway's stream messages. With no configured model the provider
// uses the account's default Codex model from GET {base}/models.
//
// See docs/architecture/gpt-live-provider.md (PR 3a) and
// docs/architecture/chatgpt-oauth.md (sections 2 and 4.4).
package openaichatgpt

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/capabilities"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
)

const (
	// ProviderName is the provider name; it selects this wire protocol.
	ProviderName = "openai-chatgpt"
	// DefaultBaseURL is the ChatGPT Codex backend (Codex
	// CHATGPT_CODEX_BASE_URL, OpenClaw OPENAI_CODEX_RESPONSES_BASE_URL).
	DefaultBaseURL = "https://chatgpt.com/backend-api/codex"
	// DefaultOriginator names this client, following OpenClaw's precedent of
	// sending its own name rather than Codex's.
	DefaultOriginator = chatgptauth.DefaultOriginator
	// DefaultClientVersion is the model list's client_version. The backend
	// filters models by minimum client version; this is the Codex version
	// OpenClaw pins (extensions/openai/openai-provider.ts).
	DefaultClientVersion = "0.160.0"

	responsesPath = "/responses"
	modelsPath    = "/models"

	headerAccountID       = "Chatgpt-Account-Id"
	headerOriginator      = "Originator"
	headerOpenAIBeta      = "Openai-Beta"
	headerSessionID       = "Session-Id"
	headerClientRequestID = "X-Client-Request-Id"
	openAIBetaResponses   = "responses=experimental"
	contentTypeJSON       = "application/json"
	contentTypeSSE        = "text/event-stream"
	sessionIDBytes        = 16
)

// CredentialSource yields a ChatGPT credential whose access token is valid
// now. *chatgptauth.Manager implements it and refreshes as needed.
type CredentialSource interface {
	Credential(ctx context.Context) (chatgptauth.Credential, error)
}

// ForceRefresher is a CredentialSource that can replace an access token the
// backend rejected before its known expiry. *chatgptauth.Manager implements
// it. On a 401 the provider force-refreshes once and retries the request
// once, as Codex's UnauthorizedRecovery does (codex-rs/core/src/client.rs).
type ForceRefresher interface {
	ForceRefresh(ctx context.Context, rejected string) (chatgptauth.Credential, error)
}

// Provider is the `openai-chatgpt` text provider.
type Provider struct {
	credentials   CredentialSource
	httpClient    *http.Client
	baseURL       string
	model         string
	originator    string
	clientVersion string
	sessionID     string
	idleTimeout   time.Duration
	logger        logging.Logger
	replay        *reasoningReplay

	mu            sync.Mutex
	resolvedModel string
}

var _ providers.Provider = (*Provider)(nil)
var _ providers.CapabilityReporter = (*Provider)(nil)

// New returns a provider that signs requests with credentials.
func New(credentials CredentialSource, opts ...Option) *Provider {
	p := &Provider{
		credentials:   credentials,
		baseURL:       DefaultBaseURL,
		originator:    DefaultOriginator,
		clientVersion: DefaultClientVersion,
		idleTimeout:   DefaultStreamIdleTimeout,
		logger:        logging.DummyLogger(),
		replay:        newReasoningReplay(),
	}
	for _, opt := range opts {
		opt(p)
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")
	if p.sessionID == "" {
		p.sessionID = newSessionID()
	}
	return p
}

// Name returns ProviderName.
func (p *Provider) Name() string { return ProviderName }

// Capabilities reports what this provider translates without contacting the
// backend.
func (p *Provider) Capabilities() capabilities.ProviderCapabilities {
	return capabilities.ProviderCapabilities{
		Provider: p.Name(),
		Stateless: capabilities.StatelessCapabilities{
			Tools:                  capabilities.Supported("Responses function tools and function_call items are translated"),
			Streaming:              capabilities.Supported("the backend only streams; Responses SSE is translated"),
			ImageInput:             capabilities.Supported("image parts are sent as input_image"),
			AudioInput:             capabilities.Unsupported("the Codex backend takes no audio input"),
			AudioOutput:            capabilities.Unsupported("the Codex backend returns no audio"),
			VideoOutput:            capabilities.Unsupported("the Codex backend returns no video"),
			Reasoning:              capabilities.Supported("Config reasoning_effort sets reasoning.effort; reasoning summaries stream as REASONING"),
			PromptCaching:          capabilities.Unsupported("InferenceRequest CacheControl is not sent; prompt_cache_key is the session id"),
			ProviderSpecificConfig: capabilities.Supported(`InferenceRequest Config {"reasoning_effort": "..."} sets reasoning.effort`),
		},
		Session: capabilities.SessionCapabilities{
			Sessions: capabilities.Unsupported("openai-chatgpt is a text provider"),
		},
	}
}

// Infer runs one streamed request and assembles its result. The backend
// only streams, so this is InferStream collected.
func (p *Provider) Infer(ctx context.Context, req providers.InferenceRequest) (providers.InferenceResponse, error) {
	stream, err := p.InferStream(ctx, req)
	if err != nil {
		return providers.InferenceResponse{}, err
	}
	return collectStream(stream)
}

// InferStream sends req to the Responses endpoint and translates the SSE
// stream into gateway stream messages.
func (p *Provider) InferStream(ctx context.Context, req providers.InferenceRequest) (<-chan messages.StreamMessage, error) {
	cred, err := p.credential(ctx)
	if err != nil {
		return nil, err
	}
	model, cred, err := p.modelFor(ctx, req.Model, cred)
	if err != nil {
		return nil, err
	}
	body, prefix, err := marshalRequest(req, model, p.sessionID, p.replay)
	if err != nil {
		return nil, err
	}
	stream, err := p.send(ctx, "responses", cred, func(cred chatgptauth.Credential) (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+responsesPath, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("%s: create request: %w", ProviderName, err)
		}
		p.setHeaders(httpReq.Header, cred)
		httpReq.Header.Set("Content-Type", contentTypeJSON)
		httpReq.Header.Set("Accept", contentTypeSSE)
		httpReq.Header.Set(headerOpenAIBeta, openAIBetaResponses)
		httpReq.Header.Set(headerSessionID, p.sessionID)
		httpReq.Header.Set(headerClientRequestID, p.sessionID)
		return httpReq, nil
	})
	if err != nil {
		return nil, err
	}
	ch := make(chan messages.StreamMessage, providers.StreamMessageBuffer)
	go func() {
		defer close(ch)
		translateStream(stream, stream.Close, ch, p.idleTimeout, replayTarget{store: p.replay, prefix: prefix})
	}()
	return ch, nil
}

// send performs one backend request and returns the body of its 200
// response, which the caller must close. On a 401
// it force-refreshes the credential (when the source can) and retries once;
// any other non-200 status is classified by statusError.
func (p *Provider) send(ctx context.Context, operation string, cred chatgptauth.Credential, build func(chatgptauth.Credential) (*http.Request, error)) (io.ReadCloser, error) {
	for attempt := 0; ; attempt++ {
		req, err := build(cred)
		if err != nil {
			return nil, err
		}
		resp, err := p.client().Do(req)
		if err != nil {
			return nil, requestError(operation, err)
		}
		if resp.StatusCode == http.StatusOK {
			return resp.Body, nil
		}
		code := readErrorCode(resp.Body)
		closeBody(resp)
		p.logger.Error(ProviderName+": "+operation+" request failed",
			logging.Field{Key: "status_code", Value: resp.StatusCode}, logging.Field{Key: "code", Value: code})
		refresher, canRefresh := p.credentials.(ForceRefresher)
		if resp.StatusCode != http.StatusUnauthorized || attempt > 0 || !canRefresh {
			return nil, statusError(resp.StatusCode, code)
		}
		cred, err = refresher.ForceRefresh(ctx, cred.AccessToken)
		if err != nil {
			return nil, credentialError(err)
		}
	}
}

// credential fetches the current ChatGPT credential.
func (p *Provider) credential(ctx context.Context) (chatgptauth.Credential, error) {
	if p.credentials == nil {
		return chatgptauth.Credential{}, credentialError(chatgptauth.ErrNotLoggedIn)
	}
	cred, err := p.credentials.Credential(ctx)
	if err != nil {
		return chatgptauth.Credential{}, credentialError(err)
	}
	if cred.AccessToken == "" {
		return chatgptauth.Credential{}, credentialError(chatgptauth.ErrNotLoggedIn)
	}
	return cred, nil
}

// setHeaders sets the headers every backend request carries: the bearer
// token, the account id, the originator and the user agent.
func (p *Provider) setHeaders(header http.Header, cred chatgptauth.Credential) {
	header.Set("Authorization", "Bearer "+cred.AccessToken)
	if cred.AccountID != "" {
		header.Set(headerAccountID, cred.AccountID)
	}
	header.Set(headerOriginator, p.originator)
	header.Set("User-Agent", fmt.Sprintf("%s (%s; %s)", p.originator, runtime.GOOS, runtime.GOARCH))
}

func (p *Provider) client() *http.Client {
	if p.httpClient != nil {
		return p.httpClient
	}
	return http.DefaultClient
}

// modelFor picks the request model, then the configured model, then the
// account's default model (listed once and cached).
func (p *Provider) modelFor(ctx context.Context, requested string, cred chatgptauth.Credential) (string, chatgptauth.Credential, error) {
	if model := strings.TrimSpace(requested); model != "" {
		return model, cred, nil
	}
	if model := strings.TrimSpace(p.model); model != "" {
		return model, cred, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.resolvedModel != "" {
		return p.resolvedModel, cred, nil
	}
	list, cred, err := p.listModels(ctx, cred)
	if err != nil {
		return "", cred, err
	}
	model, ok := DefaultModel(list)
	if !ok {
		return "", cred, &providers.ProviderError{Provider: ProviderName, Detail: ErrNoModels.Error(), Err: errors.Join(providers.ErrUnsupportedRequest, ErrNoModels)}
	}
	p.resolvedModel = model
	return model, cred, nil
}

func closeBody(resp *http.Response) {
	if err := resp.Body.Close(); err != nil {
		return
	}
}

func newSessionID() string {
	var raw [sessionIDBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(raw[:])
}

// collectStream assembles a complete response from a translated stream.
func collectStream(stream <-chan messages.StreamMessage) (providers.InferenceResponse, error) {
	var text, refusal strings.Builder
	var toolCalls []models.ToolCall
	var usage models.TokenUsage
	var streamErr error
	for msg := range stream {
		switch value := msg.Value.(type) {
		case *messages.TextDeltaValue:
			text.WriteString(value.Content)
		case *messages.RefusalValue:
			refusal.WriteString(value.Message)
		case *messages.ToolCallEndValue:
			toolCalls = append(toolCalls, models.ToolCall{ID: value.ToolCallID, Name: value.Name, Arguments: value.Arguments})
		case *messages.MessageEndValue:
			usage = value.Usage
		case *messages.ErrorValue:
			if streamErr == nil {
				streamErr = streamValueError(value)
			}
		}
	}
	if streamErr != nil {
		return providers.InferenceResponse{}, streamErr
	}
	message := models.Message{Role: models.RoleAssistant, Refusal: refusal.String(), ToolCalls: toolCalls}
	if text.Len() > 0 {
		message.ContentParts = []models.ContentPart{models.TextPart{Text: text.String()}}
	}
	return providers.InferenceResponse{Message: message, Usage: usage}, nil
}

func streamValueError(value *messages.ErrorValue) error {
	if value.Err != nil {
		return value.Err
	}
	return errors.New(value.Message)
}
