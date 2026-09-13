package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
)

func browserNeedsGeneration(kind browserEventType) bool {
	switch kind {
	case browserTargetAttached, browserGeneration, browserTargetDetached, browserTargetClosed:
		return false
	case browserCatalogAdded, browserCatalogRemoved, browserCatalogReady,
		browserInvocationMade, browserInvocationSent, browserInvocationDone,
		browserInvocationError, browserInvocationCancel:
		return true
	default:
		return false
	}
}

type browserEventBuilder struct {
	event  recording.BrowserEvent
	inputs []browserEventInput
}

func browserEventInputs(event recording.BrowserEvent, includeArguments, includeResults bool) ([]browserEventInput, error) {
	if !browserTargetSelected(event) {
		return nil, nil
	}
	builder := browserEventBuilder{event: event, inputs: make([]browserEventInput, 0, 2)}
	switch event.Type {
	case "target_attached":
		return builder.targetAttached()
	case "tools_added":
		return builder.toolsAdded()
	case "tools_removed":
		return builder.toolsRemoved()
	case "catalog_ready":
		return builder.catalogReady()
	case "tool_invoked":
		return builder.toolInvoked(includeArguments)
	case "tool_responded":
		return builder.toolResponded(includeResults)
	case "page_navigated", "frame_navigated":
		return builder.navigation()
	case "target_detached", "browser_disconnected":
		return builder.detached()
	case "session_closed":
		return builder.closed()
	default:
		return builder.inputs, nil
	}
}

func browserTargetSelected(event recording.BrowserEvent) bool {
	return strings.TrimSpace(event.BrowserID) != "" && strings.TrimSpace(event.TargetID) != ""
}

func (b *browserEventBuilder) add(kind browserEventType, generation uint64, generationSet bool, payload any) error {
	raw, err := browserJSONValue(payload)
	if err != nil {
		return err
	}
	b.inputs = append(b.inputs, browserEventInput{
		typeName: kind, browserID: b.event.BrowserID, targetID: b.event.TargetID,
		generation: generation, generationSet: generationSet, payload: raw, at: b.event.At,
	})
	return nil
}

func (b *browserEventBuilder) targetAttached() ([]browserEventInput, error) {
	err := b.add(browserTargetAttached, 0, false, map[string]any{"phase": "attached"})
	return b.inputs, err
}

func (b *browserEventBuilder) toolsAdded() ([]browserEventInput, error) {
	count := len(b.toolNames())
	if b.event.ToolCountKnown {
		count = b.event.ToolCount
	}
	err := b.add(browserCatalogAdded, b.event.Generation, true, map[string]any{
		"tools": b.toolNames(), "tool_count": count,
	})
	return b.inputs, err
}

func (b *browserEventBuilder) toolsRemoved() ([]browserEventInput, error) {
	err := b.add(browserCatalogRemoved, b.event.Generation, true, map[string]any{
		"tools": append([]string(nil), b.event.RemovedToolNames...),
	})
	return b.inputs, err
}

func (b *browserEventBuilder) catalogReady() ([]browserEventInput, error) {
	count := b.event.ToolCount
	if !b.event.ToolCountKnown {
		count = len(b.toolNames())
	}
	err := b.add(browserCatalogReady, b.event.Generation, true, map[string]any{
		"tool_count": count, "schema_digest": browserSchemaDigest(b.event.Tools),
	})
	return b.inputs, err
}

func (b *browserEventBuilder) toolNames() []string {
	names := make([]string, 0, len(b.event.Tools))
	for _, tool := range b.event.Tools {
		if strings.TrimSpace(tool.Name) != "" {
			names = append(names, tool.Name)
		}
	}
	return names
}

func (b *browserEventBuilder) toolInvoked(includeArguments bool) ([]browserEventInput, error) {
	if strings.TrimSpace(b.event.InvocationID) == "" {
		return b.inputs, nil
	}
	created := map[string]any{"invocation_id": b.event.InvocationID, "tool_name": b.event.ToolName}
	if b.event.FrameID != "" {
		created["frame_id"] = b.event.FrameID
	}
	if err := b.add(browserInvocationMade, b.event.Generation, true, created); err != nil {
		return nil, err
	}
	dispatched := map[string]any{"invocation_id": b.event.InvocationID}
	if includeArguments {
		dispatched["input"] = browserRawValue(b.event.Input)
	}
	if err := b.add(browserInvocationSent, b.event.Generation, true, dispatched); err != nil {
		return nil, err
	}
	return b.inputs, nil
}

