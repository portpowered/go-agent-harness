package discovery

import (
	"context"
	"strconv"
	"strings"
)

const maxLifecycleReason = 64

// HandleLifecycle applies one normalized page/target lifecycle observation.
// Navigation and document replacement advance only the exact browser/target
// pair named by the event. Target close/detach invalidates that pair and
// releases its external handle through the detach-only contract.
func (s *Service) HandleLifecycle(ctx context.Context, event LifecycleEvent) (Selection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Selection{}, err
	}
	event, failure := normalizeLifecycleEvent(event)
	if failure != nil {
		return Selection{}, failure
	}

	s.mu.Lock()
	if s.lifecycleSeen == nil {
		s.lifecycleSeen = make(map[string]struct{})
	}
	if key := lifecycleEventKey(event); key != "" {
		if _, seen := s.lifecycleSeen[key]; seen {
			selection := s.currentSelectionLocked()
			s.mu.Unlock()
			return selection, nil
		}
		s.lifecycleSeen[key] = struct{}{}
	}

	var (
		selection        Selection
		release          *TargetHandle
		lifecycleFailure *DiscoveryError
	)
	switch event.Type {
	case LifecycleNavigation, LifecycleDocumentReplaced:
		selection, lifecycleFailure = s.applyNavigationLocked(ctx, event)
	case LifecycleTargetClosed, LifecycleTargetDetached:
		selection, release, lifecycleFailure = s.applyTargetClosedLocked(event)
	default:
		lifecycleFailure = newProtocolInvalidAt("lifecycle", "unknown", "unsupported_lifecycle_event", nil)
	}
	s.mu.Unlock()

	// A browser adapter owns the target. Releasing a selection after the
	// adapter reported a close is still detach-only and idempotent; importantly,
	// this package never receives a close-target or browser-process operation.
	if release != nil {
		_ = release.Close()
	}
	if lifecycleFailure != nil {
		return selection, lifecycleFailure
	}
	return selection, nil
}

// ApplyLifecycleEvent is a descriptive alias for HandleLifecycle.
func (s *Service) ApplyLifecycleEvent(ctx context.Context, event LifecycleEvent) (Selection, error) {
	return s.HandleLifecycle(ctx, event)
}

// HandlePageLifecycle is a descriptive alias for HandleLifecycle.
func (s *Service) HandlePageLifecycle(ctx context.Context, event PageLifecycleEvent) (Selection, error) {
	return s.HandleLifecycle(ctx, event)
}

// HandleNavigation advances the selected page generation for a navigation
// event. The optional reason is bounded diagnostic metadata only.
func (s *Service) HandleNavigation(ctx context.Context, browserID, targetID string, reason ...string) (Selection, error) {
	return s.HandleLifecycle(ctx, LifecycleEvent{
		Type:      LifecycleNavigation,
		BrowserID: browserID,
		TargetID:  targetID,
		Reason:    firstLifecycleReason(reason),
	})
}

// OnNavigation is a concise adapter-facing alias for HandleNavigation.
func (s *Service) OnNavigation(ctx context.Context, browserID, targetID string, reason ...string) (Selection, error) {
	return s.HandleNavigation(ctx, browserID, targetID, reason...)
}

// HandlePageNavigation is a descriptive alias for HandleNavigation.
func (s *Service) HandlePageNavigation(ctx context.Context, browserID, targetID string, reason ...string) (Selection, error) {
	return s.HandleNavigation(ctx, browserID, targetID, reason...)
}

// HandleDocumentReplacement applies a document-replacement lifecycle event.
func (s *Service) HandleDocumentReplacement(ctx context.Context, browserID, targetID string, reason ...string) (Selection, error) {
	return s.HandleLifecycle(ctx, LifecycleEvent{
		Type:      LifecycleDocumentReplaced,
		BrowserID: browserID,
		TargetID:  targetID,
		Reason:    firstLifecycleReason(reason),
	})
}

// AdvanceGeneration is the explicit state-machine form of HandleNavigation.
func (s *Service) AdvanceGeneration(ctx context.Context, browserID, targetID string, reason ...string) (Selection, error) {
	return s.HandleNavigation(ctx, browserID, targetID, reason...)
}

