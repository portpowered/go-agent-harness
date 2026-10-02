package strict

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	publicreplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

// replayLoop is the part of [agentloop.AgentLoop] the offline replay drives.
type replayLoop interface {
	Run(ctx context.Context) error
	Deltas() *messages.TypedBuffer[messages.StreamMessage]
	Send(ctx context.Context, msg []messages.Message) error
	SendAudioInput(ctx context.Context, pcm []byte) error
	SendSessionEventWaiting(ctx context.Context, msg messages.StreamMessage) error
}

var _ replayLoop = (*agentloop.AgentLoop)(nil)

type coreRuntime struct {
	loop    replayLoop
	actions []replayInputAction
}

type runState struct {
	done chan struct{}
	err  error
}

func (r *coreRuntime) Run(ctx context.Context, out io.Writer) error {
	if ctx == nil {
		return errors.New("offline replay requires a context")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	state, readCtx, stopRead := r.start(runCtx)
	defer stopRead()
	if len(r.actions) == 0 {
		return r.abort(cancel, state, fmt.Errorf("%w: recorded provider session has no replayable user input", publicreplay.ErrBundleIncomplete))
	}
	if err := r.awaitSessionOpen(readCtx, cancel, state); err != nil {
		return err
	}
	for _, action := range r.actions {
		if err := r.runAction(runCtx, readCtx, cancel, state, action, out); err != nil {
			return r.abort(cancel, state, err)
		}
	}
	cancel()
	<-state.done
	return loopRunError(state.err)
}

func (r *coreRuntime) start(runCtx context.Context) (*runState, context.Context, context.CancelFunc) {
	state := &runState{done: make(chan struct{})}
	readCtx, stopRead := context.WithCancel(runCtx)
	go func() {
		state.err = r.loop.Run(runCtx)
		close(state.done)
		stopRead()
	}()
	return state, readCtx, stopRead
}

func (r *coreRuntime) abort(cancel context.CancelFunc, state *runState, primary error) error {
	cancel()
	<-state.done
	if loopErr := loopRunError(state.err); loopErr != nil {
		return loopErr
	}
	return primary
}

func (r *coreRuntime) awaitSessionOpen(readCtx context.Context, cancel context.CancelFunc, state *runState) error {
	for {
		message, err := r.loop.Deltas().ReadContext(readCtx)
		if err == nil {
			if message.Type == messages.StreamTypeSessionOpen {
				return nil
			}
			continue
		}
		cancel()
		<-state.done
		if loopErr := loopRunError(state.err); loopErr != nil {
			return fmt.Errorf("offline core loop: %w", loopErr)
		}
		return fmt.Errorf("%w: provider session did not open: %w", publicreplay.ErrBundleIncomplete, err)
	}
}

// runAction sends one recorded input and forwards the model deltas until
// the recorded number of responses has ended. Deltas are drained while the
// input is still being sent: must-deliver deltas wait for outbox capacity,
// so a provider that answers mid-input would otherwise fill the outbox and
// stall the input behind it.
func (r *coreRuntime) runAction(runCtx, readCtx context.Context, cancel context.CancelFunc, state *runState, action replayInputAction, out io.Writer) error {
	if action.responseEnds <= 0 {
		return fmt.Errorf("%w: final recorded input has no response.done completion boundary", publicreplay.ErrBundleIncomplete)
	}
	inputDone := make(chan error, 1)
	go func() { inputDone <- r.runInput(readCtx, action) }()
	ended := 0
	for ended < action.responseEnds || inputDone != nil {
		message, err := r.readActionDelta(readCtx, &inputDone)
		if errors.Is(err, errInputSent) {
			continue
		}
		if err != nil && inputDone != nil {
			// The input failed, or the read ended while the input was still
			// being sent: the action cannot complete.
			return err
		}
		if err != nil {
			ended += r.drainProviderEnds()
			if ended >= action.responseEnds {
				break
			}
			return r.waitForResponse(readCtx, runCtx, cancel, state)
		}
		if isProviderMessageEnd(message) {
			ended++
		}
		if err := writeModelDelta(out, message); err != nil {
			return err
		}
	}
	return nil
}

// errInputSent reports that the action's input finished sending; it is not
// a failure.
var errInputSent = errors.New("replay input sent")

// readActionDelta reads the next delta while also observing the action's
// input. When the input finishes, *inputDone is cleared and errInputSent is
// returned, or the input's error when it failed.
func (r *coreRuntime) readActionDelta(readCtx context.Context, inputDone *chan error) (messages.StreamMessage, error) {
	if *inputDone == nil {
		return r.loop.Deltas().ReadContext(readCtx)
	}
	select {
	case err := <-*inputDone:
		if err != nil {
			return messages.StreamMessage{}, err
		}
		*inputDone = nil
		return messages.StreamMessage{}, errInputSent
	case message := <-r.loop.Deltas().Chan():
		return message, nil
	case <-readCtx.Done():
		return messages.StreamMessage{}, readCtx.Err()
	}
}

func (r *coreRuntime) runInput(ctx context.Context, action replayInputAction) error {
	if action.text != "" {
		return r.loop.Send(ctx, []messages.Message{messages.NewTextMessage(messages.RoleUser, action.text)})
	}
	for _, pcm := range action.audio {
		if err := r.loop.SendAudioInput(ctx, pcm); err != nil {
			return err
		}
	}
	if len(action.audio) > 0 {
		return r.loop.SendSessionEventWaiting(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	}
	return nil
}

func (r *coreRuntime) drainProviderEnds() int {
	ended := 0
	for {
		buffered, ok := r.loop.Deltas().Read()
		if !ok {
			return ended
		}
		if isProviderMessageEnd(buffered) {
			ended++
			return ended
		}
	}
}

func (r *coreRuntime) waitForResponse(readCtx context.Context, runCtx context.Context, cancel context.CancelFunc, state *runState) error {
	select {
	case <-state.done:
		if loopErr := loopRunError(state.err); loopErr != nil {
			return fmt.Errorf("offline core loop: %w", loopErr)
		}
		return fmt.Errorf("offline core loop stopped before response boundary")
	case <-runCtx.Done():
		cancel()
		return runCtx.Err()
	case <-readCtx.Done():
		return readCtx.Err()
	}
}

func writeModelDelta(out io.Writer, message messages.StreamMessage) error {
	if out == nil || message.ActorID != messages.Model {
		return nil
	}
	text, ok := message.Value.(*messages.TextDeltaValue)
	if !ok {
		return nil
	}
	if _, err := io.WriteString(out, text.Content); err != nil {
		return fmt.Errorf("write offline core output: %w", err)
	}
	return nil
}

func loopRunError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func isProviderMessageEnd(message messages.StreamMessage) bool {
	return message.Type == messages.StreamTypeMessageEnd && (message.ActorID == messages.Model || message.ResponseID != "")
}
