package service

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/livedelegation"
)

// run answers one delegation.
type run struct {
	e            *executor
	value        messages.DelegationCreatedValue
	appendFailed atomic.Bool
}

func newRun(e *executor, value messages.DelegationCreatedValue) *run {
	return &run{e: e, value: value}
}

// execute runs the delegation and sends its answer as commentary. Every
// outcome is answered (a result, a failure, or a spent budget) unless ctx
// ended: a cancelled delegation was superseded or its session is over, and
// nobody is left to hear the answer.
func (r *run) execute(ctx context.Context) {
	content := r.answer(ctx)
	if ctx.Err() != nil {
		return
	}
	r.send(ctx, messages.ContextAppendCommentary, content)
}

// send delivers one append. A rejected append cannot be answered on the
// same channel (the provider reports it as an ERROR on the session), so a
// rejection only stops further progress notes of this delegation.
func (r *run) send(ctx context.Context, kind messages.ContextAppendKind, content string) {
	if err := r.e.binding.Append(ctx, messages.NewDelegationContextAppendValue(kind, r.value.ID, content)); err != nil {
		r.appendFailed.Store(true)
	}
}

// observeTool reports a tool event of this delegation to the host.
func (r *run) observeTool(event livedelegation.ToolEvent) {
	if r.e.binding.OnTool == nil {
		return
	}
	event.DelegationID = r.value.ID
	r.e.binding.OnTool(event)
}

// sendProgress delivers a quiet progress note unless an append failed.
func (r *run) sendProgress(ctx context.Context, call messages.ToolCall) {
	if !r.appendFailed.Load() {
		r.send(ctx, messages.ContextAppendThinking, progressThinking(call))
	}
}

// answer runs the nested loop and returns the text GPT-Live should speak.
func (r *run) answer(ctx context.Context) string {
	inferencer, err := r.e.backend.get(ctx)
	if err != nil {
		return r.failure(err)
	}
	limits := r.e.limits
	spent := newBudget(inferencer, limits)
	runCtx, stop := spent.withTimeBudget(ctx, r.e.binding.Scheduler)
	defer stop()
	loop, err := agentloop.New(r.loopOptions(spent)...)
	if err != nil {
		return r.failure(fmt.Errorf("create delegation loop: %w", err))
	}
	result, execErr := loop.Execute(runCtx, agentloop.NewExecuteInput(taskText(r.value, r.history())))
	final := result.FinalText()
	if exceeded := spent.exceeded(); exceeded != nil {
		r.e.logger.Warn("live delegation exceeded its budget",
			logging.Field{Key: "delegation_id", Value: r.value.ID}, logging.Field{Key: "error", Value: exceeded})
		return budgetCommentary(exceeded, final.Text)
	}
	switch final.Status {
	case agentloop.FinalTextSuccess:
		return final.Text
	case agentloop.FinalTextEmptySuccess, agentloop.FinalTextNoFinalMessage:
		return emptyCommentary
	case agentloop.FinalTextCanceled, agentloop.FinalTextFailed:
		return r.failure(firstError(final.Err, execErr))
	}
	return r.failure(firstError(final.Err, execErr))
}

// failure logs the detail of a failed delegation and returns the generic
// commentary GPT-Live speaks.
func (r *run) failure(err error) string {
	r.e.logger.Error("live delegation failed",
		logging.Field{Key: "delegation_id", Value: r.value.ID},
		logging.Field{Key: "category", Value: failureCategory(err)},
		logging.Field{Key: "error", Value: err})
	return failureCommentary(err)
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return errors.New("the backend ended without a result")
}

func (r *run) history() []messages.Message {
	if r.e.binding.History == nil {
		return nil
	}
	return r.e.binding.History()
}

func (r *run) loopOptions(spent *budget) []agentloop.Option {
	options := []agentloop.Option{
		agentloop.WithInferencer(spent),
		agentloop.WithSystemPrompt(systemPrompt(r.e.binding.Policy.Instructions)),
	}
	var definitions []messages.ToolDefinition
	if r.e.binding.Definitions != nil {
		definitions = r.e.binding.Definitions()
	}
	if r.e.binding.Tools == nil || len(definitions) == 0 {
		return append(options, agentloop.WithToolExecutionDisabled())
	}
	tools := delegationTools{inner: r.e.binding.Tools, lock: r.e.binding.ToolLock, progress: r.sendProgress, observe: r.observeTool}
	return append(options, agentloop.WithToolExecutor(tools), agentloop.WithTools(definitions))
}
