// Package errorpolicy owns credential-free room failure projections.
package errorpolicy

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

const unknownParticipantFailure = "unknown room participant failure"

// Service is stateless; each request copies caller-owned secrets before any
// error crosses the public room result boundary.
type Service struct{}

func New() *Service { return &Service{} }

func (s *Service) ParticipantFailure(request rooms.ParticipantFailureRequest) error {
	cause := request.Cause
	if cause == nil {
		cause = errors.New(unknownParticipantFailure)
	}
	return &participantFailure{participantID: request.ParticipantID, cause: cause, secrets: cloneSecrets(request.Secrets)}
}

func (s *Service) ParticipantFailureID(err error) (string, bool) {
	var failure rooms.ParticipantFailure
	if err == nil || !errors.As(err, &failure) || failure == nil {
		return "", false
	}
	return failure.ParticipantID(), true
}

func (s *Service) ParticipantFailureReason(request rooms.ParticipantFailureReasonRequest) string {
	if cause := participantFailureCause(request.Error, request.Secrets); cause != "" {
		return cause
	}
	if closeReason := strings.TrimSpace(request.CloseReason); closeReason != "" {
		if reason := strings.TrimSpace(s.Sanitize(errors.New(closeReason), request.Secrets)); reason != "" {
			return reason
		}
	}
	if request.TransportDisconnected {
		return "transport disconnected"
	}
	if request.TerminationReason == rooms.ParticipantTerminationDisconnected {
		return "participant disconnected"
	}
	return "participant failure"
}

func (s *Service) Sanitize(err error, secrets []string) string {
	if err == nil {
		return ""
	}
	return sanitizeText(err.Error(), secrets)
}

func (s *Service) FailureResult(err error, secrets []string) rooms.RoomFailureResult {
	return rooms.RoomFailureResult{
		TerminationReason: rooms.RoomTerminationFailed,
		Reason:            rooms.RoomTerminationFailed,
		Error:             s.Sanitize(err, secrets),
		Participants:      make(map[string]struct{}),
	}
}

type participantFailure struct {
	participantID string
	cause         error
	secrets       []string
}

func (e *participantFailure) Error() string {
	if e == nil {
		return "room failure"
	}
	value := fmt.Sprintf("room participant %q", e.participantID)
	if e.cause != nil {
		value += ": " + e.cause.Error()
	}
	return sanitizeText(value, e.secrets)
}

func (e *participantFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *participantFailure) ParticipantID() string {
	if e == nil {
		return ""
	}
	return e.participantID
}

func participantFailureCause(err error, secrets []string) string {
	if err == nil {
		return ""
	}
	var failure *participantFailure
	if errors.As(err, &failure) && failure != nil {
		secrets = append(append([]string(nil), secrets...), failure.secrets...)
		err = failure.cause
	}
	return strings.TrimSpace(sanitizeText(errorText(err), secrets))
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func cloneSecrets(secrets []string) []string {
	cloned := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret != "" {
			cloned = append(cloned, secret)
		}
	}
	return cloned
}

func sanitizeText(value string, secrets []string) string {
	if value == "" {
		return ""
	}
	for _, secret := range sortedSecrets(secrets) {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	for _, marker := range []string{"authorization: bearer ", "authorization=bearer ", "authorization: ", "authorization=", "x-api-key: ", "x-api-key=", "api-key: ", "api-key=", "api_key: ", "api_key=", "bearer "} {
		value = redactMarker(value, marker)
	}
	return value
}

func redactMarker(value, marker string) string {
	for {
		lower := strings.ToLower(value)
		start := strings.Index(lower, marker)
		if start < 0 {
			return value
		}
		markerEnd := start + len(marker)
		if strings.HasPrefix(value[markerEnd:], "[REDACTED]") {
			return value
		}
		end := markerEnd
		for end < len(value) {
			switch value[end] {
			case ' ', '\t', '\r', '\n', ',', ';', ')', ']', '}':
				goto tokenEnd
			default:
				end++
			}
		}
	tokenEnd:
		value = value[:markerEnd] + "[REDACTED]" + value[end:]
	}
}

func sortedSecrets(secrets []string) []string {
	cloned := cloneSecrets(secrets)
	sort.SliceStable(cloned, func(i, j int) bool { return len(cloned[i]) > len(cloned[j]) })
	return cloned
}

var _ rooms.FailureService = (*Service)(nil)
