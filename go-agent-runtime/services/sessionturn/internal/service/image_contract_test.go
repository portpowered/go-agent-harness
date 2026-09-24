package service

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

type imageTestCatalog struct{ model providers.RealtimeModel }

func (c imageTestCatalog) RealtimeModels(string) []providers.RealtimeModel {
	return []providers.RealtimeModel{c.model}
}
func (c imageTestCatalog) LookupRealtimeModel(provider, model string) (providers.RealtimeModel, bool) {
	return c.model, provider == "openai" && model == c.model.ID
}
func (c imageTestCatalog) SupportedRealtimeModelIDs(string) []string { return []string{c.model.ID} }

type imageStagingProbe struct {
	cleanup int
}

func (p *imageStagingProbe) Stage(_ context.Context, request tools.ImageStagingRequest) (tools.ImageStagingResult, error) {
	definitions := append([]messages.ToolDefinition(nil), request.ToolDefinitions...)
	definitions = append(definitions, messages.ToolDefinition{Name: "staged_image_lookup"})
	return tools.ImageStagingResult{
		ToolDefinitions: definitions,
		RefreshToolDefinitions: func(context.Context) ([]messages.ToolDefinition, error) {
			return append([]messages.ToolDefinition(nil), definitions...), nil
		},
		Cleanup: func() error { p.cleanup++; return nil },
	}, nil
}

type imageMessageProbe struct {
	message         messages.Message
	withResponse    bool
	withoutResponse bool
	accept          bool
}

func (p *imageMessageProbe) SendMessage(_ context.Context, message messages.Message) bool {
	p.message, p.withResponse = message, true
	return p.accept
}
func (p *imageMessageProbe) SendMessageWithoutResponse(_ context.Context, message messages.Message) bool {
	p.message, p.withoutResponse = message, true
	return p.accept
}
func (p *imageMessageProbe) Send(context.Context, messages.StreamMessage) bool    { return p.accept }
func (*imageMessageProbe) Receive() *messages.TypedBuffer[messages.StreamMessage] { return nil }
func (*imageMessageProbe) Done() <-chan struct{}                                  { return nil }
func (*imageMessageProbe) Close() error                                           { return nil }

func TestPublicImagePreparationValidatesStagesAndSendsPrivateParts(t *testing.T) {
	capabilities := assertPublicImageCapabilitySnapshot(t)
	imagePath, imageBytes := writePublicImageFixture(t)
	service, staging, prepared := preparePublicStagedImage(t, imagePath, capabilities)
	assertPublicImagePreparation(t, prepared, staging, imageBytes)
	assertPublicImageFailures(t, service, imagePath, capabilities)
	assertPublicImageSend(t, service, prepared, imageBytes)
}

func assertPublicImageCapabilitySnapshot(t *testing.T) sessionturn.ImageCapabilities {
	t.Helper()
	service := New(Dependencies{})
	request := sessionturn.ImageCapabilityRequest{
		Provider: "openai",
		ModelCatalog: imageTestCatalog{model: providers.RealtimeModel{
			ID: providers.OpenAIRealtimeDefaultModel, SupportsImageInput: true,
		}},
		ConfiguredModel: &sessionturn.ImageModelMetadata{SupportedInputMIMETypes: []string{"image/png"}},
	}
	capabilities, err := service.ResolveImageCapabilities(request)
	if err != nil || !capabilities.SupportsImageInput || capabilities.Model != providers.OpenAIRealtimeDefaultModel {
		t.Fatalf("default model image capabilities = %+v, %v", capabilities, err)
	}
	request.ConfiguredModel.SupportedInputMIMETypes[0] = "image/jpeg"
	if capabilities.SupportedInputMIMETypes[0] != "image/png" {
		t.Fatalf("capability snapshot aliases host MIME types: %+v", capabilities)
	}
	if _, err := service.ResolveImageCapabilities(sessionturn.ImageCapabilityRequest{Provider: "other", Model: "model"}); !errors.Is(err, sessionturn.ErrImageCapability) {
		t.Fatalf("unsupported provider error = %v, want image capability identity", err)
	}
	return capabilities
}