// HandleTargetClosed invalidates one exact target and clears it if it is the
// active selection. The target's last normalized generation is retained as a
// tombstone so old callers receive stale_selection rather than a fallback.
func (s *Service) HandleTargetClosed(ctx context.Context, browserID, targetID string, reason ...string) (Selection, error) {
	return s.HandleLifecycle(ctx, LifecycleEvent{
		Type:      LifecycleTargetClosed,
		BrowserID: browserID,
		TargetID:  targetID,
		Reason:    firstLifecycleReason(reason),
	})
}

// OnTargetClosed is a concise adapter-facing alias for HandleTargetClosed.
func (s *Service) OnTargetClosed(ctx context.Context, browserID, targetID string, reason ...string) (Selection, error) {
	return s.HandleTargetClosed(ctx, browserID, targetID, reason...)
}

// HandleTargetDetached is the equivalent close-path alias for adapters whose
// lifecycle vocabulary reports detach rather than close.
func (s *Service) HandleTargetDetached(ctx context.Context, browserID, targetID string, reason ...string) (Selection, error) {
	return s.HandleLifecycle(ctx, LifecycleEvent{
		Type:      LifecycleTargetDetached,
		BrowserID: browserID,
		TargetID:  targetID,
		Reason:    firstLifecycleReason(reason),
	})
}

// RefreshSelection revalidates the current exact target without selecting a
// different browser or tab. It is useful after a lifecycle notification when
// the adapter exposes capability state through a target-list or probe seam.
// A target that no longer proves WebMCP is left non-ready and returns
// unsupported_webmcp.
func (s *Service) RefreshSelection(ctx context.Context) (Selection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Selection{}, err
	}
	s.mu.Lock()
	if s.selection == nil {
		s.mu.Unlock()
		return Selection{}, newNoEligibleTab("", TargetListOptions{}, 0)
	}
	selection, failure := s.refreshSelectionLocked(ctx, LifecycleEvent{})
	s.mu.Unlock()
	if failure != nil {
		return selection, failure
	}
	return selection, nil
}

// RefreshSelected is a descriptive alias for RefreshSelection.
func (s *Service) RefreshSelected(ctx context.Context) (Selection, error) {
	return s.RefreshSelection(ctx)
}

// ValidateSelection checks that an operation still refers to the exact
// browser, target, and generation it was given. It never refreshes, scans, or
// substitutes a target.
func (s *Service) ValidateSelection(ctx context.Context, selection Selection) (Selection, error) {
	return s.ValidateSelectionGeneration(ctx, SelectionValidationRequest{
		BrowserID:  selection.BrowserID,
		TargetID:   selection.TargetID,
		Generation: selection.Generation,
	})
}

// ValidateSelectionGeneration validates an exact generation-bearing identity.
func (s *Service) ValidateSelectionGeneration(ctx context.Context, request SelectionValidationRequest) (Selection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Selection{}, err
	}
	browserID := strings.TrimSpace(request.BrowserID)
	targetID := strings.TrimSpace(request.TargetID)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.validateSelectionGenerationLocked(browserID, targetID, request.Generation)
}

// RequireSelection is a descriptive alias for ValidateSelectionGeneration.
func (s *Service) RequireSelection(ctx context.Context, request SelectionValidationRequest) (Selection, error) {
	return s.ValidateSelectionGeneration(ctx, request)
}

// ValidateSelected is a descriptive alias for ValidateSelection.
func (s *Service) ValidateSelected(ctx context.Context, selection Selection) (Selection, error) {
	return s.ValidateSelection(ctx, selection)
}

func normalizeLifecycleEvent(event LifecycleEvent) (LifecycleEvent, *DiscoveryError) {
	event.BrowserID = strings.TrimSpace(event.BrowserID)
	event.TargetID = strings.TrimSpace(event.TargetID)
	if event.BrowserID == "" || event.TargetID == "" || hasControl(event.BrowserID) || hasControl(event.TargetID) {
		return LifecycleEvent{}, newProtocolInvalidAt("lifecycle", "unknown", "browser_target_required", nil)
	}
	if !publicIDPattern.MatchString(event.BrowserID) || !publicIDPattern.MatchString(event.TargetID) {
		return LifecycleEvent{}, newProtocolInvalidAt("lifecycle", "unknown", "normalized_ids_required", nil)
	}
	originalEventID := event.EventID
	event.EventID = lifecycleMarker(originalEventID)
	if event.EventID == "" && strings.TrimSpace(originalEventID) != "" {
		return LifecycleEvent{}, newProtocolInvalidAt("lifecycle", "unknown", "event_id_invalid", nil)
	}
	originalDocumentID := event.DocumentID
	event.DocumentID = lifecycleMarker(originalDocumentID)
	if event.DocumentID == "" && strings.TrimSpace(originalDocumentID) != "" {
		return LifecycleEvent{}, newProtocolInvalidAt("lifecycle", "unknown", "document_id_invalid", nil)
	}
	switch event.Type {
	case LifecycleNavigation, LifecycleDocumentReplaced, LifecycleTargetClosed, LifecycleTargetDetached:
		return event, nil
	default:
		return LifecycleEvent{}, newProtocolInvalidAt("lifecycle", "unknown", "unsupported_lifecycle_event", nil)
	}
}

