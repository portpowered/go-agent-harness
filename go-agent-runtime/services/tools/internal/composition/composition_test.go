package composition_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	composition "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/composition"
	display "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/display"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/sight"
)

func TestComposeToolSurfaceRejectsCollisionBeforeRouting(t *testing.T) {
	static := &compositionExecutor{}
	broker := &compositionExecutor{}

	surface, err := composition.ComposeToolSurface(
		static,
		[]messages.ToolDefinition{{Name: "shared"}},
		broker,
		[]messages.ToolDefinition{{Name: "shared"}},
	)
	if !errors.Is(err, composition.ErrToolCompositionCollision) {
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
	err := composition.ValidateToolDefinitionNamespaces(
		[]messages.ToolDefinition{{Name: "webmcp_get_context"}},
		[]messages.ToolDefinition{{Name: "webmcp_get_context"}},
	)
	if !errors.Is(err, composition.ErrToolCompositionCollision) {
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
	surface, err := composition.ComposeToolSurface(
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

func TestComposeToolSurfaceMakesPageSightAuthoritativeAndSplitsHostDisplay(t *testing.T) {
	static := &compositionExecutor{
		response: messages.ToolCallResponse{Content: "physical display"},
	}
	pageMetadata := `{"version":2,"status":"success","source":"browser_page","mime_type":"image/png","byte_length":4,"width":1,"height":1,"sha256":"` + strings.Repeat("0", 64) + `","typed_projection":"input_image"}`
	broker := &compositionExecutor{
		response: messages.ToolCallResponse{
			Content: pageMetadata,
			ContentParts: []messages.ContentPart{
				messages.TextPart{Text: pageMetadata},
				messages.ImagePart{Bytes: []byte{0x89, 'P', 'N', 'G'}, MediaType: "image/png"},
			},
		},
	}
	surface, err := composition.ComposeToolSurface(
		static,
		[]messages.ToolDefinition{{Name: display.ScreenToolID, Description: "legacy host capture"}},
		broker,
		[]messages.ToolDefinition{{Name: display.PageSightToolID, Description: "selected page capture"}},
	)
	if err != nil {
		t.Fatalf("ComposeToolSurface: %v", err)
	}
	assertPageSightDefinitions(t, surface.Definitions)
	assertLegacyPageRoute(t, surface.Executor, static, broker)
	assertLiteralPageRoute(t, surface.Executor, static, broker)
	assertHostDisplayRoute(t, surface.Executor, static, broker)
	assertPageSightClassification(t, surface.Executor)
}

func assertPageSightDefinitions(t *testing.T, definitions []messages.ToolDefinition) {
	t.Helper()
	if _, ok := findDefinition(definitions, display.ScreenToolID); ok {
		t.Fatalf("browser-composed definitions still advertise legacy physical name: %#v", definitions)
	}
	if host, ok := findDefinition(definitions, display.HostDisplayToolID); !ok || host.Description == "legacy host capture" {
		t.Fatalf("host display definition = %#v, want explicit renamed capability", host)
	}
}

func assertLegacyPageRoute(t *testing.T, executor messages.ToolExecutor, static, broker *compositionExecutor) {
	t.Helper()
	response, err := executor.Execute(context.Background(), messages.ToolCall{ID: "page-call", Name: display.ScreenToolID, Arguments: `{"action":"screenshot"}`})
	if err != nil {
		t.Fatalf("legacy page execute: %v", err)
	}
	if static.calls != 0 || broker.calls != 1 || broker.lastCall.Name != display.PageSightToolID || broker.lastCall.Arguments != `{}` {
		t.Fatalf("page route calls = static:%d broker:%d last=%#v, want broker show_page only", static.calls, broker.calls, broker.lastCall)
	}
	if response.ToolCallID != "page-call" || response.Name != display.ScreenToolID || len(response.ContentParts) != 2 {
		t.Fatalf("page response = %#v, want correlated rich response under legacy call name", response)
	}
	result, err := sight.Decode([]byte(response.Content))
	if err != nil || result.Source != sight.SourceBrowserPage {
		t.Fatalf("page result = %+v, err = %v, want browser_page source", result, err)
	}
}

func assertLiteralPageRoute(t *testing.T, executor messages.ToolExecutor, static, broker *compositionExecutor) {
	t.Helper()
	response, err := executor.Execute(context.Background(), messages.ToolCall{ID: "literal-page-call", Name: display.PageSightToolID, Arguments: `{}`})
	if err != nil {
		t.Fatalf("literal page execute: %v", err)
	}
	if broker.calls != 2 || static.calls != 0 || broker.lastCall.Name != display.PageSightToolID || len(response.ContentParts) != 2 {
		t.Fatalf("successive page routes = static:%d broker:%d last=%#v response=%#v, want two broker page captures and no host capture", static.calls, broker.calls, broker.lastCall, response)
	}
}

func assertHostDisplayRoute(t *testing.T, executor messages.ToolExecutor, static, broker *compositionExecutor) {
	t.Helper()
	response, err := executor.Execute(context.Background(), messages.ToolCall{ID: "host-call", Name: display.HostDisplayToolID, Arguments: `{}`})
	if err != nil {
		t.Fatalf("host display execute: %v", err)
	}
	if static.calls != 1 || static.lastCall.Name != display.ScreenToolID || broker.calls != 2 {
		t.Fatalf("host route calls = static:%d last=%#v broker:%d, want static show only", static.calls, static.lastCall, broker.calls)
	}
	if response.ToolCallID != "host-call" || response.Name != display.HostDisplayToolID || response.Content != "physical display" {
		t.Fatalf("host response = %#v", response)
	}
}

func assertPageSightClassification(t *testing.T, executor messages.ToolExecutor) {
	t.Helper()
	router, ok := executor.(composition.PageSightToolRouter)
	if !ok || !router.IsPageSightTool(display.ScreenToolID) || !router.IsPageSightTool(display.PageSightToolID) || router.IsPageSightTool(display.HostDisplayToolID) {
		t.Fatalf("page routing classification = ok:%v legacy:%v page:%v host:%v", ok, pageSight(router, display.ScreenToolID), pageSight(router, display.PageSightToolID), pageSight(router, display.HostDisplayToolID))
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
	surface, err := composition.ComposeToolSurface(
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
	_, err := composition.ComposeToolSurface(nil, []messages.ToolDefinition{{Name: "static_tool"}}, nil, nil)
	if !errors.Is(err, composition.ErrToolCompositionInvalid) {
		t.Fatalf("ComposeToolSurface error = %v, want invalid composition", err)
	}
}

func TestComposedExecutorBindsSessionImagePreparer(t *testing.T) {
	recorder := &imageBindingRecorder{}
	static := &imageBindingExecutor{recorder: recorder}
	broker := &compositionExecutor{}
	surface, err := composition.ComposeToolSurface(
		static,
		[]messages.ToolDefinition{{Name: runtimeTools.ReadImageToolID}},
		broker,
		[]messages.ToolDefinition{{Name: "browser_tool"}},
	)
	if err != nil {
		t.Fatalf("ComposeToolSurface: %v", err)
	}

	var preparedPaths []string
	binder, ok := surface.Executor.(runtimeTools.SessionImagePreparerBinder)
	if !ok {
		t.Fatal("composed executor does not expose session image preparation binding")
	}
	bound := binder.WithSessionImagePreparer(func(paths []string) ([]messages.ImagePart, error) {
		preparedPaths = append([]string(nil), paths...)
		return nil, nil
	})
	if _, err := bound.Execute(context.Background(), messages.ToolCall{ID: "image-call", Name: runtimeTools.ReadImageToolID}); err != nil {
		t.Fatalf("bound image execute: %v", err)
	}
	if recorder.calls != 1 || len(preparedPaths) != 1 || preparedPaths[0] != "fixture.png" {
		t.Fatalf("bound image preparation = calls:%d paths:%v, want one fixture callback", recorder.calls, preparedPaths)
	}
	if broker.calls != 0 {
		t.Fatalf("binding unexpectedly routed static image call to broker: %d", broker.calls)
	}
}

type compositionExecutor struct {
	response messages.ToolCallResponse
	calls    int
	lastCall messages.ToolCall
}

type imageBindingRecorder struct {
	calls int
}

type imageBindingExecutor struct {
	recorder *imageBindingRecorder
	preparer runtimeTools.ImagePartPreparer
}

func (e *imageBindingExecutor) Execute(_ context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
	e.recorder.calls++
	if e.preparer != nil {
		if _, err := e.preparer([]string{"fixture.png"}); err != nil {
			return messages.ToolCallResponse{}, err
		}
	}
	return messages.ToolCallResponse{}, nil
}

func (e *imageBindingExecutor) WithSessionImagePreparer(preparer runtimeTools.ImagePartPreparer) messages.ToolExecutor {
	if e == nil || e.recorder == nil {
		return nil
	}
	clone := *e
	clone.preparer = preparer
	return &clone
}

var _ runtimeTools.SessionImagePreparerBinder = (*imageBindingExecutor)(nil)

func findDefinition(definitions []messages.ToolDefinition, name string) (messages.ToolDefinition, bool) {
	for _, definition := range definitions {
		if definition.Name == name {
			return definition, true
		}
	}
	return messages.ToolDefinition{}, false
}

func pageSight(router composition.PageSightToolRouter, name string) bool {
	if router == nil {
		return false
	}
	return router.IsPageSightTool(name)
}

func (e *compositionExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.calls++
	e.lastCall = call
	return e.response, nil
}

var _ messages.ToolExecutor = (*compositionExecutor)(nil)
