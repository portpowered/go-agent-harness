package tools

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

var (
	// ErrToolCompositionCollision identifies a name that is advertised by
	// both sides of a composed tool surface.
	ErrToolCompositionCollision = errors.New("tool composition collision")
	// ErrToolCompositionInvalid identifies a surface whose definitions and
	// executors cannot form a safe routing table.
	ErrToolCompositionInvalid = errors.New("invalid tool composition")
)

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
	if err := ValidateToolDefinitionNamespaces(staticDefinitions, brokerDefinitions); err != nil {
		return ToolSurface{}, err
	}
	staticNames, err := preflightToolNamespace("static", staticExecutor, staticDefinitions)
	if err != nil {
		return ToolSurface{}, err
	}
	brokerNames, err := preflightToolNamespace("broker", brokerExecutor, brokerDefinitions)
	if err != nil {
		return ToolSurface{}, err
	}

	var collisions []string
	for name := range staticNames {
		if _, exists := brokerNames[name]; exists {
			collisions = append(collisions, name)
		}
	}
	if len(collisions) > 0 {
		sort.Strings(collisions)
		return ToolSurface{}, fmt.Errorf("%w: tool %q is advertised by both static and broker surfaces", ErrToolCompositionCollision, collisions[0])
	}

	definitions := make([]messages.ToolDefinition, 0, len(staticDefinitions)+len(brokerDefinitions))
	definitions = append(definitions, cloneToolDefinitions(staticDefinitions)...)
	definitions = append(definitions, cloneToolDefinitions(brokerDefinitions)...)
	definitions = messages.CanonicalToolDefinitions(definitions)

	routes := make(map[string]toolRoute, len(definitions))
	for _, definition := range staticDefinitions {
		routes[definition.Name] = toolRoute{executor: staticExecutor}
	}
	for _, definition := range brokerDefinitions {
		routes[definition.Name] = toolRoute{executor: brokerExecutor, broker: true}
	}

	return ToolSurface{
		Executor:    &composedToolExecutor{routes: routes},
		Definitions: definitions,
	}, nil
}

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
	executor messages.ToolExecutor
	broker   bool
}

type composedToolExecutor struct {
	routes map[string]toolRoute
}

var _ messages.ToolExecutor = (*composedToolExecutor)(nil)

func (e *composedToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	response := messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name}
	if e == nil {
		return response, fmt.Errorf("%w: executor is nil", ErrToolCompositionInvalid)
	}
	route, ok := e.routes[call.Name]
	if !ok || isNilToolExecutor(route.executor) {
		return response, fmt.Errorf("%w: tool %q has no composed executor", ErrToolCompositionInvalid, call.Name)
	}

	response, err := route.executor.Execute(ctx, call)
	// The outer call is authoritative even if an injected executor returns
	// incomplete or conflicting metadata.
	response.ToolCallID = call.ID
	response.Name = call.Name
	if route.broker {
		response = textualBrokerResponse(response)
	}
	return response, err
}

func textualBrokerResponse(response messages.ToolCallResponse) messages.ToolCallResponse {
	if len(response.ContentParts) == 0 {
		return response
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
	default:
		return false
	}
}
