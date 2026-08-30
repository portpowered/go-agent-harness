package tools_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestComposeToolSurfaceRejectsCollisionBeforeRouting(t *testing.T) {
	static := &compositionExecutor{}
	broker := &compositionExecutor{}

	surface, err := tools.ComposeToolSurface(
		static,
		[]messages.ToolDefinition{{Name: "shared"}},
		broker,
		[]messages.ToolDefinition{{Name: "shared"}},
	)
	if !errors.Is(err, tools.ErrToolCompositionCollision) {
		t.Fatalf("ComposeToolSurface error = %v, want collision", err)
	}
	if surface.Executor != nil || surface.Definitions != nil {
		t.Fatalf("collision returned a partial surface: %#v", surface)
	}
	if static.calls != 0 || broker.calls != 0 {
		t.Fatalf("collision performed execution: static=%d broker=%d", static.calls, broker.calls)
	}
}

func TestValidateToolDefinitionNamespacesIsSideEffectFree(t *testing.T) {
	err := tools.ValidateToolDefinitionNamespaces(
		[]messages.ToolDefinition{{Name: "webmcp_get_context"}},
		[]messages.ToolDefinition{{Name: "webmcp_get_context"}},
	)
	if !errors.Is(err, tools.ErrToolCompositionCollision) {
		t.Fatalf("ValidateToolDefinitionNamespaces error = %v, want collision", err)
	}
}

func TestComposeToolSurfaceRoutesExactNamesAndTextualizesBrokerResult(t *testing.T) {
	static := &compositionExecutor{
		response: messages.ToolCallResponse{Content: "static result"},
	}
	broker := &compositionExecutor{
		response: messages.ToolCallResponse{
			ContentParts: []messages.ContentPart{messages.TextPart{Text: `{"version":"webmcp.tool-result.v1"}`}},
		},
	}
	surface, err := tools.ComposeToolSurface(
		static,
		[]messages.ToolDefinition{{Name: "static_tool"}},
		broker,
		[]messages.ToolDefinition{{Name: "broker_tool"}},
	)
	if err != nil {
		t.Fatalf("ComposeToolSurface: %v", err)
	}
	if len(surface.Definitions) != 2 || surface.Definitions[0].Name != "broker_tool" || surface.Definitions[1].Name != "static_tool" {
		t.Fatalf("definitions = %#v, want canonical broker then static order", surface.Definitions)
	}

	staticResponse, err := surface.Executor.Execute(context.Background(), messages.ToolCall{ID: "static-call", Name: "static_tool"})
	if err != nil {
		t.Fatalf("static execute: %v", err)
	}
	if static.calls != 1 || static.lastCall.ID != "static-call" {
		t.Fatalf("static route = calls %d, call %#v", static.calls, static.lastCall)
	}
	if staticResponse.ToolCallID != "static-call" || staticResponse.Name != "static_tool" || staticResponse.Content != "static result" {
		t.Fatalf("static response = %#v", staticResponse)
	}

	brokerResponse, err := surface.Executor.Execute(context.Background(), messages.ToolCall{ID: "broker-call", Name: "broker_tool"})
	if err != nil {
		t.Fatalf("broker execute: %v", err)
	}
	if broker.calls != 1 || broker.lastCall.ID != "broker-call" {
		t.Fatalf("broker route = calls %d, call %#v", broker.calls, broker.lastCall)
	}
	if brokerResponse.ToolCallID != "broker-call" || brokerResponse.Name != "broker_tool" {
		t.Fatalf("broker correlation = %#v", brokerResponse)
	}
	if brokerResponse.Content != `{"version":"webmcp.tool-result.v1"}` || len(brokerResponse.ContentParts) != 0 {
		t.Fatalf("broker result = %#v, want one textual result", brokerResponse)
	}
}

func TestComposeToolSurfacePreservesBrokerImageProjection(t *testing.T) {
	imageBytes := []byte{0x89, 'P', 'N', 'G'}
	metadata := `{"version":2,"status":"success","source":"browser_page","mime_type":"image/png","byte_length":4,"width":1,"height":1,"sha256":"fixture","typed_projection":"input_image"}`
	broker := &compositionExecutor{
		response: messages.ToolCallResponse{
			Content: metadata,
			ContentParts: []messages.ContentPart{
				messages.TextPart{Text: metadata},
				messages.ImagePart{Bytes: imageBytes, MediaType: "image/png"},
			},
		},
	}
	surface, err := tools.ComposeToolSurface(
		&compositionExecutor{},
		[]messages.ToolDefinition{{Name: "static_tool"}},
		broker,
		[]messages.ToolDefinition{{Name: "broker_tool"}},
	)
	if err != nil {
		t.Fatalf("ComposeToolSurface: %v", err)
	}
	response, err := surface.Executor.Execute(context.Background(), messages.ToolCall{ID: "image-call", Name: "broker_tool"})
	if err != nil {
		t.Fatalf("broker execute: %v", err)
	}
	if response.Content != metadata || len(response.ContentParts) != 2 {
		t.Fatalf("broker image response = %#v, want metadata plus one image part", response)
	}
	part, ok := response.ContentParts[1].(messages.ImagePart)
	if !ok || !bytes.Equal(part.Bytes, imageBytes) || part.MediaType != "image/png" {
		t.Fatalf("broker image projection = %#v, want exact bytes", response.ContentParts[1])
	}
}

func TestComposeToolSurfaceRejectsAdvertisedToolsWithoutOwner(t *testing.T) {
	_, err := tools.ComposeToolSurface(nil, []messages.ToolDefinition{{Name: "static_tool"}}, nil, nil)
	if !errors.Is(err, tools.ErrToolCompositionInvalid) {
		t.Fatalf("ComposeToolSurface error = %v, want invalid composition", err)
	}
}

type compositionExecutor struct {
	response messages.ToolCallResponse
	calls    int
	lastCall messages.ToolCall
}

func (e *compositionExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.calls++
	e.lastCall = call
	return e.response, nil
}

var _ messages.ToolExecutor = (*compositionExecutor)(nil)
