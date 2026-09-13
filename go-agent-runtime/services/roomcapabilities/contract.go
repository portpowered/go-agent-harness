// Package roomcapabilities owns provider-neutral participant tool policy.
//
// Hosts adapt their manifest, configuration, and browser protocol values to
// these contracts. Concrete validation, composition, and lifecycle state stay
// below the package's internal boundary and are assembled through wire.
package roomcapabilities

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// These aliases let an external consumer use the capability boundary without
// importing the agent-loop implementation package directly. They do not add
// a second tool contract.
type ToolCall = messages.ToolCall
type ToolCallResponse = messages.ToolCallResponse
type ToolDefinition = messages.ToolDefinition
type ToolParameter = messages.ToolParameter
type ToolExecutor = messages.ToolExecutor

type errorSentinel string

func (e errorSentinel) Error() string { return string(e) }

// Participant is the provider-neutral portion of one room participant used by
// capability admission. The host retains all other manifest fields.
type Participant struct {
	ID    string
	Tools []string
}

// ToolCapabilities pairs a participant's allowlisted definitions with the
// executor that owns them. A non-empty allowlist must be represented exactly by
// this pair.
type ToolCapabilities struct {
	Executor    ToolExecutor
	Definitions []ToolDefinition
}

// BrowserCapabilities is the host-neutral browser surface. Browser discovery,
// broker events, and protocol-specific values remain at the host edge.
type BrowserCapabilities struct {
	Executor               ToolExecutor
	Definitions            []ToolDefinition
	ToolDefinitionBase     []ToolDefinition
	RefreshToolDefinitions func(context.Context) ([]ToolDefinition, error)
	Initialize             func(context.Context) error
	Close                  func() error
	CloseTimeout           time.Duration
}

// Capability is the composed participant-local surface returned by Service.
// Definitions are independent snapshots. RefreshToolDefinitions preserves the
// static surface and returns a new composed snapshot on every successful call.
type Capability struct {
	Executor               ToolExecutor
	Definitions            []ToolDefinition
	ToolDefinitionBase     []ToolDefinition
	RefreshToolDefinitions func(context.Context) ([]ToolDefinition, error)
	Initialize             func(context.Context) error
	Close                  func() error
}

// Service owns validation and composition for one participant-local surface.
// Implementations are inert until Compose is called and hold no cross-
// participant mutable state.
type Service interface {
	Compose(context.Context, Participant, ToolCapabilities, *BrowserCapabilities) (Capability, error)
	ValidateTools(Participant, ToolCapabilities) error
	ValidateBrowser(BrowserCapabilities) error
	CloneDefinitions([]ToolDefinition) []ToolDefinition
	OrderDefinitions([]ToolDefinition, []string) []ToolDefinition
}

const (
	// ErrToolsUnavailable identifies requested static tools without an executor.
	ErrToolsUnavailable errorSentinel = "room participant tools are unavailable"
	// ErrToolMismatch identifies a static surface that does not match its
	// participant allowlist.
	ErrToolMismatch errorSentinel = "room participant tool capabilities do not match the manifest"
	// ErrDuplicateTool identifies repeated requested or returned tool names.
	ErrDuplicateTool errorSentinel = "room participant tool name is duplicated"
	// ErrMissingTool identifies an allowlisted name with no definition.
	ErrMissingTool errorSentinel = "room participant tool definition is missing"
	// ErrUnrequestedTool identifies a returned definition outside the allowlist.
	ErrUnrequestedTool errorSentinel = "room participant tool was not requested"
	// ErrEmptyToolName identifies an empty or whitespace-only definition/name.
	ErrEmptyToolName errorSentinel = "room participant tool name is empty"
	// ErrNilToolExecutor identifies a typed-nil or nil executor.
	ErrNilToolExecutor errorSentinel = "room participant tool executor is nil"

	// ErrBrowserToolsUnavailable identifies a browser surface without an
	// executor. A selected browser must always have a participant-local route.
	ErrBrowserToolsUnavailable errorSentinel = "room participant browser tools are unavailable"
	// ErrBrowserToolMismatch identifies malformed browser definitions.
	ErrBrowserToolMismatch errorSentinel = "room participant browser tool capabilities do not match the contract"
	// ErrDuplicateDefinition identifies repeated browser definition names.
	ErrDuplicateDefinition errorSentinel = "browser tool definition is duplicated"

	// ErrCompositionCollision identifies a name owned by both static and
	// browser surfaces.
	ErrCompositionCollision errorSentinel = "tool composition collision"
	// ErrCompositionInvalid identifies a surface that cannot form safe routes.
	ErrCompositionInvalid errorSentinel = "invalid tool composition"
	// ErrCapabilityClosePanic identifies a cleanup hook that panicked.
	ErrCapabilityClosePanic errorSentinel = "tool capability cleanup panicked"
	// ErrCapabilityCloseTimeout identifies a cleanup hook that exceeded its
	// request-scoped deadline.
	ErrCapabilityCloseTimeout errorSentinel = "tool capability cleanup timed out"
	ErrServiceUnavailable     errorSentinel = "room capability service is unavailable"
)

// Compatibility names make the old CLI adapter's error surface explicit while
// sharing the runtime-owned identities.
const (
	ErrRoomParticipantToolsUnavailable        = ErrToolsUnavailable
	ErrRoomParticipantToolMismatch            = ErrToolMismatch
	ErrRoomParticipantBrowserToolsUnavailable = ErrBrowserToolsUnavailable
	ErrRoomParticipantBrowserToolMismatch     = ErrBrowserToolMismatch
)

// DefaultCloseTimeout is the bounded cleanup budget used when a host does not
// provide a tighter browser-specific deadline.
const DefaultCloseTimeout = 15 * time.Second
