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
