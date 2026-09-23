package sessiontrace

import (
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type toolErrorCode string

func (e toolErrorCode) Error() string { return string(e) }

const ErrUnresolvedToolResults toolErrorCode = "session ended with unresolved tool results"

type UnresolvedToolResultsError struct {
	CallIDs      []string
	SendStatuses map[string]messages.SessionSendStatus
}

func (e *UnresolvedToolResultsError) Error() string {
	if e == nil || len(e.CallIDs) == 0 {
		return ErrUnresolvedToolResults.Error()
	}
	message := fmt.Sprintf("tool results were not delivered for %d unresolved call(s): %s", len(e.CallIDs), strings.Join(e.CallIDs, ", "))
	statusParts := make([]string, 0, len(e.SendStatuses))
	for _, id := range e.CallIDs {
		if status, ok := e.SendStatuses[id]; ok && status != "" {
			statusParts = append(statusParts, fmt.Sprintf("%s=%s", id, status))
		}
	}
	if len(statusParts) > 0 {
		message += " (send outcomes: " + strings.Join(statusParts, ", ") + ")"
	}
	return message
}

func (e *UnresolvedToolResultsError) Unwrap() error { return ErrUnresolvedToolResults }
func (e *UnresolvedToolResultsError) UnresolvedCallIDs() []string {
	if e == nil {
		return nil
	}
	return append([]string(nil), e.CallIDs...)
}
