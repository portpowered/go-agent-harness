package webmcp

import "sort"

type browserReplacementCleanup struct {
	session TargetSession
	handle  BrowserHandle
}

func (b *StatefulBroker) reconcileBrowserReplacementsLocked(candidates []BrowserCandidate) ([]browserReplacementCleanup, map[BrowserID]struct{}) {
	if b == nil || len(candidates) == 0 || len(b.browsers) == 0 {
		return nil, nil
	}
	ids := make([]BrowserID, 0, len(b.browsers))
	for browserID := range b.browsers {
		ids = append(ids, browserID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	cleanups := make([]browserReplacementCleanup, 0)
	replaced := make(map[BrowserID]struct{})
	for _, candidate := range candidates {
		for _, browserID := range ids {
			state := b.browsers[browserID]
			if state == nil || !browserCandidatesReplaced(state.candidate, candidate) {
				continue
			}
			cleanup := b.retireBrowserReplacementLocked(browserID)
			cleanups = append(cleanups, cleanup)
			replaced[browserID] = struct{}{}
		}
	}
	return cleanups, replaced
}

func (b *StatefulBroker) retireBrowserReplacementLocked(browserID BrowserID) browserReplacementCleanup {
	state := b.browsers[browserID]
	cleanup := browserReplacementCleanup{}
	if state != nil {
		cleanup.handle = state.handle
	}
	if b.selected != nil && b.selected.context.Key.BrowserID == browserID {
		selected := b.selected
		b.retireSessionLocked(selected, "browser_replaced")
		b.selected = nil
		cleanup.session = selected.session
		if cleanup.handle == nil {
			cleanup.handle = selected.handle
		}
	}
	delete(b.browsers, browserID)
	return cleanup
}

func closeBrowserReplacementCleanups(cleanups []browserReplacementCleanup) {
	for _, cleanup := range cleanups {
		if cleanup.session != nil {
			_ = cleanup.session.Close()
		}
		if cleanup.handle != nil {
			_ = cleanup.handle.Close()
		}
	}
}
