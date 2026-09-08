package composition

import (
	"context"
	"fmt"
	display "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/display"
	"reflect"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

var (
	// ErrToolCompositionCollision identifies a name that is advertised by
	// both sides of a composed tool surface.
	ErrToolCompositionCollision = public.ErrToolCompositionCollision
	// ErrToolCompositionInvalid identifies a surface whose definitions and
	// executors cannot form a safe routing table.
	ErrToolCompositionInvalid = public.ErrToolCompositionInvalid
)

const hostDisplayToolDescription = "Capture the host's physical display for an explicit host-display request. Use this only for the computer screen itself; use show_page for browser-page content."

// ToolSurface is the executor and definitions that must cross a session
// boundary together. Definitions are copied before they are returned so a
// caller cannot mutate the routing contract after preflight.
type ToolSurface struct {
	Executor    messages.ToolExecutor
	Definitions []messages.ToolDefinition
}

// ComposeToolSurface combines a static executor with an optional broker
// executor. The two definition namespaces are checked before an executor is
// returned, so an ambiguous name can never be advertised or silently routed
// to one owner.
//
// The broker side is marked as textual by the caller's position in this
// function. Broker responses have their rich ContentParts cleared after any
// text fallback is recovered; this keeps the stable WebMCP result on the
// ordinary textual ToolCallResponse path.
func ComposeToolSurface(
	staticExecutor messages.ToolExecutor,
	staticDefinitions []messages.ToolDefinition,
	brokerExecutor messages.ToolExecutor,
	brokerDefinitions []messages.ToolDefinition,
) (ToolSurface, error) {
	staticNames, brokerNames, err := preflightComposition(staticExecutor, staticDefinitions, brokerExecutor, brokerDefinitions)
	if err != nil {
		return ToolSurface{}, err
	}
	effectiveStaticDefinitions, pageSightComposition, err := prepareStaticDefinitions(staticDefinitions, brokerDefinitions, staticNames, brokerNames)
	if err != nil {
		return ToolSurface{}, err
	}
	definitions := composeDefinitions(effectiveStaticDefinitions, brokerDefinitions)
	routes := composeRoutes(staticExecutor, staticDefinitions, brokerExecutor, brokerDefinitions, pageSightComposition, len(definitions))

	composed := &composedToolExecutor{routes: routes}
	if router, ok := brokerExecutor.(DynamicToolRouter); ok && router.ResolvesDynamicTools() {
		composed.dynamicFallback = brokerExecutor
	}
	return ToolSurface{
		Executor:    composed,
		Definitions: definitions,
	}, nil
}

func preflightComposition(
	staticExecutor messages.ToolExecutor,
	staticDefinitions []messages.ToolDefinition,
	brokerExecutor messages.ToolExecutor,
	brokerDefinitions []messages.ToolDefinition,
) (map[string]struct{}, map[string]struct{}, error) {
	if err := ValidateToolDefinitionNamespaces(staticDefinitions, brokerDefinitions); err != nil {
		return nil, nil, err
	}
	staticNames, err := preflightToolNamespace("static", staticExecutor, staticDefinitions)
	if err != nil {
		return nil, nil, err
	}
	brokerNames, err := preflightToolNamespace("broker", brokerExecutor, brokerDefinitions)
	if err != nil {
		return nil, nil, err
	}
	var collisions []string
	for name := range staticNames {
		if _, exists := brokerNames[name]; exists {
			collisions = append(collisions, name)
		}
	}
	if len(collisions) == 0 {
		return staticNames, brokerNames, nil
	}
	sort.Strings(collisions)
	return nil, nil, fmt.Errorf("%w: tool %q is advertised by both static and broker surfaces", ErrToolCompositionCollision, collisions[0])
}

func prepareStaticDefinitions(
	staticDefinitions []messages.ToolDefinition,
	brokerDefinitions []messages.ToolDefinition,
	staticNames, brokerNames map[string]struct{},
) ([]messages.ToolDefinition, bool, error) {
	pageSightComposition := hasToolName(brokerNames, display.PageSightToolID)
	if pageSightComposition {
		if _, alreadyNamed := staticNames[display.HostDisplayToolID]; alreadyNamed {
			if _, hasLegacyScreen := staticNames[display.ScreenToolID]; hasLegacyScreen {
				return nil, false, fmt.Errorf("%w: browser sight needs the reserved physical display name %q", ErrToolCompositionInvalid, display.HostDisplayToolID)
			}
		}
	}
	effective := cloneToolDefinitions(staticDefinitions)
	if pageSightComposition {
		for index := range effective {
			if effective[index].Name == display.ScreenToolID {
				effective[index].Name = display.HostDisplayToolID
				effective[index].Description = hostDisplayToolDescription
			}
		}
	}
	if err := ValidateToolDefinitionNamespaces(effective, brokerDefinitions); err != nil {
		return nil, false, err
	}
	return effective, pageSightComposition, nil
}

func composeDefinitions(staticDefinitions, brokerDefinitions []messages.ToolDefinition) []messages.ToolDefinition {
	definitions := make([]messages.ToolDefinition, 0, len(staticDefinitions)+len(brokerDefinitions))
	definitions = append(definitions, staticDefinitions...)
	definitions = append(definitions, cloneToolDefinitions(brokerDefinitions)...)
	return messages.CanonicalToolDefinitions(definitions)
}

func composeRoutes(
	staticExecutor messages.ToolExecutor,
	staticDefinitions []messages.ToolDefinition,
	brokerExecutor messages.ToolExecutor,
	brokerDefinitions []messages.ToolDefinition,
	pageSightComposition bool,
	definitionCount int,
) map[string]toolRoute {
	routes := make(map[string]toolRoute, definitionCount+1)
	for _, definition := range staticDefinitions {
		name := definition.Name
		route := toolRoute{executor: staticExecutor}
		if pageSightComposition && name == display.ScreenToolID {
			name = display.HostDisplayToolID
			route.callName = display.ScreenToolID
		}
		routes[name] = route
	}
	for _, definition := range brokerDefinitions {
		routes[definition.Name] = toolRoute{
			executor:  brokerExecutor,
			broker:    true,
			pageSight: definition.Name == display.PageSightToolID,
		}
	}
	if pageSightComposition {
		routes[display.ScreenToolID] = toolRoute{
			executor:  brokerExecutor,
			broker:    true,
			callName:  display.PageSightToolID,
			pageSight: true,
		}
	}
	return routes
}

// DynamicToolRouter is an optional broker-executor extension. An executor
// that reports true routes tool names beyond its advertised definitions —
// for example first-class page tools resolved against a live browser catalog
// at call time — so the composed surface forwards unknown names to it
// instead of failing with an invalid-composition dead-end.
type DynamicToolRouter = public.DynamicToolRouter

// ValidateToolDefinitionNamespaces performs the side-effect-free portion of
// composition preflight. Callers that construct a broker lazily can use it to
// reject a static/broker collision before the broker factory is allowed to
// allocate or dial browser resources.
func ValidateToolDefinitionNamespaces(
	staticDefinitions []messages.ToolDefinition,
	brokerDefinitions []messages.ToolDefinition,
) error {
	staticNames, err := preflightDefinitionNames("static", staticDefinitions)
	if err != nil {
		return err
	}
	brokerNames, err := preflightDefinitionNames("broker", brokerDefinitions)
	if err != nil {
		return err
	}

	var collisions []string
	for name := range staticNames {
		if _, exists := brokerNames[name]; exists {
			collisions = append(collisions, name)
		}
	}
	if len(collisions) == 0 {
		return nil
	}
	sort.Strings(collisions)
	return fmt.Errorf("%w: tool %q is advertised by both static and broker surfaces", ErrToolCompositionCollision, collisions[0])
}

// ComposeExecutors is a descriptive alias for ComposeToolSurface.
func ComposeExecutors(
	staticExecutor messages.ToolExecutor,
	staticDefinitions []messages.ToolDefinition,
	brokerExecutor messages.ToolExecutor,
	brokerDefinitions []messages.ToolDefinition,
) (ToolSurface, error) {
	return ComposeToolSurface(staticExecutor, staticDefinitions, brokerExecutor, brokerDefinitions)
}

type toolRoute struct {
	executor  messages.ToolExecutor
	broker    bool
	callName  string
	pageSight bool
}

// PageSightToolRouter exposes the composition decision to the provider-
// neutral session adapter. In particular, it prevents a timeout on the
// compatibility "show" alias from triggering a host Screen Recording
// permission re-check when that call actually belongs to page sight.
type PageSightToolRouter = public.PageSightToolRouter

type composedToolExecutor struct {
	routes          map[string]toolRoute
	dynamicFallback messages.ToolExecutor
}

var _ messages.ToolExecutor = (*composedToolExecutor)(nil)
var _ PageSightToolRouter = (*composedToolExecutor)(nil)
var _ public.ScreenRecordingPermissionRechecker = (*composedToolExecutor)(nil)

func (e *composedToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	response := messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name}
	if e == nil {
		return response, fmt.Errorf("%w: executor is nil", ErrToolCompositionInvalid)
	}
	route, ok := e.routes[call.Name]
	if !ok || isNilToolExecutor(route.executor) {
		if isNilToolExecutor(e.dynamicFallback) {
			return response, fmt.Errorf("%w: tool %q has no composed executor", ErrToolCompositionInvalid, call.Name)
		}
		route = toolRoute{executor: e.dynamicFallback, broker: true}
	}

	innerCall := call
	if route.callName != "" {
		innerCall.Name = route.callName
	}
	if route.pageSight && call.Name == display.ScreenToolID && route.callName == display.PageSightToolID {
		// Older callers may still send the physical-screen action object with
		// the legacy name. The page screenshot contract is a closed empty
		// object, so discard those host-display-only arguments at the alias
		// boundary instead of turning compatibility into a page input error.
		innerCall.Arguments = `{}`
	}
	response, err := route.executor.Execute(ctx, innerCall)
	// The outer call is authoritative even if an injected executor returns
	// incomplete or conflicting metadata.
	response.ToolCallID = call.ID
	response.Name = call.Name
	if route.broker {
		response = textualBrokerResponse(response)
	}
	return response, err
}

