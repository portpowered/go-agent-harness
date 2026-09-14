package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomcapabilities"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const hostDisplayToolDescription = "Capture the host's physical display for an explicit host-display request. Use this only for the computer screen itself; use show_page for browser-page content."

type surface struct {
	Executor    public.ToolExecutor
	Definitions []public.ToolDefinition
}

type route struct {
	executor  public.ToolExecutor
	broker    bool
	callName  string
	pageSight bool
}

func composeSurface(staticExecutor public.ToolExecutor, staticDefinitions []public.ToolDefinition, browserExecutor public.ToolExecutor, browserDefinitions []public.ToolDefinition) (surface, error) {
	if err := validateSurface("static", staticExecutor, staticDefinitions); err != nil {
		return surface{}, err
	}
	if err := validateSurface("browser", browserExecutor, browserDefinitions); err != nil {
		return surface{}, err
	}
	staticNames := names(staticDefinitions)
	browserNames := names(browserDefinitions)
	for name := range staticNames {
		if _, ok := browserNames[name]; ok {
			return surface{}, fmt.Errorf("%w: tool %q is advertised by both static and browser surfaces", public.ErrCompositionCollision, name)
		}
	}
	pageSight := hasName(browserNames, runtimeTools.PageSightToolID)
	if pageSight && hasName(staticNames, runtimeTools.HostDisplayToolID) && hasName(staticNames, runtimeTools.ScreenToolID) {
		return surface{}, fmt.Errorf("%w: browser sight needs the reserved physical display name %q", public.ErrCompositionInvalid, runtimeTools.HostDisplayToolID)
	}
	effectiveStatic := messages.CanonicalToolDefinitions(staticDefinitions)
	if pageSight {
		for index := range effectiveStatic {
			if effectiveStatic[index].Name == runtimeTools.ScreenToolID {
				effectiveStatic[index].Name = runtimeTools.HostDisplayToolID
				effectiveStatic[index].Description = hostDisplayToolDescription
			}
		}
	}
	definitions := make([]public.ToolDefinition, 0, len(effectiveStatic)+len(browserDefinitions))
	definitions = append(definitions, effectiveStatic...)
	definitions = append(definitions, messages.CanonicalToolDefinitions(browserDefinitions)...)
	definitions = messages.CanonicalToolDefinitions(definitions)
	composed := &composedExecutor{routes: composeRoutes(staticExecutor, staticDefinitions, browserExecutor, browserDefinitions, pageSight, len(definitions))}
	if dynamic, ok := browserExecutor.(runtimeTools.DynamicToolRouter); ok && dynamic.ResolvesDynamicTools() {
		composed.dynamicFallback = browserExecutor
	}
	return surface{Executor: composed, Definitions: definitions}, nil
}

func validateSurface(namespace string, executor public.ToolExecutor, definitions []public.ToolDefinition) error {
	if len(definitions) > 0 && isNilExecutor(executor) {
		return fmt.Errorf("%w: %s surface advertises tools without an executor", public.ErrCompositionInvalid, namespace)
	}
	if err := validateDefinitions(definitions, namespace); err != nil {
		return fmt.Errorf("%w: %w", public.ErrCompositionInvalid, err)
	}
	return nil
}

func names(definitions []public.ToolDefinition) map[string]struct{} {
	names := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		names[definition.Name] = struct{}{}
	}
	return names
}

func hasName(names map[string]struct{}, name string) bool {
	_, ok := names[name]
	return ok
}

func composeRoutes(staticExecutor public.ToolExecutor, staticDefinitions []public.ToolDefinition, browserExecutor public.ToolExecutor, browserDefinitions []public.ToolDefinition, pageSight bool, capacity int) map[string]route {
	routes := make(map[string]route, capacity+1)
	for _, definition := range staticDefinitions {
		name := definition.Name
		callName := ""
		if pageSight && name == runtimeTools.ScreenToolID {
			name = runtimeTools.HostDisplayToolID
			callName = runtimeTools.ScreenToolID
		}
		routes[name] = route{executor: staticExecutor, callName: callName}
	}
	for _, definition := range browserDefinitions {
		routes[definition.Name] = route{executor: browserExecutor, broker: true, pageSight: definition.Name == runtimeTools.PageSightToolID}
	}
	if pageSight {
		routes[runtimeTools.ScreenToolID] = route{executor: browserExecutor, broker: true, callName: runtimeTools.PageSightToolID, pageSight: true}
	}
	return routes
}

