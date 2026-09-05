package cli

import (
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
)

// validateLoopFlagRanges rejects an explicit --max-iterations or
// --context-pressure-threshold value that --loop mode cannot act on
// meaningfully, instead of silently discarding it.
//
// Before this check: `--max-iterations -5` (or 0) fell into
// RunIterativeLoop's "maxIter <= 0 -> default to 5" fallback with no
// warning at all, so the value the user typed was thrown away in favor of
// the default they never asked for. `--context-pressure-threshold -3` is
// likewise indistinguishable from never having passed the flag: the
// context-pressure notifier only wires up when the threshold is > 0, so a
// negative value silently disables the feature the user just tried to
// configure. `--context-pressure-threshold 5000` is accepted just as
// silently in the other direction: the loop compares this value against a
// 0-1 fraction of context used, so anything above 1 can never trigger --
// the notifier is wired up (since 5000 > 0) but permanently inert. In every
// case the user believed they configured something; they hadn't.
func validateLoopFlagRanges(loopFlags *flags.LoopFlags) error {
	if loopFlags.MaxIterations <= 0 {
		return fmt.Errorf("--max-iterations must be a positive integer, got %d", loopFlags.MaxIterations)
	}
	if loopFlags.ContextPressureThreshold <= 0 || loopFlags.ContextPressureThreshold > 1 {
		return fmt.Errorf("--context-pressure-threshold must be greater than 0 and at most 1 (a fraction of the context window), got %g", loopFlags.ContextPressureThreshold)
	}
	return nil
}
