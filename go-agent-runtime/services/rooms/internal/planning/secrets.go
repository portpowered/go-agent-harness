package planning

import (
	"os"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

// envCredentialPrefix is the optional explicit form of an environment
// credential reference accepted by the live provider edge.
const envCredentialPrefix = "env:"

// EvidenceSecrets resolves the credential values the room's participants use
// so the evidence owner can redact them from every artifact it writes. The
// values live only in the recorder request; the manifest keeps references.
func EvidenceSecrets(manifest rooms.Manifest, request rooms.RoomRunOptions) []string {
	lookup := request.CredentialLookup
	if lookup == nil {
		lookup = os.LookupEnv
	}
	set := secretSet{seen: make(map[string]struct{})}
	for _, participant := range manifest.Participants {
		reference := strings.TrimPrefix(strings.TrimSpace(participant.APIKeyEnv), envCredentialPrefix)
		if reference == "" {
			continue
		}
		if value, ok := lookup(reference); ok {
			set.add(value)
		}
		if request.ConfigCredential != nil {
			// A config read failure leaves nothing to redact from that source:
			// the participant cannot have received a credential it could not load.
			if value, err := request.ConfigCredential(reference); err == nil {
				set.add(value)
			}
		}
	}
	// Redact longer values first so a secret containing another secret is
	// never left partially visible.
	sort.SliceStable(set.values, func(i, j int) bool { return len(set.values[i]) > len(set.values[j]) })
	return set.values
}

type secretSet struct {
	seen   map[string]struct{}
	values []string
}

func (s *secretSet) add(value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	if _, ok := s.seen[value]; ok {
		return
	}
	s.seen[value] = struct{}{}
	s.values = append(s.values, value)
}

// redactedMarker replaces a participant credential in host-visible failure
// text, matching the room evidence redaction marker.
const redactedMarker = "[REDACTED]"

// redactedRunError presents a run error with participant credentials
// replaced while keeping the original chain for errors.Is/As.
type redactedRunError struct {
	message string
	cause   error
}

func (e *redactedRunError) Error() string { return e.message }

func (e *redactedRunError) Unwrap() error { return e.cause }

// RedactRunFailure removes the room's participant credentials — the set
// EvidenceSecrets gives the evidence owner — from the run error and every
// failure string in the room result. Hosts render these values, and provider
// or admission failures can echo a credential. Secrets are resolved only when
// failure text exists.
func RedactRunFailure(result rooms.RoomResult, runErr error, manifest rooms.Manifest, request rooms.RoomRunOptions) (rooms.RoomResult, error) {
	if runErr == nil && !resultHasFailureText(result) {
		return result, nil
	}
	secrets := EvidenceSecrets(manifest, request)
	if len(secrets) == 0 {
		return result, runErr
	}
	result.Error = redactSecrets(result.Error, secrets)
	if result.Participants != nil {
		participants := make(map[string]rooms.RoomParticipantResult, len(result.Participants))
		for id, participant := range result.Participants {
			participant.Error = redactSecrets(participant.Error, secrets)
			participant.TerminalReason = redactSecrets(participant.TerminalReason, secrets)
			participants[id] = participant
		}
		result.Participants = participants
	}
	if runErr != nil {
		if message := redactSecrets(runErr.Error(), secrets); message != runErr.Error() {
			runErr = &redactedRunError{message: message, cause: runErr}
		}
	}
	return result, runErr
}

func resultHasFailureText(result rooms.RoomResult) bool {
	if result.Error != "" {
		return true
	}
	for _, participant := range result.Participants {
		if participant.Error != "" || participant.TerminalReason != "" {
			return true
		}
	}
	return false
}

// redactSecrets relies on EvidenceSecrets ordering longer values first so a
// secret containing another is never left partially visible.
func redactSecrets(value string, secrets []string) string {
	for _, secret := range secrets {
		value = strings.ReplaceAll(value, secret, redactedMarker)
	}
	return value
}
