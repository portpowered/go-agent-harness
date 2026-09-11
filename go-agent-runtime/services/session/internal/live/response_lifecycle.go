package live

import "strings"

func (h *handle) observeResponseStartLocked(responseID string) bool {
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		// Providers that omit response IDs may emit more than one message
		// start for a single tool-bearing response (for example, one start per
		// tool item). Keep that anonymous stream as one lifecycle owner; there
		// is no identifier with which to distinguish an additional response.
		if h.anonymousResponses > 0 {
			return false
		}
		h.anonymousResponses++
	} else {
		if h.activeResponseIDs == nil {
			h.activeResponseIDs = make(map[string]struct{})
		}
		if _, exists := h.activeResponseIDs[responseID]; exists {
			return false
		}
		h.activeResponseIDs[responseID] = struct{}{}
	}
	h.responsePending, h.responseActive = false, true
	return true
}
