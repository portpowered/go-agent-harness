// Command sessionconfig-consumer exercises the public session configuration
// contract from a separate Go module with GOWORK=off.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	providerswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionconfig"
	sessionconfigwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionconfig/wire"
)

type report struct {
	Status            string `json:"status"`
	DefaultTransport  string `json:"default_transport,omitempty"`
	ExplicitTransport string `json:"explicit_transport,omitempty"`
	ExplicitSignaling string `json:"explicit_signaling,omitempty"`
	ExplicitMedia     string `json:"explicit_media,omitempty"`
	DefaultModel      string `json:"default_model,omitempty"`
}

type inertInferencer struct{}

func (*inertInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, errors.New("consumer replay validation must not connect")
}

func main() {
	mode := "valid"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	service := sessionconfigwire.NewService(sessionconfigwire.Dependencies{ModelCatalog: providerswire.NewModelCatalog()})
	switch mode {
	case "valid":
		valid(service)
	case "replay":
		if err := service.Validate(sessionconfig.Request{ReplayPath: "credential-free-replay.session.json", ReplayTiming: sessionconfig.ReplayTimingRecorded, SessionInferencer: &inertInferencer{}}); err != nil {
			fail(fmt.Errorf("credential-free replay admission failed: %w", err))
		}
		write(report{Status: "replay-ok"})
	case "invalid-model":
		invalidModel(service)
	case "invalid-transport":
		invalidTransport(service)
	default:
		fail(fmt.Errorf("unknown mode %q", mode))
	}
}

func valid(service sessionconfig.Service) {
	defaultSelection, err := service.ResolveRuntimeSelection(sessionconfig.Request{})
	if err != nil {
		fail(err)
	}
	defaultConfig, err := service.ResolveOpenAIRealtimeConfig(sessionconfig.Request{Defaults: sessionconfig.Defaults{Provider: "openai", OpenAI: &sessionconfig.ProviderConfig{APIKey: "synthetic-consumer-key"}}})
	if err != nil {
		fail(err)
	}
	explicitSelection, err := service.ResolveRuntimeSelection(sessionconfig.Request{Transport: " WebRTC ", SignalingEndpoint: "loopback://consumer", MediaSource: "fixture://consumer-media"})
	if err != nil {
		fail(err)
	}
	if defaultSelection.Transport != sessionconfig.TransportWebSocket || defaultConfig.Model != sessionconfig.OpenAIRealtimeDefaultModel {
		fail(fmt.Errorf("default literal oracle failed: selection=%#v config=%#v", defaultSelection, defaultConfig))
	}
	write(report{Status: "ok", DefaultTransport: defaultSelection.Transport, ExplicitTransport: explicitSelection.Transport, ExplicitSignaling: explicitSelection.SignalingEndpoint, ExplicitMedia: explicitSelection.MediaSource, DefaultModel: defaultConfig.Model})
}

func invalidModel(service sessionconfig.Service) {
	_, err := service.ResolveOpenAIRealtimeConfig(sessionconfig.Request{Model: "consumer-not-a-realtime-model", Defaults: sessionconfig.Defaults{Provider: "openai", OpenAI: &sessionconfig.ProviderConfig{APIKey: "synthetic-consumer-key"}}})
	var typed *providers.UnsupportedRealtimeModelError
	if err == nil || !errors.Is(err, providers.ErrUnsupportedRealtimeModel) || !errors.As(err, &typed) {
		fail(fmt.Errorf("invalid-model oracle failed: %v", err))
	}
	fail(fmt.Errorf("expected invalid-model rejection: %v", err))
}

func invalidTransport(service sessionconfig.Service) {
	_, err := service.ResolveRuntimeSelection(sessionconfig.Request{Transport: "quic"})
	var typed *sessionconfig.RuntimeSelectionError
	if err == nil || !errors.Is(err, sessionconfig.ErrInvalidSessionRuntimeSelection) || !errors.As(err, &typed) {
		fail(fmt.Errorf("invalid-transport oracle failed: %v", err))
	}
	fail(fmt.Errorf("expected invalid-transport rejection: %v", err))
}

func write(value report) {
	encoded, err := json.Marshal(value)
	if err != nil {
		fail(err)
	}
	fmt.Println(string(encoded))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
