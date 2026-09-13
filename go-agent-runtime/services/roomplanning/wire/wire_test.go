package wire

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomplanning"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

type wireInferencer struct{}

func (wireInferencer) ConnectSession(context.Context) (messages.Session, error) { return nil, nil }

func TestGeneratedWireConstructsIndependentServices(t *testing.T) {
	first := NewService(Dependencies{})
	second := NewService(Dependencies{})
	manifest := rooms.Manifest{Participants: []rooms.Participant{{ID: "alpha", Kind: rooms.ParticipantKindAgent}}}
	for _, service := range []roomplanning.Service{first, second} {
		_, err := service.Plan(context.Background(), roomplanning.Options{
			Manifest:   manifest,
			Filesystem: &roomplanning.FilesystemScope{PrimaryRoot: t.TempDir()},
			SessionFactory: func(roomplanning.LiveSessionRequest) (messages.SessionInferencer, error) {
				return wireInferencer{}, nil
			},
		})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
	}
}