type composedExecutor struct {
	routes          map[string]route
	dynamicFallback public.ToolExecutor
}

var _ public.ToolExecutor = (*composedExecutor)(nil)
var _ runtimeTools.DynamicToolRouter = (*composedExecutor)(nil)
var _ runtimeTools.PageSightToolRouter = (*composedExecutor)(nil)
var _ runtimeTools.ScreenRecordingPermissionRechecker = (*composedExecutor)(nil)

func (e *composedExecutor) Execute(ctx context.Context, call public.ToolCall) (public.ToolCallResponse, error) {
	response := public.ToolCallResponse{ToolCallID: call.ID, Name: call.Name}
	if e == nil {
		return response, fmt.Errorf("%w: executor is nil", public.ErrCompositionInvalid)
	}
	selectedRoute, ok := e.routes[call.Name]
	if !ok || isNilExecutor(selectedRoute.executor) {
		if isNilExecutor(e.dynamicFallback) {
			return response, fmt.Errorf("%w: tool %q has no composed executor", public.ErrCompositionInvalid, call.Name)
		}
		selectedRoute = route{executor: e.dynamicFallback, broker: true}
	}
	innerCall := call
	if selectedRoute.callName != "" {
		innerCall.Name = selectedRoute.callName
	}
	if selectedRoute.pageSight && call.Name == runtimeTools.ScreenToolID && selectedRoute.callName == runtimeTools.PageSightToolID {
		innerCall.Arguments = `{}`
	}
	response, err := selectedRoute.executor.Execute(ctx, innerCall)
	response.ToolCallID = call.ID
	response.Name = call.Name
	if selectedRoute.broker {
		response = textualBrokerResponse(response)
	}
	return response, err
}

func (e *composedExecutor) ResolvesDynamicTools() bool {
	return e != nil && e.dynamicFallback != nil
}

func (e *composedExecutor) IsPageSightTool(name string) bool {
	if e == nil {
		return false
	}
	selectedRoute, ok := e.routes[name]
	return ok && selectedRoute.pageSight
}

func (e *composedExecutor) screenRecordingPermissionRechecker() (runtimeTools.ScreenRecordingPermissionRechecker, bool) {
	if e == nil {
		return nil, false
	}
	for _, name := range []string{runtimeTools.HostDisplayToolID, runtimeTools.ScreenToolID} {
		selectedRoute, ok := e.routes[name]
		if !ok || selectedRoute.pageSight || isNilExecutor(selectedRoute.executor) {
			continue
		}
		rechecker, ok := selectedRoute.executor.(runtimeTools.ScreenRecordingPermissionRechecker)
		if ok {
			return rechecker, true
		}
	}
	return nil, false
}

func (e *composedExecutor) ScreenRecordingPermissionRecheckSupported() bool {
	rechecker, ok := e.screenRecordingPermissionRechecker()
	return ok && rechecker.ScreenRecordingPermissionRecheckSupported()
}

func (e *composedExecutor) RecheckScreenRecordingPermission(ctx context.Context) (runtimeTools.DisplayPermission, error) {
	rechecker, ok := e.screenRecordingPermissionRechecker()
	if !ok {
		return runtimeTools.DisplayPermission{State: runtimeTools.DisplayPermissionUnavailable, Reason: "screen recording permission re-check is unavailable"}, nil
	}
	return rechecker.RecheckScreenRecordingPermission(ctx)
}

func textualBrokerResponse(response public.ToolCallResponse) public.ToolCallResponse {
	if len(response.ContentParts) == 0 {
		return response
	}
	for _, part := range response.ContentParts {
		if _, ok := part.(messages.ImagePart); ok {
			return response
		}
	}
	if response.Content == "" {
		var content strings.Builder
		for _, part := range response.ContentParts {
			if textPart, ok := part.(messages.TextPart); ok {
				content.WriteString(textPart.Text)
			}
		}
		response.Content = content.String()
	}
	response.ContentParts = nil
	return response
}