func lifecycleMarker(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || hasControl(value) {
		return ""
	}
	return value
}

func lifecycleEventKey(event LifecycleEvent) string {
	prefix := string(event.Type) + "\x00" + event.BrowserID + "\x00" + event.TargetID + "\x00"
	if event.EventID != "" {
		return prefix + "event:" + event.EventID
	}
	if event.Sequence != 0 {
		return prefix + "sequence:" + strconv.FormatUint(event.Sequence, 10)
	}
	if event.DocumentID != "" {
		return prefix + "document:" + event.DocumentID
	}
	return ""
}

func firstLifecycleReason(reasons []string) string {
	if len(reasons) == 0 {
		return ""
	}
	return reasons[0]
}

func (s *Service) currentSelectionLocked() Selection {
	if s.selection == nil {
		return Selection{}
	}
	return *s.selection
}

func (s *Service) applyNavigationLocked(ctx context.Context, event LifecycleEvent) (Selection, *DiscoveryError) {
	state, previous, current, applied := s.advanceTargetGenerationLocked(event)
	if !applied {
		return s.currentSelectionLocked(), nil
	}
	if event.Capabilities != nil || event.WebMCP != nil || event.ToolCount != nil {
		applyLifecycleCapabilities(&state.target, event)
	}
	if event.DocumentID != "" {
		state.target.ContinuityMarker = targetContinuityMarker(
			event.BrowserID,
			state.rawID,
			state.target.Origin,
			state.pageWebSocket,
			TargetDescriptor{DocumentID: event.DocumentID},
		)
	}
	s.storeLifecycleTargetLocked(event.BrowserID, event.TargetID, state)
	reason := boundedLabel(event.Reason, maxLifecycleReason)
	if reason == "" {
		if event.Type == LifecycleDocumentReplaced {
			reason = "document_replaced"
		} else {
			reason = "navigation"
		}
	}
	s.emitTarget(EventPageGenerationChanged, event.BrowserID, event.TargetID, current, map[string]any{
		"previous_generation": previous,
		"current_generation":  current,
		"reason":              reason,
	})

	if s.selection == nil || s.selection.BrowserID != event.BrowserID || s.selection.TargetID != event.TargetID {
		return s.currentSelectionLocked(), nil
	}
	selection := *s.selection
	selection = selectionFromTarget(selection, state.target)
	selection.statusSet = true
	selection.connected = true
	selection.ready = false
	s.selection = &selection
	refreshed, refreshFailure := s.refreshSelectionLocked(ctx, event)
	if refreshFailure != nil {
		return refreshed, refreshFailure
	}
	if browser, ok := s.browsers[event.BrowserID]; ok {
		if persistenceFailure := s.persistSelectionLocked(ctx, browser, refreshed.Target, refreshed.SelectedAt); persistenceFailure != nil {
			return refreshed, persistenceFailure
		}
	}
	return refreshed, nil
}

