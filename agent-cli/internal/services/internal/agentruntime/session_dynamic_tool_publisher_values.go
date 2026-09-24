package agentruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// SessionDynamicToolPublicationState is a diagnostic snapshot for one live
// session. Definitions are copied on the way in and out so callers cannot
// mutate the state used by the publisher.
type SessionDynamicToolPublicationState struct {
	StaticStableDefinitions     []messages.ToolDefinition
	LastSuccessfulDefinitions   []messages.ToolDefinition
	LastSuccessfulDigest        string
	LastSuccessfulBrowserID     webmcp.BrowserID
	LastSuccessfulTargetID      webmcp.TargetID
	LastSuccessfulGeneration    uint64
	LastSuccessfulEventSequence uint64
	LatestEventSequence         uint64
	Lifecycle                   SessionDynamicToolPublicationLifecycle
	Err                         error
	PublicationCount            uint64
}

// SessionDynamicToolPublicationError contains only bounded phase and event
// metadata in its public text while retaining the underlying cause for tests
// and programmatic classification.
type SessionDynamicToolPublicationError struct {
	Phase    string
	Sequence uint64
	Err      error
}

func (e *SessionDynamicToolPublicationError) Error() string {
	if e == nil {
		return ErrSessionDynamicToolPublication.Error()
	}
	message := strings.TrimSpace(e.ErrString())
	if message == "" {
		message = "unknown error"
	}
	return fmt.Sprintf("%s: phase=%s sequence=%d: %s", ErrSessionDynamicToolPublication, e.Phase, e.Sequence, message)
}

func (e *SessionDynamicToolPublicationError) ErrString() string {
	if e == nil || e.Err == nil {
		return ""
	}
	message := strings.TrimSpace(e.Err.Error())
	const maxPublicationErrorText = 256
	if len(message) > maxPublicationErrorText {
		return message[:maxPublicationErrorText] + "..."
	}
	return message
}

func (e *SessionDynamicToolPublicationError) Unwrap() error {
	if e == nil {
		return ErrSessionDynamicToolPublication
	}
	return errors.Join(ErrSessionDynamicToolPublication, e.Err)
}

func mergeSessionToolDefinitionBase(base, definitions []messages.ToolDefinition) []messages.ToolDefinition {
	canonicalBase := messages.CanonicalToolDefinitions(base)
	canonicalDefinitions := messages.CanonicalToolDefinitions(definitions)
	if len(canonicalBase) == 0 {
		return canonicalDefinitions
	}
	merged := append([]messages.ToolDefinition(nil), canonicalBase...)
	baseNames := make(map[string]struct{}, len(canonicalBase))
	for _, definition := range canonicalBase {
		baseNames[definition.Name] = struct{}{}
	}
	for _, definition := range canonicalDefinitions {
		if _, isBase := baseNames[definition.Name]; !isBase {
			merged = append(merged, definition)
		}
	}
	return messages.CanonicalToolDefinitions(merged)
}

func sessionToolDefinitionDigest(definitions []messages.ToolDefinition) (string, error) {
	canonical := messages.CanonicalToolDefinitions(definitions)
	payload := make([]sessionToolDefinitionDigestEntry, 0, len(canonical))
	for _, definition := range canonical {
		payload = append(payload, sessionToolDefinitionDigestEntry{
			Name: definition.Name, Description: definition.Description, Parameters: definition.Parameters,
			ParameterSchema: string(definition.ParameterSchema), ParametersClosed: definition.ParametersClosed,
		})
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

type sessionToolDefinitionDigestEntry struct {
	Name             string                   `json:"name"`
	Description      string                   `json:"description"`
	Parameters       []messages.ToolParameter `json:"parameters"`
	ParameterSchema  string                   `json:"parameter_schema,omitempty"`
	ParametersClosed bool                     `json:"parameters_closed"`
}

func isDynamicToolPublicationEvent(kind webmcp.BrokerEventType) bool {
	switch kind {
	case webmcp.BrokerEventSelected, webmcp.BrokerEventCatalogChanged, webmcp.BrokerEventGenerationChanged:
		return true
	case webmcp.BrokerEventInvocationCreated, webmcp.BrokerEventInvocationTerminal, webmcp.BrokerEventSessionClosed:
		return false
	default:
		return false
	}
}

func publicationEventFromBroker(event webmcp.BrokerEvent) sessionDynamicToolPublicationEvent {
	return sessionDynamicToolPublicationEvent{
		browserID: event.BrowserID, targetID: event.TargetID,
		generation: event.Generation, sequence: event.Sequence,
	}
}