func (e *composedToolExecutor) screenRecordingPermissionRechecker() (display.ScreenRecordingPermissionRechecker, bool) {
	if e == nil {
		return nil, false
	}
	for _, name := range []string{display.HostDisplayToolID, display.ScreenToolID} {
		route, ok := e.routes[name]
		if !ok || route.pageSight || isNilToolExecutor(route.executor) {
			continue
		}
		rechecker, ok := route.executor.(display.ScreenRecordingPermissionRechecker)
		if ok {
			return rechecker, true
		}
	}
	return nil, false
}

func (e *composedToolExecutor) IsPageSightTool(name string) bool {
	if e == nil {
		return false
	}
	route, ok := e.routes[name]
	return ok && route.pageSight
}

func (e *composedToolExecutor) ScreenRecordingPermissionRecheckSupported() bool {
	rechecker, ok := e.screenRecordingPermissionRechecker()
	return ok && rechecker.ScreenRecordingPermissionRecheckSupported()
}

func (e *composedToolExecutor) RecheckScreenRecordingPermission(ctx context.Context) (display.DisplayPermission, error) {
	rechecker, ok := e.screenRecordingPermissionRechecker()
	if !ok {
		return display.DisplayPermission{
			State:  display.DisplayPermissionUnavailable,
			Reason: "screen recording permission re-check is unavailable",
		}, nil
	}
	return rechecker.RecheckScreenRecordingPermission(ctx)
}

