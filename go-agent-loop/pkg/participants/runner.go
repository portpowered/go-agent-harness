package participants

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ParticipantRunner is implemented by active participants that run as background
// goroutines, reading from their inbox and writing results to their outbox.
type ParticipantRunner interface {
	// Run starts the participant's processing loop. It must return when ctx is cancelled.
	Run(ctx context.Context) error
}

// ActiveParticipant wraps a ParticipantRunner with lifecycle management.
type ActiveParticipant struct {
	id     messages.ParticipantID
	runner ParticipantRunner
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// NewActiveParticipant creates a participant backed by a runner goroutine.
func NewActiveParticipant(id messages.ParticipantID, runner ParticipantRunner) *ActiveParticipant {
	return &ActiveParticipant{
		id:     id,
		runner: runner,
	}
}

// ID returns the participant's identifier.
func (p *ActiveParticipant) ID() messages.ParticipantID { return p.id }

// Start launches the runner goroutine. The runner will be cancelled when Stop is called
// or when the parent context is cancelled.
func (p *ActiveParticipant) Start(ctx context.Context) {
	rctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.done = make(chan struct{})

	go func() {
		defer close(p.done)
		p.err = p.runner.Run(rctx)
	}()
}

// Stop cancels the runner goroutine and waits for it to finish.
func (p *ActiveParticipant) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	if p.done != nil {
		<-p.done
	}
}

// Err returns the error the runner returned. It is valid after Stop returns
// and is nil while the runner is still active or was never started.
func (p *ActiveParticipant) Err() error {
	if p.done == nil {
		return nil
	}
	select {
	case <-p.done:
		return p.err
	default:
		return nil
	}
}

// joinOnFailure attaches cleanupErr only to an operation that already failed.
// A cleanup failure after a successful operation does not turn that success
// into a failure, and a nil cleanup error leaves err's identity unchanged.
func joinOnFailure(err, cleanupErr error) error {
	if err == nil || cleanupErr == nil {
		return err
	}
	return errors.Join(err, cleanupErr)
}

type MessageReader struct {
	UserRequest  *messages.TypedBuffer[messages.UserRequest]
	UserResponse *messages.TypedBuffer[messages.UserResponse]
}

// UserRunner runs asynchronously as an active participant.
// It reads requests to the user from its inbox, does something based on the request, then writes to its outbox.
// Each message written is tagged with runner-specific ordering: ActorStreamID, ActorProvidedIndex,
// ActorProvidedID, ActorID (see ORDERING.md).
type UserRunner struct {
	Inbox      *messages.TypedBuffer[messages.UserRequest]
	Outbox     *messages.TypedBuffer[messages.UserResponse]
	outChannel chan messages.UserRequest

	streamID   string // stable stream id for this runner (one stream per user participant)
	actorIndex int    // incremented for each message written
}

func NewUserRunner(bufferCapacity int) *UserRunner {
	return &UserRunner{
		Inbox:      messages.NewTypedBuffer[messages.UserRequest](bufferCapacity),
		Outbox:     messages.NewTypedBuffer[messages.UserResponse](bufferCapacity),
		outChannel: nil,
		streamID:   mustStreamID("user"),
	}
}

func (r *UserRunner) Run(ctx context.Context) error {
	for {
		req, ok := r.Inbox.ReadBlocking(ctx.Done())
		if !ok {
			return ctx.Err()
		}
		if r.outChannel != nil {
			// The turn-taking loop stops selecting userOut as soon as its
			// context is cancelled. Make publication cancellation-aware so a
			// model response racing shutdown cannot strand this participant and
			// prevent AgentLoop.Run from joining its workers.
			select {
			case r.outChannel <- req:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func (r *UserRunner) OutChannel() <-chan messages.UserRequest {
	if r.outChannel == nil {
		r.outChannel = make(chan messages.UserRequest)
	}
	return r.outChannel
}

func (r *UserRunner) Write(ctx context.Context, msg messages.Message) error {
	r.applyOrdering(&msg)
	write := r.Outbox.Write(ctx, messages.UserResponse{Message: msg})
	if !write {
		return fmt.Errorf("failed to write user request, buffer is full")
	}
	return nil
}

// Send a signal to stop the model.
func (r *UserRunner) Stop(ctx context.Context) error {
	msg := messages.Message{
		Role:         messages.RoleUser,
		ContentParts: []messages.ContentPart{messages.NewTextPart("stop")},
	}
	r.applyOrdering(&msg)
	write := r.Outbox.Write(ctx, messages.UserResponse{Message: msg})
	if !write {
		return fmt.Errorf("failed to write user request, buffer is full")
	}
	return nil
}

// applyOrdering sets runner-specific ordering on the message (ActorStreamID, ActorProvidedIndex, ActorProvidedID, ActorID).
func (r *UserRunner) applyOrdering(msg *messages.Message) {
	msg.ActorStreamID = r.streamID
	msg.ActorProvidedIndex = r.actorIndex
	msg.ActorProvidedID = fmt.Sprintf("user-%s-%d", r.streamID, r.actorIndex)
	msg.ActorID = messages.User
	r.actorIndex++
}

// InteractionRunner is an input-only participant used to feed normalized
// gateway interaction events into the agent loop.
type InteractionRunner struct {
	Outbox     *messages.TypedBuffer[messages.InteractionEvent]
	actorIndex int
}

func NewInteractionRunner(bufferCapacity int) *InteractionRunner {
	return &InteractionRunner{
		Outbox: messages.NewTypedBuffer[messages.InteractionEvent](bufferCapacity),
	}
}

func (r *InteractionRunner) Write(ctx context.Context, event messages.InteractionEvent) error {
	if ok := r.Outbox.Write(ctx, event); !ok {
		return fmt.Errorf("failed to write interaction event, buffer is full")
	}
	r.actorIndex++
	return nil
}

func (r *InteractionRunner) WriteBatch(ctx context.Context, events []messages.InteractionEvent) error {
	for _, event := range events {
		if err := r.Write(ctx, event); err != nil {
			return err
		}
	}
	return nil
}
