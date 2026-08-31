package testtimeout

import (
	"fmt"
	"io"
	"time"
)

// BudgetSummary describes elapsed wall time relative to a finite command
// budget. RemainingPercent is measured against the configured budget, so a
// run that consumes 70% has 30% remaining headroom.
type BudgetSummary struct {
	Elapsed          time.Duration
	Budget           time.Duration
	Remaining        time.Duration
	ConsumedPercent  float64
	RemainingPercent float64
}

// CalculateBudgetSummary computes the values printed for a budgeted command.
// Elapsed is allowed to exceed Budget so timeout cleanup and over-budget
// failures remain visible instead of being clamped to 100%.
func CalculateBudgetSummary(elapsed, budget time.Duration) (BudgetSummary, error) {
	if elapsed < 0 {
		return BudgetSummary{}, fmt.Errorf("budget summary requires non-negative elapsed time, got %s", elapsed)
	}
	if budget <= 0 {
		return BudgetSummary{}, fmt.Errorf("budget summary requires a positive finite budget, got %s", budget)
	}

	remaining := budget - elapsed
	return BudgetSummary{
		Elapsed:          elapsed,
		Budget:           budget,
		Remaining:        remaining,
		ConsumedPercent:  float64(elapsed) * 100 / float64(budget),
		RemainingPercent: float64(remaining) * 100 / float64(budget),
	}, nil
}

// WriteBudgetReport writes a stable, human-readable budget summary. The
// classification is supplied by the command boundary so timeout termination
// cannot be confused with a test or coverage assertion failure.
func WriteBudgetReport(w io.Writer, summary BudgetSummary, classification string) error {
	if _, err := fmt.Fprintln(w, "Agent CLI coverage budget summary"); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "Elapsed: %s\nConfigured timeout: %s\nConsumed: %.1f%%\nRemaining headroom: %s (%.1f%%)\nClassification: %s\n", formatBudgetDuration(summary.Elapsed), formatBudgetDuration(summary.Budget), summary.ConsumedPercent, formatBudgetDuration(summary.Remaining), summary.RemainingPercent, classification)
	return err
}

func formatBudgetDuration(duration time.Duration) string {
	return duration.Round(time.Millisecond).String()
}