func (b *browserEventBuilder) toolResponded(includeResults bool) ([]browserEventInput, error) {
	if strings.TrimSpace(b.event.InvocationID) == "" {
		return b.inputs, nil
	}
	status, reason := strings.ToLower(strings.TrimSpace(b.event.Status)), strings.TrimSpace(b.event.Reason)
	if reason == "" {
		reason = status
	}
	switch {
	case isBrowserCanceled(status):
		return b.canceled(reason)
	case b.event.ErrorCode != "" || status == "error" || status == "failed":
		return b.failed(includeResults, reason)
	default:
		return b.completed(includeResults)
	}
}

func isBrowserCanceled(status string) bool {
	switch status {
	case "canceled", "cancelled", "timed_out", "timeout", "timedout":
		return true
	default:
		return false
	}
}

func (b *browserEventBuilder) canceled(reason string) ([]browserEventInput, error) {
	err := b.add(browserInvocationCancel, b.event.Generation, true, map[string]any{
		"invocation_id": b.event.InvocationID, "source": "browser", "reason": reason,
	})
	return b.inputs, err
}

func (b *browserEventBuilder) failed(includeResults bool, reason string) ([]browserEventInput, error) {
	fields := map[string]any{"invocation_id": b.event.InvocationID, "code": browserErrorCode(b.event)}
	if includeResults && reason != "" {
		fields["message"] = reason
	}
	if includeResults && len(b.event.Output) > 0 {
		fields["error"] = browserRawValue(b.event.Output)
	}
	err := b.add(browserInvocationError, b.event.Generation, true, fields)
	return b.inputs, err
}

func (b *browserEventBuilder) completed(includeResults bool) ([]browserEventInput, error) {
	status := b.event.Status
	if status == "" {
		status = "completed"
	}
	fields := map[string]any{"invocation_id": b.event.InvocationID, "status": status}
	if includeResults {
		fields["output"] = browserRawValue(b.event.Output)
	}
	err := b.add(browserInvocationDone, b.event.Generation, true, fields)
	return b.inputs, err
}

func (b *browserEventBuilder) navigation() ([]browserEventInput, error) {
	reason := strings.TrimSpace(b.event.Reason)
	if reason == "" {
		reason = "navigation"
	}
	err := b.add(browserGeneration, 0, false, map[string]any{
		"previous_generation": b.event.PreviousGeneration,
		"current_generation":  b.event.Generation,
		"reason":              reason,
	})
	return b.inputs, err
}

func (b *browserEventBuilder) detached() ([]browserEventInput, error) {
	err := b.add(browserTargetDetached, 0, false, map[string]any{"reason": b.event.Reason})
	return b.inputs, err
}

func (b *browserEventBuilder) closed() ([]browserEventInput, error) {
	err := b.add(browserTargetClosed, 0, false, map[string]any{"reason": b.event.Reason})
	return b.inputs, err
}

func browserRawValue(raw json.RawMessage) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.RawMessage(append([]byte(nil), raw...))
}

func browserJSONValue(value any) (json.RawMessage, error) {
	if raw, ok := value.(json.RawMessage); ok {
		return compactJSON(raw)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return compactJSON(data)
}

func compactJSON(data []byte) (json.RawMessage, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return []byte("null"), nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("multiple JSON values")
	}
	return json.Marshal(value)
}

func browserSchemaDigest(tools []recording.BrowserTool) string {
	digest := sha256.New()
	for _, tool := range tools {
		digest.Write([]byte(tool.Name))
		digest.Write([]byte{0})
		digest.Write(bytes.TrimSpace(tool.InputSchema))
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func browserErrorCode(event recording.BrowserEvent) string {
	if code := strings.TrimSpace(event.ErrorCode); code != "" {
		return code
	}
	return "invocation_error"
}
