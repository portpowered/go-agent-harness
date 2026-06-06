package agentloop

import (
	"context"
	"io"

	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// AgenticLoop is the porcelain API for interacting with the agentic loop engine.
type AgenticLoop interface {
	// Execute sends a command and returns the complete response as an [ExecuteResult]
	// containing all messages produced during the turn.
	Execute(ctx context.Context, input ExecuteInput) (ExecuteResult, error)
	// ExecuteStreaming sends a command and returns a [StreamingExecuteResult] whose
	// EventStream delivers typed delta events in real time via the [Stream] interface.
	// Call HasNext/Response to consume events; Messages() becomes available after the turn completes.
	ExecuteStreaming(ctx context.Context, input ExecuteInput) (*StreamingExecuteResult, error)
	// Run starts the loop in continuous mode (for TurnTaking/AllAtOnce). Blocks until paused or stopped.
	Run(ctx context.Context) error

	// GetConversationHistory returns a copy of the conversation buffer (full messages).
	GetConversationHistory() []messages.Message
	// GetConversationDeltas returns a copy of the conversation delta buffer (streaming deltas).
	GetConversationDeltas() []messages.StreamMessage

	// Sends messages to the loop, in the context of the model, this is done for
	// - resuming the loop while its awaiting an input from the user
	// - interrupting the loop while the model is running, i.e. if you send a message for a new conversation or something, then the message will be enqueued.
	// The loop will then be interrupted at whatever is current context screen. If the message has an index that is lower than the current top message in the history, then we presume
	// that the message is more correct, and you intend to override the current message in the history, and we run the loop based on a rewritten history.
	Send(ctx context.Context, msg []messages.Message) error

	// Pause pauses the loop.
	Pause(ctx context.Context) error
	// GetState returns the current loop state.
	GetState(ctx context.Context) (AgenticLoopState, error)
	// SetInputs configures the input sources.
	SetInputs(ctx context.Context, inputs []Input) error
	// SetOutputs configures the output sinks.
	SetOutputs(ctx context.Context, outputs []Output) error

	// EnqueueTodo appends a message to the TODO queue for deferred processing.
	EnqueueTodo(msg string)
	// DequeueTodo removes and returns the next TODO message. Returns false if the queue is empty.
	DequeueTodo() (string, bool)
	// TodoQueueLen returns the number of messages in the TODO queue.
	TodoQueueLen() int
}

// The way a customer runs an agentic loop in a duplexed way is as follows:
// ctx := context.Background()
// defer ctx.Done()
// k := NewAgenticLoop(ctx, agent_loop.WithTextOut(stdOut))
// go k.Run(ctx)
// k.Send(ctx, []messages.Message{messages.NewTextMessage(messages.RoleUser, "Hello, how are you?")})
// ... model receives and responds into the agent loop, the messages are dispatched to the user's std out.
// Message sends to index 130
// k.SendStreaming(ctx, []messages.StreamMessage{messages.NewMessageStartValue(index: 123), messages.NewTextDeltaValue("wait, stop right there! I meant do this", index: 124)})
// ... loop gets reboot to execute at the index 123, as the interrupt message.
// This works for multimodal and text messages.

// This is how its modelled for an IO stream such as with built in MIC/speaker
// k := NewAgenticLoop(ctx, agent_loop.WithAudioOut(speakerBuffer), agent_loop.WithAudioIn(micBuffer))
// go k.Run(ctx)
// system runs the model.
// ... model receives and responds into the agent loop, the messages are dispatched to the user's speaker.
// ... customer sends messages into the agent loop via the audio in buffer.

// AgenticLoopState represents the current state of the loop.
type AgenticLoopState struct {
	RunState RunState
}

// RunState represents the execution state of the loop.
type RunState string

const (
	RunStateRunning RunState = "running"
	RunStateStopped RunState = "stopped"
	RunStatePaused  RunState = "paused"
	RunStateError   RunState = "error"
)

// InputType describes the modality of an input source.
type InputType string

const (
	InputTypeText  InputType = "TEXT"
	InputTypeAudio InputType = "AUDIO"
	InputTypeImage InputType = "IMAGE"
	InputTypeVideo InputType = "VIDEO"
)

// Input represents an input source to the loop.
type Input struct {
	Reader io.Reader
	Label  string
	Type   InputType
}

// Output represents an output sink from the loop.
type Output struct {
	Writer io.Writer
	Label  string
}
