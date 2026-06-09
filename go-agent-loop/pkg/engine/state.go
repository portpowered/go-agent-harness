package engine

import (
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

// RunState represents the current state of the engine.
type RunState string

const (
	RunStateRunning RunState = "running"
	RunStateStopped RunState = "stopped"
	RunStatePaused  RunState = "paused"
	RunStateError   RunState = "error"
)

// SharedState holds all mutable state for the engine across ticks.
type SharedState struct {
	mu sync.RWMutex

	RunState  RunState
	Error     error
	LoopState *state.LoopState
}

func NewSharedState(outputs state.OutputBuffers) *SharedState {
	return &SharedState{
		RunState: RunStateStopped,
		LoopState: &state.LoopState{
			History: state.History{
				ConversationBuffer:      []messages.Message{},
				ConversationDeltaBuffer: []messages.StreamMessage{},
			},
			Inputs: state.Buffers{
				UserOutputMessage:       []messages.Message{},
				UserInputDelta:          []messages.StreamMessage{},
				UserControlPlaneMessage: []messages.Message{},
				ToolOutputMessage:       []messages.Message{},
				ToolInputDelta:          []messages.StreamMessage{},
				ToolControlPlaneMessage: []messages.Message{},
				ModelOutputMessage:      []messages.Message{},
				ModelInputDelta:         []messages.StreamMessage{},
				InteractionEvents:       []messages.InteractionEvent{},
			},
			Outputs:   outputs,
			Lifecycle: state.LoopLifecycleStateCreated,
		},
	}
}

func (s *SharedState) SetRunState(state RunState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RunState = state
}

func (s *SharedState) GetRunState() RunState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.RunState
}

func (s *SharedState) SetError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Error = err
	s.RunState = RunStateError
}
