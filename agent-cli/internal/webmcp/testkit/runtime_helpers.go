package testkit

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

func boolPointer(value bool) *bool { return &value }

func cloneCandidate(candidate webmcp.BrowserCandidate) webmcp.BrowserCandidate {
	candidate.Diagnostics = append([]webmcp.Diagnostic(nil), candidate.Diagnostics...)
	return candidate
}

func cloneTarget(target webmcp.Target) webmcp.Target { return target }

func cloneTargetConfig(config TargetConfig) TargetConfig {
	config.Session.EnableEvents = cloneEvents(config.Session.EnableEvents)
	config.Session.InitialCatalog = cloneTools(config.Session.InitialCatalog)
	config.Session.AutoResponseOutput = cloneBytes(config.Session.AutoResponseOutput)
	return config
}

func cloneTool(tool webmcp.ToolDescriptor) webmcp.ToolDescriptor {
	tool.InputSchema = cloneBytes(tool.InputSchema)
	tool.Annotations.Raw = cloneBytes(tool.Annotations.Raw)
	return tool
}

func cloneTools(tools []webmcp.ToolDescriptor) []webmcp.ToolDescriptor {
	if tools == nil {
		return nil
	}
	cloned := make([]webmcp.ToolDescriptor, len(tools))
	for i, tool := range tools {
		cloned[i] = cloneTool(tool)
	}
	return cloned
}

func cloneEvents(events []webmcp.BrowserEvent) []webmcp.BrowserEvent {
	if events == nil {
		return nil
	}
	cloned := make([]webmcp.BrowserEvent, len(events))
	for i, event := range events {
		event.Tools = cloneTools(event.Tools)
		event.RemovedToolNames = append([]string(nil), event.RemovedToolNames...)
		event.Input = cloneBytes(event.Input)
		event.Output = cloneBytes(event.Output)
		cloned[i] = event
	}
	return cloned
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	return append([]byte(nil), value...)
}

func toolKey(frameID webmcp.FrameID, name string) string { return string(frameID) + "\x00" + name }

func cloneOperation(operation Operation) Operation {
	operation.Input = cloneBytes(operation.Input)
	operation.Arguments = cloneBytes(operation.Arguments)
	return operation
}

func cloneInvocationRecord(record InvocationRecord) InvocationRecord {
	record.Input = cloneBytes(record.Input)
	record.Output = cloneBytes(record.Output)
	return record
}

var (
	_ webmcp.BrowserRuntime = (*ScriptedBrowserRuntime)(nil)
	_ webmcp.BrowserHandle  = (*ScriptedBrowserHandle)(nil)
	_ webmcp.TargetSession  = (*ScriptedTargetSession)(nil)
)
