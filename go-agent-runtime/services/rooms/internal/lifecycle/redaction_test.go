package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// TestRunnerRedactsParticipantCredentialsFromRunFailures pins the host
// rendering contract: a provider failure that echoes a participant credential
// (from the environment or the config file) reaches the host error, the room
// result, and participant callbacks with the credential replaced by the same
// marker room evidence uses.
func TestRunnerRedactsParticipantCredentialsFromRunFailures(t *testing.T) {
	const envSecret = "sk-env-alice-0123456789"
	const configSecret = "sk-config-bob-9876543210"
	openErr := fmt.Errorf("provider rejected credentials %s and %s", envSecret, configSecret)
	runner := New(Dependencies{Live: &fakeLiveService{openErr: openErr}, Clock: platformclock.Real{}})
	var terminated []rooms.RoomParticipantResult
	result, err := runner.Run(context.Background(), nil, rooms.RoomRunOptions{
		Manifest: testManifest(),
		CredentialLookup: func(name string) (string, bool) {
			if name == "ALICE_KEY" {
				return envSecret, true
			}
			return "", false
		},
		ConfigCredential: func(name string) (string, error) {
			if name == "BOB_KEY" {
				return configSecret, nil
			}
			return "", errors.New("not configured")
		},
		OnParticipantTerminated: func(value rooms.RoomParticipantResult) { terminated = append(terminated, value) },
	})
	if err == nil {
		t.Fatal("Run error = nil, want provider failure")
	}
	if !errors.Is(err, openErr) {
		t.Fatalf("Run error %v does not preserve the provider failure identity", err)
	}
	visible := []string{err.Error(), result.Error}
	for _, participant := range result.Participants {
		visible = append(visible, participant.Error, participant.TerminalReason)
	}
	for _, participant := range terminated {
		visible = append(visible, participant.Error)
	}
	if len(terminated) == 0 {
		t.Fatal("no participant termination callbacks")
	}
	for _, text := range visible {
		for _, secret := range []string{envSecret, configSecret} {
			if strings.Contains(text, secret) {
				t.Fatalf("host-visible failure %q leaks credential %q", text, secret)
			}
		}
	}
	if !strings.Contains(err.Error(), "provider rejected credentials [REDACTED] and [REDACTED]") {
		t.Fatalf("Run error = %q, want redacted provider failure", err)
	}
}
