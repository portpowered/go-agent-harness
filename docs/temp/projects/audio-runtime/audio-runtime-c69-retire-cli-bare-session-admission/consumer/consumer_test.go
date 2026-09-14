package consumer_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/bareadmission"
	bareadmissionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/bareadmission/wire"
)

func catalog() *bareadmission.ModelCatalog {
	return &bareadmission.ModelCatalog{Models: []bareadmission.Model{
		{Provider: bareadmission.ProviderOpenAI, ID: bareadmission.DefaultOpenAIModel},
	}}
}

func TestPublicBareAdmissionConsumerResolvesExplicitValues(t *testing.T) {
	service := bareadmissionwire.NewService()
	request := bareadmission.Request{
		Provider: bareadmission.ProviderOpenAI, Model: bareadmission.DefaultOpenAIModel,
		APIKey: "consumer-key", BaseURL: "wss://consumer.example/realtime",
		Catalog: catalog(), Config: &bareadmission.ConfigSnapshot{Path: "consumer-config.yaml"},
		Device: bareadmission.DeviceSelection{InputDevice: "default", OutputDevice: "default"},
	}
	first, err := service.Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("public service Resolve() error = %v", err)
	}
	if first.Provider != bareadmission.ProviderOpenAI || first.Model != bareadmission.DefaultOpenAIModel || first.APIKey != "consumer-key" || first.BaseURL != request.BaseURL || first.TurnDetection == nil || first.TurnDetection.Type != "semantic_vad" || first.InputAudioTranscription == nil || !first.InputAudioTranscription.Enabled || !first.Device.InputPresent || !first.Device.OutputPresent {
		t.Fatalf("public result = %+v, want explicit values and defaults", first)
	}
	second, err := service.Resolve(context.Background(), request)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated public resolution = %+v/%v, want deterministic result", second, err)
	}
}

func TestPublicBareAdmissionConsumerPreservesTypedRedactedFailure(t *testing.T) {
	service := bareadmissionwire.NewService()
	request := bareadmission.Request{
		Provider: bareadmission.ProviderOpenAI, Model: bareadmission.DefaultOpenAIModel,
		Catalog: catalog(), Config: &bareadmission.ConfigSnapshot{Path: "consumer-config.yaml"},
	}
	_, err := service.Resolve(context.Background(), request)
	if err == nil {
		t.Fatal("missing credential Resolve() error = nil")
	}
	var credentialErr *bareadmission.CredentialError
	if !errors.As(err, &credentialErr) || !errors.Is(err, bareadmission.ErrCredentialMissing) {
		t.Fatalf("error = %T %v, want typed credential failure", err, err)
	}
	if strings.Contains(err.Error(), "sk-consumer-secret") || !strings.Contains(err.Error(), "consumer-config.yaml") {
		t.Fatalf("credential error = %q, want redacted config guidance", err)
	}
}

func TestPublicBareAdmissionConsumerRejectsCancellationAndCatalogMutation(t *testing.T) {
	service := bareadmissionwire.NewService()
	request := bareadmission.Request{
		Provider: bareadmission.ProviderOpenAI, Model: bareadmission.DefaultOpenAIModel,
		APIKey: "consumer-key", Catalog: catalog(), Config: &bareadmission.ConfigSnapshot{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Resolve(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Resolve() error = %v, want context.Canceled", err)
	}
	result, err := service.Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("post-cancel Resolve() error = %v", err)
	}
	request.Catalog.Models[0].ID = "mutated-after-call"
	if result.Model != bareadmission.DefaultOpenAIModel {
		t.Fatalf("result model = %q, want copied model identity", result.Model)
	}
}
