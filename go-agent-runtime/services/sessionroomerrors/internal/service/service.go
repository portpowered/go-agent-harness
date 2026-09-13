// Package service contains the private room-error policy implementation.
package service

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionroomerrors"
)

const unknownParticipantFailure = "unknown room participant failure"

// Service is stateless; every Wire construction returns an independent
// instance and every request copies caller-owned secret input.
type Service struct{}

func New() *Service { return &Service{} }

func (s *Service) ParticipantFailure(request sessionroomerrors.ParticipantFailureRequest) error {
	cause := request.Cause
	if cause == nil {
		cause = errors.New(unknownParticipantFailure)
	}
	return &participantFailure{
		participantID: request.ParticipantID,
		cause:         cause,
		secrets:       cloneSecrets(request.Secrets),
	}
}

func (s *Service) ParticipantFailureReason(request sessionroomerrors.ParticipantFailureReasonRequest) string {
	if cause := participantFailureCause(request.Error, request.Secrets); cause != "" {
		return cause
	}
	if closeReason := strings.TrimSpace(request.CloseReason); closeReason != "" {
		if reason := strings.TrimSpace(sanitizeText(closeReason, request.Secrets)); reason != "" {
			return reason
		}
	}
	if request.TransportDisconnected {
		return "transport disconnected"
	}
	if request.TerminationReason == sessionroomerrors.ParticipantTerminationDisconnected {
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

func (s *Service) FailureResult(err error, secrets []string) sessionroomerrors.RoomFailureResult {
	return sessionroomerrors.RoomFailureResult{
		TerminationReason: sessionroomerrors.RoomTerminationFailed,
		Reason:            sessionroomerrors.RoomTerminationFailed,
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
	if err == nil {
		return ""
	}
	return strings.TrimSpace(sanitizeText(err.Error(), secrets))
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
	for _, marker := range credentialMarkers() {
		value = redactCredentialMarker(value, marker)
	}
	return value
}

func credentialMarkers() []string {
	return []string{
		"authorization: bearer ",
		"authorization=bearer ",
		"authorization: ",
		"authorization=",
		"x-api-key: ",
		"x-api-key=",
		"api-key: ",
		"api-key=",
		"api_key: ",
		"api_key=",
		"bearer ",
	}
}

func redactCredentialMarker(value, marker string) string {
	for {
		markerStart := strings.Index(strings.ToLower(value), marker)
		if markerStart < 0 {
			return value
		}
		markerEnd := markerStart + len(marker)
		if strings.HasPrefix(value[markerEnd:], "[REDACTED]") {
			return value
		}
		tokenEnd := strings.IndexAny(value[markerEnd:], " \t\r\n,;)]}")
		if tokenEnd < 0 {
			tokenEnd = len(value) - markerEnd
		}
		tokenEnd += markerEnd
		value = value[:markerEnd] + "[REDACTED]" + value[tokenEnd:]
	}
}

func sortedSecrets(secrets []string) []string {
	cloned := cloneSecrets(secrets)
	sort.SliceStable(cloned, func(i, j int) bool { return len(cloned[i]) > len(cloned[j]) })
	return cloned
}
