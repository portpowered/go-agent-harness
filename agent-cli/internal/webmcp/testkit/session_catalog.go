package testkit

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func (s *ScriptedTargetSession) ReleaseInvocation(id webmcp.InvocationID, output json.RawMessage) error {
	return s.EmitToolResponse(id, "Completed", output)
}

func (s *ScriptedTargetSession) ReleaseNextInvocation(output json.RawMessage) (webmcp.InvocationID, error) {
	s.mu.Lock()
	for _, id := range s.order {
		record := s.invokes[id]
		if record != nil && !record.Terminal {
			s.mu.Unlock()
			return id, s.ReleaseInvocation(id, output)
		}
	}
	s.mu.Unlock()
	return "", webmcp.ErrInvocationNotFound
}

// EmitToolResponse deliberately permits a response after cancellation or a
// previous response. The broker must treat that event as bounded late
// reconciliation rather than a second delivery.
func (s *ScriptedTargetSession) EmitToolResponse(id webmcp.InvocationID, status string, output json.RawMessage) error {
	s.mu.Lock()
	record := s.invokes[id]
	if record == nil {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s", webmcp.ErrInvocationNotFound, id)
	}
	record.Status = status
	generation := record.Generation
	if !record.Terminal || status == "Completed" {
		record.Output = cloneBytes(output)
		if status == "Completed" {
			record.State = webmcp.InvocationCompleted
		} else if status == "Canceled" || status == "Cancelled" {
			record.State = webmcp.InvocationCanceled
		}
		record.Terminal = true
	}
	s.notifyLocked()
	s.mu.Unlock()
	published, err := s.emitPublished(webmcp.BrowserEvent{Type: webmcp.EventToolResponded, InvocationID: id, Status: status, Output: output, Generation: generation})
	if err != nil {
		return err
	}
	s.markTerminalObserved(id, published)
	return nil
}

func (s *ScriptedTargetSession) WaitForInvocation(ctx context.Context) (InvocationRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		s.mu.Lock()
		for _, id := range s.order {
			if s.observed[id] {
				continue
			}
			record := cloneInvocationRecord(*s.invokes[id])
			s.observed[id] = true
			s.mu.Unlock()
			return record, nil
		}
		if s.closed {
			s.mu.Unlock()
			return InvocationRecord{}, webmcp.ErrClosed
		}
		changes := s.change
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return InvocationRecord{}, ctx.Err()
		case <-changes:
		}
	}
}

func (s *ScriptedTargetSession) Invocation(id webmcp.InvocationID) (InvocationRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.invokes[id]
	if !ok {
		return InvocationRecord{}, false
	}
	return cloneInvocationRecord(*record), true
}

func (s *ScriptedTargetSession) Invocations() []InvocationRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := make([]InvocationRecord, 0, len(s.order))
	for _, id := range s.order {
		records = append(records, cloneInvocationRecord(*s.invokes[id]))
	}
	return records
}

func (s *ScriptedTargetSession) PendingInvocations() []InvocationRecord {
	records := s.Invocations()
	pending := records[:0]
	for _, record := range records {
		if !record.Terminal {
			pending = append(pending, record)
		}
	}
	return pending
}

func (s *ScriptedTargetSession) Catalog() []webmcp.ToolDescriptor {
	s.mu.Lock()
	defer s.mu.Unlock()
	tools := make([]webmcp.ToolDescriptor, 0, len(s.tools))
	for _, tool := range s.tools {
		tools = append(tools, cloneTool(tool))
	}
	sort.Slice(tools, func(i, j int) bool {
		if tools[i].FrameID != tools[j].FrameID {
			return tools[i].FrameID < tools[j].FrameID
		}
		return tools[i].Name < tools[j].Name
	})
	return tools
}

func (s *ScriptedTargetSession) Emit(event webmcp.BrowserEvent) error {
	if event.Type == webmcp.EventToolsAdded {
		return s.emitToolsAdded(event, event.Tools...)
	}
	if event.Type == webmcp.EventToolsRemoved {
		return s.emitToolsRemoved(event, event.FrameID, event.RemovedToolNames...)
	}
	return s.emit(event)
}

func (s *ScriptedTargetSession) EmitToolsAdded(tools ...webmcp.ToolDescriptor) error {
	return s.emitToolsAdded(webmcp.BrowserEvent{Type: webmcp.EventToolsAdded}, tools...)
}

func (s *ScriptedTargetSession) emitToolsAdded(event webmcp.BrowserEvent, tools ...webmcp.ToolDescriptor) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return webmcp.ErrClosed
	}
	cloned := cloneTools(tools)
	for i := range cloned {
		if cloned[i].BrowserID == "" {
			cloned[i].BrowserID = s.target.BrowserID
		}
		if cloned[i].TargetID == "" {
			cloned[i].TargetID = s.target.ID
		}
		if cloned[i].Generation == 0 {
			cloned[i].Generation = event.Generation
			if cloned[i].Generation == 0 {
				cloned[i].Generation = s.context.Generation
			}
		}
		s.tools[toolKey(cloned[i].FrameID, cloned[i].Name)] = cloneTool(cloned[i])
	}
	s.mu.Unlock()
	event.Type = webmcp.EventToolsAdded
	event.Tools = cloned
	return s.emit(event)
}

func (s *ScriptedTargetSession) EmitToolsRemoved(frameID webmcp.FrameID, names ...string) error {
	return s.emitToolsRemoved(webmcp.BrowserEvent{Type: webmcp.EventToolsRemoved}, frameID, names...)
}

func (s *ScriptedTargetSession) emitToolsRemoved(event webmcp.BrowserEvent, frameID webmcp.FrameID, names ...string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return webmcp.ErrClosed
	}
	for _, name := range names {
		delete(s.tools, toolKey(frameID, name))
	}
	s.mu.Unlock()
	event.Type = webmcp.EventToolsRemoved
	event.FrameID = frameID
	event.RemovedToolNames = append([]string(nil), names...)
	return s.emit(event)
}

// Navigate advances the page generation and emits a lifecycle event. URL and
// origin are optional; an empty value leaves the previous value unchanged.
func (s *ScriptedTargetSession) Navigate(url, origin string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return webmcp.ErrClosed
	}
	previous := s.context.Generation
	if previous == ^uint64(0) {
		s.mu.Unlock()
		return ErrGenerationExhausted
	}
	s.context.Generation++
	if url != "" {
		s.context.URL = url
	}
	if origin != "" {
		s.context.Origin = origin
	}
	s.context.Ready = false
	s.tools = make(map[string]webmcp.ToolDescriptor)
	current := s.context.Generation
	s.target.URL = s.context.URL
	s.target.Origin = s.context.Origin
	s.target.Generation = current
	updatedTarget := cloneTarget(s.target)
	updatedContext := s.context
	err := s.emitLocked(webmcp.BrowserEvent{Type: webmcp.EventPageNavigated, PreviousGeneration: previous, Generation: current, Reason: "navigation"})
	s.mu.Unlock()
	s.handle.updateTarget(s, updatedTarget, updatedContext)
	return err
}

func (s *ScriptedTargetSession) EmitNavigation(url, origin string) error {
	return s.Navigate(url, origin)
}
