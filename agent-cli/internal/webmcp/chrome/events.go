package chrome

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	cdpTarget "github.com/chromedp/cdproto/target"
	cdpWebMCP "github.com/chromedp/cdproto/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func (s *targetSession) convertProtocolEvent(event any) (webmcp.BrowserEvent, bool) {
	switch value := event.(type) {
	case *cdpWebMCP.EventToolsAdded:
		return s.convertToolsAdded(value), true
	case cdpWebMCP.EventToolsAdded:
		return s.convertToolsAdded(&value), true
	case *cdpWebMCP.EventToolsRemoved:
		return s.convertToolsRemoved(value), true
	case cdpWebMCP.EventToolsRemoved:
		return s.convertToolsRemoved(&value), true
	case *cdpWebMCP.EventToolInvoked:
		return s.convertToolInvoked(value), true
	case cdpWebMCP.EventToolInvoked:
		return s.convertToolInvoked(&value), true
	case *cdpWebMCP.EventToolResponded:
		return s.convertToolResponded(value), true
	case cdpWebMCP.EventToolResponded:
		return s.convertToolResponded(&value), true
	case *page.EventFrameNavigated:
		return s.convertFrameNavigated(value), true
	case page.EventFrameNavigated:
		return s.convertFrameNavigated(&value), true
	case *runtime.EventExecutionContextsCleared:
		return s.convertExecutionContextsCleared(), true
	case runtime.EventExecutionContextsCleared:
		return s.convertExecutionContextsCleared(), true
	case *cdpTarget.EventDetachedFromTarget:
		return s.convertDetached(value), true
	case cdpTarget.EventDetachedFromTarget:
		return s.convertDetached(&value), true
	case *cdpTarget.EventTargetDestroyed:
		return s.convertDestroyed(value), true
	case cdpTarget.EventTargetDestroyed:
		return s.convertDestroyed(&value), true
	case *cdpTarget.EventTargetCrashed:
		return s.convertCrashed(value), true
	case cdpTarget.EventTargetCrashed:
		return s.convertCrashed(&value), true
	default:
		return webmcp.BrowserEvent{}, false
	}
}

func (s *targetSession) convertToolsAdded(value *cdpWebMCP.EventToolsAdded) webmcp.BrowserEvent {
	event := webmcp.BrowserEvent{Type: webmcp.EventToolsAdded}
	if value == nil {
		return event
	}
	page := s.Context()
	event.Tools = make([]webmcp.ToolDescriptor, 0, len(value.Tools))
	for _, tool := range value.Tools {
		if converted, ok := s.convertToolAt(tool, page); ok {
			event.Tools = append(event.Tools, converted)
			if event.FrameID == "" {
				event.FrameID = converted.FrameID
			}
		}
	}
	return event
}

func (s *targetSession) convertToolsRemoved(value *cdpWebMCP.EventToolsRemoved) webmcp.BrowserEvent {
	event := webmcp.BrowserEvent{Type: webmcp.EventToolsRemoved}
	if value == nil {
		return event
	}
	for _, tool := range value.Tools {
		if tool == nil {
			continue
		}
		event.RemovedToolNames = append(event.RemovedToolNames, tool.Name)
		if event.FrameID == "" {
			event.FrameID = webmcp.FrameID(tool.FrameID)
		}
	}
	return event
}

func (s *targetSession) convertToolInvoked(value *cdpWebMCP.EventToolInvoked) webmcp.BrowserEvent {
	if value == nil {
		return webmcp.BrowserEvent{Type: webmcp.EventToolInvoked}
	}
	return webmcp.BrowserEvent{
		Type:         webmcp.EventToolInvoked,
		FrameID:      webmcp.FrameID(value.FrameID),
		ToolName:     value.ToolName,
		InvocationID: webmcp.InvocationID(value.InvocationID),
		Input:        cloneBytes([]byte(value.Input)),
	}
}

func (s *targetSession) convertToolResponded(value *cdpWebMCP.EventToolResponded) webmcp.BrowserEvent {
	if value == nil {
		return webmcp.BrowserEvent{Type: webmcp.EventToolResponded}
	}
	event := webmcp.BrowserEvent{
		Type:         webmcp.EventToolResponded,
		InvocationID: webmcp.InvocationID(value.InvocationID),
		Status:       value.Status.String(),
		Output:       cloneBytes([]byte(value.Output)),
	}
	if value.Exception != nil {
		event.ErrorCode = "page_exception"
	} else if strings.TrimSpace(value.ErrorText) != "" {
		event.ErrorCode = "page_error"
	}
	return event
}