func writePublicImageFixture(t *testing.T) (string, []byte) {
	t.Helper()
	var data bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	if err := png.Encode(&data, img); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	path := filepath.Join(t.TempDir(), "input.png")
	if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
		t.Fatalf("write PNG: %v", err)
	}
	return path, data.Bytes()
}

func preparePublicStagedImage(t *testing.T, imagePath string, capabilities sessionturn.ImageCapabilities) (*Service, *imageStagingProbe, sessionturn.ImagePreparationResult) {
	t.Helper()
	stager := &imageStagingProbe{}
	service := New(Dependencies{ImageStaging: stager})
	prepared, err := service.PrepareImage(context.Background(), sessionturn.ImagePreparationRequest{
		SourcePaths:     []string{imagePath},
		Capabilities:    capabilities,
		StagingRoot:     t.TempDir(),
		ToolDefinitions: []messages.ToolDefinition{{Name: tools.ReadImageToolID}},
		RefreshToolDefinitions: func(context.Context) ([]messages.ToolDefinition, error) {
			return []messages.ToolDefinition{{Name: tools.ReadImageToolID}}, nil
		},
	})
	if err != nil {
		t.Fatalf("PrepareImage: %v", err)
	}
	return service, stager, prepared
}

func assertPublicImagePreparation(t *testing.T, prepared sessionturn.ImagePreparationResult, stager *imageStagingProbe, original []byte) {
	t.Helper()
	if len(prepared.Parts) != 1 || prepared.Parts[0].MediaType != "image/png" || !bytes.Equal(prepared.Parts[0].Bytes, original) {
		t.Fatalf("prepared image = %+v, want the validated PNG bytes", prepared.Parts)
	}
	if len(prepared.ToolDefinitions) != 2 || prepared.RefreshToolDefinitions == nil || prepared.Cleanup == nil {
		t.Fatalf("staged image contract = %+v, want staged tools and cleanup", prepared)
	}
	refreshed, err := prepared.RefreshToolDefinitions(context.Background())
	if err != nil || len(refreshed) != 2 {
		t.Fatalf("staged tool refresh = %v, %v; want both definitions", refreshed, err)
	}
	if err := prepared.Cleanup(); err != nil || stager.cleanup != 1 {
		t.Fatalf("staged cleanup = %v, calls=%d", err, stager.cleanup)
	}
}

func assertPublicImageFailures(t *testing.T, service *Service, imagePath string, capabilities sessionturn.ImageCapabilities) {
	t.Helper()
	if _, err := service.PrepareImageParts([]string{imagePath}, sessionturn.ImageCapabilities{Model: "text-only"}); !errors.Is(err, sessionturn.ErrImageCapability) {
		t.Fatalf("unsupported image preparation error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.PrepareImage(ctx, sessionturn.ImagePreparationRequest{Capabilities: capabilities}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled image preparation error = %v", err)
	}
}

func assertPublicImageSend(t *testing.T, service *Service, prepared sessionturn.ImagePreparationResult, original []byte) {
	t.Helper()
	target := &imageMessageProbe{accept: true}
	if err := service.SendImageTurn(context.Background(), target, "describe it", prepared.Parts, false); err != nil {
		t.Fatalf("SendImageTurn: %v", err)
	}
	if !target.withoutResponse || target.withResponse || target.message.Role != messages.RoleUser || len(target.message.ContentParts) != 2 {
		t.Fatalf("sent image message = %+v, without-response=%v", target.message, target.withoutResponse)
	}
	part, ok := target.message.ContentParts[1].(messages.ImagePart)
	if !ok || part.MediaType != "image/png" || !bytes.Equal(part.Bytes, original) {
		t.Fatalf("sent image content part = %#v", target.message.ContentParts[1])
	}
	prepared.Parts[0].Bytes[0] ^= 0xff
	if bytes.Equal(target.message.ContentParts[1].(messages.ImagePart).Bytes, prepared.Parts[0].Bytes) {
		t.Fatal("sent image message retained the mutable preparation buffer")
	}
	target.accept = false
	if err := service.SendImageTurn(context.Background(), target, "", prepared.Parts, true); !errors.Is(err, sessionturn.ErrImageSend) {
		t.Fatalf("rejected image send error = %v", err)
	}
}
