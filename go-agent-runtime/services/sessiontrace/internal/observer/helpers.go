package observer

import (
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
)

func NewUnresolvedToolResultsError(ids []string, statuses map[string]messages.SessionSendStatus) *sessiontrace.UnresolvedToolResultsError {
	seen := make(map[string]struct{}, len(ids))
	ordered := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	owned := make(map[string]messages.SessionSendStatus, len(statuses))
	for _, id := range ordered {
		if status, ok := statuses[id]; ok {
			owned[id] = status
		}
	}
	return &sessiontrace.UnresolvedToolResultsError{CallIDs: ordered, SendStatuses: owned}
}
