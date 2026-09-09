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

type coreRuntime struct {
	loop    *agentloop.AgentLoop
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

func (r *coreRuntime) runAction(runCtx, readCtx context.Context, cancel context.CancelFunc, state *runState, action replayInputAction, out io.Writer) error {
	if err := r.runInput(runCtx, action); err != nil {
		return err
	}
	ended := 0
	for ended < action.responseEnds {
		message, err := r.loop.Deltas().ReadContext(readCtx)
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
		return r.loop.SendSessionEvent(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
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