func (s *targetSession) convertTool(value *cdpWebMCP.Tool) (webmcp.ToolDescriptor, bool) {
	return s.convertToolAt(value, s.Context())
}

func (s *targetSession) convertToolAt(value *cdpWebMCP.Tool, page webmcp.PageContext) (webmcp.ToolDescriptor, bool) {
	if value == nil || value.Name == "" || value.FrameID == "" {
		return webmcp.ToolDescriptor{}, false
	}
	descriptor := webmcp.ToolDescriptor{
		Name:        value.Name,
		Description: value.Description,
		InputSchema: cloneBytes([]byte(value.InputSchema)),
		BrowserID:   page.Key.BrowserID,
		TargetID:    page.Key.TargetID,
		FrameID:     webmcp.FrameID(value.FrameID),
		Origin:      page.Origin,
		Generation:  page.Generation,
	}
	if value.Annotations != nil {
		readOnly := value.Annotations.ReadOnly
		untrusted := value.Annotations.UntrustedContent
		autoSubmit := value.Annotations.Autosubmit
		descriptor.Annotations.ReadOnly = &readOnly
		descriptor.Annotations.UntrustedContent = &untrusted
		descriptor.Annotations.AutoSubmit = &autoSubmit
		if raw, err := json.Marshal(value.Annotations); err == nil {
			descriptor.Annotations.Raw = cloneBytes(raw)
		}
	}
	digest := sha256.Sum256(descriptor.InputSchema)
	descriptor.SchemaDigest = hex.EncodeToString(digest[:])
	return descriptor, true
}

func (s *targetSession) convertFrameNavigated(value *page.EventFrameNavigated) webmcp.BrowserEvent {
	if value == nil || value.Frame == nil {
		return webmcp.BrowserEvent{Type: webmcp.EventFrameNavigated}
	}
	topLevel := value.Frame.ParentID == ""
	s.mu.Lock()
	previous := s.page.Generation
	if topLevel {
		s.page.Generation++
		s.page.URL = value.Frame.URL
		s.page.Origin = targetOrigin(value.Frame.URL)
		s.page.Ready = false
	}
	current := s.page.Generation
	s.mu.Unlock()
	event := webmcp.BrowserEvent{
		Type:               webmcp.EventFrameNavigated,
		FrameID:            webmcp.FrameID(value.Frame.ID),
		PreviousGeneration: previous,
		Generation:         current,
	}
	if topLevel {
		event.Type = webmcp.EventPageNavigated
	}
	return event
}

func (s *targetSession) convertExecutionContextsCleared() webmcp.BrowserEvent {
	s.mu.Lock()
	previous := s.page.Generation
	s.page.Generation++
	s.page.Ready = false
	current := s.page.Generation
	s.mu.Unlock()
	return webmcp.BrowserEvent{
		Type:               webmcp.EventPageNavigated,
		PreviousGeneration: previous,
		Generation:         current,
		Reason:             "execution_contexts_cleared",
	}
}

func (s *targetSession) convertDetached(value *cdpTarget.EventDetachedFromTarget) webmcp.BrowserEvent {
	s.mu.Lock()
	matched := value != nil && value.SessionID == s.protocolSession
	if matched {
		s.page.Connected = false
	}
	s.mu.Unlock()
	if !matched {
		return webmcp.BrowserEvent{}
	}
	return webmcp.BrowserEvent{Type: webmcp.EventTargetDetached, Reason: "target_detached"}
}

func (s *targetSession) convertDestroyed(value *cdpTarget.EventTargetDestroyed) webmcp.BrowserEvent {
	s.mu.Lock()
	matched := value != nil && value.TargetID == s.protocolTargetID
	if matched {
		s.page.Connected = false
	}
	s.mu.Unlock()
	if !matched {
		return webmcp.BrowserEvent{}
	}
	return webmcp.BrowserEvent{Type: webmcp.EventTargetDetached, Reason: "target_destroyed"}
}

func (s *targetSession) convertCrashed(value *cdpTarget.EventTargetCrashed) webmcp.BrowserEvent {
	s.mu.Lock()
	matched := value != nil && value.TargetID == s.protocolTargetID
	if matched {
		s.page.Connected = false
	}
	s.mu.Unlock()
	if !matched {
		return webmcp.BrowserEvent{}
	}
	return webmcp.BrowserEvent{Type: webmcp.EventTargetDetached, Reason: "target_crashed"}
}
