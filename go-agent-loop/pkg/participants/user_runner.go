package participants

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

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