func (s *Service) applyTargetClosedLocked(event LifecycleEvent) (Selection, *TargetHandle, *DiscoveryError) {
	state, previous, current, applied := s.advanceTargetGenerationLocked(event)
	if !applied {
		return s.currentSelectionLocked(), nil, nil
	}
	state.closed = true
	state.target.Generation = current
	state.generation = current
	state.target.Eligible = false
	state.target.EligibilityReason = "target_closed"
	s.storeLifecycleTargetLocked(event.BrowserID, event.TargetID, state)
	reason := boundedLabel(event.Reason, maxLifecycleReason)
	if reason == "" {
		reason = "target_closed"
	}
	s.emitTarget(EventPageGenerationChanged, event.BrowserID, event.TargetID, current, map[string]any{
		"previous_generation": previous,
		"current_generation":  current,
		"reason":              reason,
	})
	ownership := string(TargetOwnershipExternal)
	var release *TargetHandle
	if s.selection != nil && s.selection.BrowserID == event.BrowserID && s.selection.TargetID == event.TargetID {
		release = s.selection.Handle
		if release != nil {
			ownership = string(release.Ownership())
		}
		s.selection = nil
	}
	s.emitTarget(EventTargetDetached, event.BrowserID, event.TargetID, current, map[string]any{
		"generation":     current,
		"reason":         reason,
		"ownership_mode": ownership,
	})
	return Selection{}, release, nil
}

func (s *Service) advanceTargetGenerationLocked(event LifecycleEvent) (targetState, uint64, uint64, bool) {
	if s.targets == nil {
		s.targets = make(map[string]map[string]targetState)
	}
	if s.targets[event.BrowserID] == nil {
		s.targets[event.BrowserID] = make(map[string]targetState)
	}
	state, ok := s.targets[event.BrowserID][event.TargetID]
	if !ok && s.selection != nil && s.selection.BrowserID == event.BrowserID && s.selection.TargetID == event.TargetID {
		state = targetState{target: s.selection.Target, generation: s.selection.Generation}
		ok = true
	}
	if !ok {
		// An event for an unknown/unselected target cannot affect active
		// selection state. Ignore it until a normalized target is observed.
		return targetState{}, 0, 0, false
	}
	if (event.Type == LifecycleTargetClosed || event.Type == LifecycleTargetDetached) && state.closed {
		return state, state.generation, state.generation, false
	}
	previous := state.generation
	if previous == 0 {
		previous = state.target.Generation
	}
	if previous == 0 {
		previous = 1
	}
	current, applied := lifecycleGeneration(previous, event.PreviousGeneration, event.Generation)
	if !applied {
		return state, previous, current, false
	}
	state.generation = current
	state.target.Generation = current
	return state, previous, current, true
}

func lifecycleGeneration(previous, expectedPrevious, requested uint64) (uint64, bool) {
	if expectedPrevious != 0 && expectedPrevious != previous {
		return previous, false
	}
	if requested != 0 {
		if requested <= previous {
			return previous, false
		}
		return requested, true
	}
	if previous == ^uint64(0) {
		return previous, false
	}
	return previous + 1, true
}

func (s *Service) storeLifecycleTargetLocked(browserID, targetID string, state targetState) {
	if s.targets[browserID] == nil {
		s.targets[browserID] = make(map[string]targetState)
	}
	s.targets[browserID][targetID] = state
}

func applyLifecycleCapabilities(target *Target, event LifecycleEvent) {
	if target == nil {
		return
	}
	if event.Capabilities != nil {
		target.WebMCP = event.Capabilities.WebMCP
		target.WebMCPKnown = true
		if event.Capabilities.ToolCount >= 0 {
			target.ToolCount = event.Capabilities.ToolCount
			target.ToolCountKnown = event.Capabilities.ToolCountKnown || event.Capabilities.ToolCount >= 0
		}
	}
	if event.WebMCP != nil {
		target.WebMCP = *event.WebMCP
		target.WebMCPKnown = true
	}
	if event.ToolCount != nil && *event.ToolCount >= 0 {
		target.ToolCount = *event.ToolCount
		target.ToolCountKnown = true
	}
	if !target.WebMCP {
		target.Eligible = false
		target.EligibilityReason = "unsupported_webmcp"
	}
}

func selectionFromTarget(selection Selection, target Target) Selection {
	selection.BrowserID = target.BrowserID
	selection.TargetID = target.ID
	selection.Title = target.Title
	selection.URL = target.URL
	selection.Origin = target.Origin
	selection.Generation = target.Generation
	selection.Target = target
	return selection
}

