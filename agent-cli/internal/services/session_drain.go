// This file contains session output draining, close dispatch, stop decisions, terminal formatting, and shutdown error handling.
package services

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func writeSessionReplayMessage(out io.Writer, msg messages.StreamMessage) error {
	switch v := msg.Value.(type) {
	case *messages.TextDeltaValue:
		_, err := fmt.Fprint(out, v.Content)
		return err
	case *messages.TranscriptDeltaValue:
		_, err := fmt.Fprint(out, v.Text)
		return err
	case *messages.SessionCloseValue:
		if v.Reason != "" {
			if _, err := fmt.Fprintf(out, "\n[session closed: %s]\n", v.Reason); err != nil {
				return err
			}
		}
		if fields := sessionTerminalFields(v.Classification, v.TerminalReason, v.TerminalProvenance, v.OutputState); fields != "" {
			_, err := fmt.Fprintf(out, "[session terminal: %s]\n", fields)
			return err
		}
	case *messages.ErrorValue:
		if v.IsNonTerminal() {
			return nil
		}
		fields := sessionTerminalFields(v.Classification, v.TerminalReason, v.TerminalProvenance, v.OutputState)
		if v.Message != "" {
			if fields != "" {
				return fmt.Errorf("session error: %s [%s]", v.Message, fields)
			}
			return fmt.Errorf("session error: %s", v.Message)
		}
		if fields != "" {
			return fmt.Errorf("session error [%s]", fields)
		}
		return fmt.Errorf("session error")
	}
	return nil
}

func isTerminalErrorMessage(msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeError {
		return false
	}
	value, ok := msg.Value.(*messages.ErrorValue)
	return !ok || value.IsTerminal()
}

func sessionTerminalFields(classification string, reason messages.TerminalReason, provenance messages.TerminalProvenance, outputState messages.TerminalOutputState) string {
	var fields []string
	if classification != "" {
		fields = append(fields, "classification="+classification)
	}
	if reason != "" {
		fields = append(fields, "terminal_reason="+string(reason))
	}
	if provenance != "" {
		fields = append(fields, "terminal_provenance="+string(provenance))
	}
	if outputState != "" {
		fields = append(fields, "output_state="+string(outputState))
	}
	return strings.Join(fields, " ")
}

func drainSessionLoopMessagesUntilIdle(out io.Writer, loop *agentloop.AgentLoop, idleDelay time.Duration, obs *sessionProgressObserver) error {
	if idleDelay <= 0 {
		return drainSessionLoopMessages(out, loop, obs)
	}

	idle := time.NewTimer(idleDelay)
	defer idle.Stop()
	for {
		select {
		case msg := <-loop.Deltas().Chan():
			if obs != nil {
				obs.observe(msg)
			}
			if err := writeSessionReplayMessage(out, msg); err != nil {
				return err
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(idleDelay)
		case <-idle.C:
			return nil
		}
	}
}

func shouldStopSessionLoop(msg messages.StreamMessage, opts sessionLoopOptions, closeSent bool) bool {
	if msg.Type == messages.StreamTypeMessageEnd && opts.observer != nil && opts.observer.hasTerminalToolContinuationFailure() {
		return true
	}
	if opts.CloseAfterOpen {
		return closeSent && msg.Type == messages.StreamTypeSessionClose
	}
	if opts.WaitForClose {
		return msg.Type == messages.StreamTypeSessionClose
	}
	switch msg.Type {
	case messages.StreamTypeMessageEnd:
		if opts.observer != nil && !opts.observer.lastMessageEndAdmitted() {
			return false
		}
		if opts.observer != nil && opts.observer.hasToolLifecycleObligation() {
			return false
		}
		return true
	case messages.StreamTypeTextEnd:
		if opts.observer != nil && opts.observer.hasToolLifecycleObligation() {
			return false
		}
		return true
	case messages.StreamTypeSessionClose:
		return true
	default:
		return false
	}
}

func drainSessionLoopMessages(out io.Writer, loop *agentloop.AgentLoop, obs *sessionProgressObserver) error {
	for {
		msg, ok := loop.Deltas().Read()
		if !ok {
			return nil
		}
		if obs != nil {
			obs.observe(msg)
		}
		if err := writeSessionReplayMessage(out, msg); err != nil {
			return err
		}
	}
}

func drainSessionLoopMessagesUntilQuiet(out io.Writer, loop *agentloop.AgentLoop, quiet time.Duration, obs *sessionProgressObserver) error {
	timer := time.NewTimer(quiet)
	defer timer.Stop()

	for {
		select {
		case msg, ok := <-loop.Deltas().Chan():
			if !ok {
				return nil
			}
			if obs != nil {
				obs.observe(msg)
			}
			if err := writeSessionReplayMessage(out, msg); err != nil {
				return err
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(quiet)
		case <-timer.C:
			return nil
		}
	}
}

func sendSessionClose(ctx context.Context, loop *agentloop.AgentLoop) error {
	msg := messages.Message{
		Role: messages.RoleUser,
		ContentParts: []messages.ContentPart{
			messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypeSessionClose},
		},
	}
	if err := loop.Send(ctx, []messages.Message{msg}); err != nil {
		return fmt.Errorf("close session loop: %w", err)
	}
	return nil
}

func wrapSessionPhaseError(phase string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", phase, err)
}
