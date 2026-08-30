package testkit

import "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"

func cloneCandidate(candidate webmcp.BrowserCandidate) webmcp.BrowserCandidate {
	candidate.Diagnostics = append([]webmcp.Diagnostic(nil), candidate.Diagnostics...)
	return candidate
}

func cloneTarget(target webmcp.Target) webmcp.Target { return target }

func cloneTargetConfig(config TargetConfig) TargetConfig {
	config.Session.EnableEvents = cloneEvents(config.Session.EnableEvents)
	config.Session.InitialCatalog = cloneTools(config.Session.InitialCatalog)
	config.Session.PageScreenshot = clonePageScreenshot(config.Session.PageScreenshot)
	config.Session.AutoResponseOutput = cloneBytes(config.Session.AutoResponseOutput)
	return config
}

func clonePageScreenshot(screenshot webmcp.PageScreenshot) webmcp.PageScreenshot {
	screenshot.Bytes = cloneBytes(screenshot.Bytes)
	return screenshot
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

func cloneEvent(event webmcp.BrowserEvent) webmcp.BrowserEvent {
	event.Tools = cloneTools(event.Tools)
	event.RemovedToolNames = append([]string(nil), event.RemovedToolNames...)
	event.Input = cloneBytes(event.Input)
	event.Output = cloneBytes(event.Output)
	return event
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
