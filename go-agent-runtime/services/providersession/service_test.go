package providersession

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func TestPublicContractsPreserveErrorsReplayToolsAndSliceOwnership(t *testing.T) {
	if ErrAudioSampleRateConflict.Error() != "session input and output sample rates conflict" {
		t.Fatalf("rate conflict text = %q", ErrAudioSampleRateConflict)
	}
	if !errors.Is(ErrAudioSampleRateConflict, errors.New("session input and output sample rates conflict")) {
		t.Fatal("rate conflict does not preserve the legacy errors.Is identity")
	}
	if got, known, err := (ReplayTools{}).Names("capture", 7, map[string]json.RawMessage{}); err != nil || !known || len(got) != 0 {
		t.Fatalf("missing replay tools = (%v, %t, %v)", got, known, err)
	}
	got, known, err := (ReplayTools{}).Names("capture", 7, map[string]json.RawMessage{
		"tools": json.RawMessage(`[{"name":" lookup "},{"name":""},{"name":"weather"}]`),
	})
	if err != nil || !known || len(got) != 2 || got[0] != "lookup" || got[1] != "weather" {
		t.Fatalf("replay tools = (%v, %t, %v)", got, known, err)
	}
	if _, _, err := (ReplayTools{}).Names("capture", 8, map[string]json.RawMessage{"tools": json.RawMessage(`{"name":"bad"}`)}); err == nil {
		t.Fatal("invalid replay tools were accepted")
	}

}

func TestPlanApplyTurnDetectionClonesPolicy(t *testing.T) {
	policy := &models.TurnDetectionConfig{Type: "server_vad", CreateResponse: boolPointer(true)}
	planned := Plan{TurnDetection: policy}
	inferencer := &turnDetectionInferencer{}
	planned.ApplyTurnDetection(inferencer)
	if inferencer.policy == nil || inferencer.policy == policy || inferencer.policy.Type != policy.Type || inferencer.policy.CreateResponse == policy.CreateResponse || !*inferencer.policy.CreateResponse {
		t.Fatalf("applied policy = %#v, want a deep copy", inferencer.policy)
	}
	inferencer.policy.Type = "mutated"
	*inferencer.policy.CreateResponse = false
	if policy.Type != "server_vad" || !*policy.CreateResponse {
		t.Fatal("turn detection policy was not isolated from the inferencer")
	}

	planned = Plan{}
	planned.ApplyTurnDetection(inferencer)
	if inferencer.policy != nil {
		t.Fatalf("cleared policy = %#v, want nil", inferencer.policy)
	}
}

func boolPointer(value bool) *bool { return &value }

type turnDetectionInferencer struct{ policy *models.TurnDetectionConfig }

func (*turnDetectionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, nil
}

func (i *turnDetectionInferencer) SetSessionTurnDetection(policy *models.TurnDetectionConfig) {
	i.policy = policy
}
