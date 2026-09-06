package input

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/stretchr/testify/require"
)

func TestCloneLiveRequestOwnsMutableAdmissionData(t *testing.T) {
	create, interrupt := true, false
	request := session.LiveRequest{
		ToolNames:     []string{"first", "second"},
		TurnDetection: &session.LiveTurnDetection{CreateResponse: &create, InterruptResponse: &interrupt},
		Capabilities: &session.LiveCapabilities{Definitions: []messages.ToolDefinition{
			{Name: "first", Parameters: []messages.ToolParameter{{Name: "target"}}, ParameterSchema: []byte(`{"type":"object"}`)},
			{Name: "second"},
		}},
		ReplayPlan: &session.LiveReplayPlan{AudioTurns: []session.LiveReplayAudioTurn{{Chunks: [][]int16{{1, -2}, {3}}}}},
	}
	cloned := CloneLiveRequest(request)
	require.Equal(t, request, cloned)
	request.ToolNames[0] = "changed allowlist"
	create, interrupt = false, true
	request.Capabilities.Definitions[0].Name = "changed definition"
	request.Capabilities.Definitions[0].Parameters[0].Name = "changed parameter"
	request.Capabilities.Definitions[0].ParameterSchema[0] = '!'
	request.ReplayPlan.AudioTurns[0].Chunks[0][0] = 99
	require.Equal(t, []string{"first", "second"}, cloned.ToolNames)
	require.True(t, *cloned.TurnDetection.CreateResponse)
	require.False(t, *cloned.TurnDetection.InterruptResponse)
	require.Equal(t, "first", cloned.Capabilities.Definitions[0].Name)
	require.Equal(t, "second", cloned.Capabilities.Definitions[1].Name)
	require.Equal(t, "target", cloned.Capabilities.Definitions[0].Parameters[0].Name)
	require.JSONEq(t, `{"type":"object"}`, string(cloned.Capabilities.Definitions[0].ParameterSchema))
	require.Equal(t, []int16{1, -2}, cloned.ReplayPlan.AudioTurns[0].Chunks[0])
	cloned.ReplayPlan.AudioTurns[0].Chunks[1][0] = 88
	require.Equal(t, int16(3), request.ReplayPlan.AudioTurns[0].Chunks[1][0])
}

func TestCloneContentPartsOwnsEveryBinaryPart(t *testing.T) {
	data := []byte{1, 2, 3}
	parts := []messages.ContentPart{
		messages.TextPart{Text: "preserved"}, messages.ImagePart{Bytes: data},
		messages.AudioPart{Bytes: data}, messages.VideoPart{Bytes: data},
		messages.FilePart{Bytes: data}, messages.EmbeddingPart{Bytes: data},
	}
	cloned := CloneContentParts(parts)
	require.Equal(t, parts, cloned)
	data[0] = 9
	expected := []byte{1, 2, 3}
	require.Equal(t, messages.TextPart{Text: "preserved"}, cloned[0])
	require.Equal(t, messages.ImagePart{Bytes: expected}, cloned[1])
	require.Equal(t, messages.AudioPart{Bytes: expected}, cloned[2])
	require.Equal(t, messages.VideoPart{Bytes: expected}, cloned[3])
	require.Equal(t, messages.FilePart{Bytes: expected}, cloned[4])
	require.Equal(t, messages.EmbeddingPart{Bytes: expected}, cloned[5])
	require.Nil(t, CloneContentParts(nil))
	require.Equal(t, session.LiveRequest{}, CloneLiveRequest(session.LiveRequest{}))
}