func (s *Service) refreshSelectionLocked(ctx context.Context, event LifecycleEvent) (Selection, *DiscoveryError) {
	if s.selection == nil {
		return Selection{}, newNoEligibleTab("", TargetListOptions{}, 0)
	}
	selection := *s.selection
	state, stateOK := s.targets[selection.BrowserID][selection.TargetID]
	target := selection.Target
	if stateOK {
		target = state.target
	}

	if event.Capabilities != nil || event.WebMCP != nil || event.ToolCount != nil {
		applyLifecycleCapabilities(&target, event)
		state.target = target
		state.generation = target.Generation
		state.closed = false
		s.storeLifecycleTargetLocked(selection.BrowserID, selection.TargetID, state)
	} else if browser, ok := s.browsers[selection.BrowserID]; ok && (s.targetLister != nil || s.endpoints[selection.BrowserID].httpURL != "") {
		descriptors, failure := s.listTargetDescriptorsLocked(ctx, browser)
		if failure != nil {
			selection.statusSet = true
			selection.connected = true
			selection.ready = false
			s.selection = &selection
			return selection, failure
		}
		targets, normalizeFailure := s.normalizeTargetsLocked(ctx, browser, descriptors)
		if normalizeFailure != nil {
			selection.statusSet = true
			selection.connected = true
			selection.ready = false
			s.selection = &selection
			return selection, normalizeFailure
		}
		snapshot := makeTargetSnapshot(browser, targets, resolvedTargetListOptions(TargetListOptions{BrowserID: selection.BrowserID, EligibleOnly: Bool(false)}))
		s.emit(EventTargetsSnapshot, browser.ID, targetSnapshotPayload(snapshot))
		var found bool
		for _, candidate := range targets {
			if candidate.ID == selection.TargetID {
				target = candidate
				found = true
				break
			}
		}
		if !found {
			selection.statusSet = true
			selection.connected = false
			selection.ready = false
			s.selection = &selection
			return selection, newStaleSelection(selection.BrowserID, selection.TargetID, selection.Generation, "target_missing_after_refresh")
		}
	} else if s.targetProbe != nil {
		browser := s.browsers[selection.BrowserID]
		capabilities, probeErr := s.targetProbe.Probe(ctx, browser, target)
		if probeErr != nil {
			selection.statusSet = true
			selection.connected = true
			selection.ready = false
			s.selection = &selection
			return selection, classifyTargetListError(probeErr, browser)
		}
		target.WebMCP = capabilities.WebMCP
		target.WebMCPKnown = true
		if capabilities.ToolCount >= 0 {
			target.ToolCount = capabilities.ToolCount
			target.ToolCountKnown = capabilities.ToolCountKnown || capabilities.ToolCount >= 0
		}
		state.target = target
		state.generation = target.Generation
		state.closed = false
		s.storeLifecycleTargetLocked(selection.BrowserID, selection.TargetID, state)
	}

	selection = selectionFromTarget(selection, target)
	selection.statusSet = true
	selection.connected = true
	if !target.WebMCP || !target.WebMCPKnown {
		selection.ready = false
		s.selection = &selection
		return selection, newUnsupportedWebMCP(selection.BrowserID, selection.TargetID)
	}
	selection.ready = true
	s.selection = &selection
	return selection, nil
}

func (s *Service) validateSelectionGenerationLocked(browserID, targetID string, generation uint64) (Selection, error) {
	state, stateOK := s.targets[browserID][targetID]
	if stateOK && state.closed {
		return Selection{}, newStaleSelection(browserID, targetID, generation, "target_closed")
	}
	if s.selection == nil {
		reason := "selection_not_active"
		if stateOK && state.generation != generation {
			reason = "generation_changed"
		}
		return Selection{}, newStaleSelection(browserID, targetID, generation, reason)
	}
	current := *s.selection
	if current.BrowserID != browserID || current.TargetID != targetID {
		return Selection{}, newStaleSelection(browserID, targetID, generation, "selection_replaced")
	}
	if generation == 0 || current.Generation != generation || (stateOK && state.generation != generation) {
		reason := "generation_changed"
		if stateOK && state.closed {
			reason = "target_closed"
		}
		return Selection{}, newStaleSelection(browserID, targetID, generation, reason)
	}
	if !current.connected {
		return Selection{}, newStaleSelection(browserID, targetID, generation, "selection_disconnected")
	}
	if !current.ready {
		if !current.Target.WebMCP || !current.Target.WebMCPKnown {
			return Selection{}, newUnsupportedWebMCP(browserID, targetID)
		}
		return Selection{}, newStaleSelection(browserID, targetID, generation, "selection_not_ready")
	}
	return current, nil
}
