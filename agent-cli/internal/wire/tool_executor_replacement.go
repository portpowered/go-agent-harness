package wire

import "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"

func hasPortSwap(swaps []PortSwap, name string) bool {
	for _, swap := range swaps {
		if swap.Name == name {
			return true
		}
	}
	return false
}

func markToolExecutorReplacementIfSwapped(executor messages.ToolExecutor, swaps []PortSwap) messages.ToolExecutor {
	if !hasPortSwap(swaps, PortToolExecutor) {
		return executor
	}
	return markToolExecutorReplacement(executor)
}

// replacementToolExecutor identifies the compatibility path used by the
// legacy executor-only initializer. That API predates request-scoped tool
// capability services and intentionally supplies its own complete execution
// surface; applying the runtime catalog allowlist to it would reject fixture
// tools that are not part of the built-in catalog. New compositions should
// use PortToolService so definitions and execution remain one binding.
type replacementToolExecutor struct{ messages.ToolExecutor }

func markToolExecutorReplacement(executor messages.ToolExecutor) messages.ToolExecutor {
	if executor == nil {
		return nil
	}
	if _, marked := executor.(interface{ AllowUnadvertisedTools() bool }); marked {
		return executor
	}
	return &replacementToolExecutor{ToolExecutor: executor}
}

func (*replacementToolExecutor) AllowUnadvertisedTools() bool { return true }

func (e *replacementToolExecutor) originalToolExecutor() messages.ToolExecutor {
	if e == nil {
		return nil
	}
	return e.ToolExecutor
}