func textualBrokerResponse(response messages.ToolCallResponse) messages.ToolCallResponse {
	if len(response.ContentParts) == 0 {
		return response
	}
	for _, part := range response.ContentParts {
		if _, ok := part.(messages.ImagePart); ok {
			// A broker image result has already paired its compact metadata
			// envelope with the exact bytes it describes. Preserve that pair so
			// complete-message providers can project it as input_image.
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

func preflightToolNamespace(namespace string, executor messages.ToolExecutor, definitions []messages.ToolDefinition) (map[string]struct{}, error) {
	names, err := preflightDefinitionNames(namespace, definitions)
	if err != nil {
		return nil, err
	}
	if len(definitions) > 0 && isNilToolExecutor(executor) {
		return nil, fmt.Errorf("%w: %s surface advertises tools without an executor", ErrToolCompositionInvalid, namespace)
	}
	return names, nil
}

func hasToolName(names map[string]struct{}, name string) bool {
	_, ok := names[name]
	return ok
}

func preflightDefinitionNames(namespace string, definitions []messages.ToolDefinition) (map[string]struct{}, error) {
	names := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		name := definition.Name
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("%w: %s definition has an empty name", ErrToolCompositionInvalid, namespace)
		}
		if _, exists := names[name]; exists {
			return nil, fmt.Errorf("%w: %s surface repeats tool %q", ErrToolCompositionInvalid, namespace, name)
		}
		names[name] = struct{}{}
	}
	return names, nil
}

func cloneToolDefinitions(definitions []messages.ToolDefinition) []messages.ToolDefinition {
	return messages.CanonicalToolDefinitions(definitions)
}

func isNilToolExecutor(executor messages.ToolExecutor) bool {
	if executor == nil {
		return true
	}
	value := reflect.ValueOf(executor)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	case reflect.Invalid, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128, reflect.Array, reflect.String,
		reflect.Struct, reflect.UnsafePointer:
		return false
	default:
		return false
	}
}
