package testtimeout

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestFormatBudgetDuration(t *testing.T) {
	if got := formatBudgetDuration(7*time.Second + 250*time.Millisecond); got != "7.25s" {
		t.Fatalf("formatBudgetDuration = %q, want 7.25s", got)
	}
}

func TestCalculateBudgetSummaryFormatsHeadroom(t *testing.T) {
	summary, err := CalculateBudgetSummary(7*time.Second+250*time.Millisecond, 10*time.Second)
	if err != nil {
		t.Fatalf("CalculateBudgetSummary: %v", err)
	}
	if summary.Remaining != 2*time.Second+750*time.Millisecond {
		t.Fatalf("remaining = %s, want 2.75s", summary.Remaining)
	}
	if summary.ConsumedPercent != 72.5 || summary.RemainingPercent != 27.5 {
		t.Fatalf("percentages = %.1f/%.1f, want 72.5/27.5", summary.ConsumedPercent, summary.RemainingPercent)
	}

	var output bytes.Buffer
	if err := WriteBudgetReport(&output, summary, "success"); err != nil {
		t.Fatalf("WriteBudgetReport: %v", err)
	}
	for _, want := range []string{
		"Elapsed: 7.25s",
		"Configured timeout: 10s",
		"Consumed: 72.5%",
		"Remaining headroom: 2.75s (27.5%)",
		"Classification: success",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("budget report missing %q:\n%s", want, output.String())
		}
	}
}

func TestCalculateBudgetSummaryRetainsOverBudgetFailure(t *testing.T) {
	summary, err := CalculateBudgetSummary(11*time.Second, 10*time.Second)
	if err != nil {
		t.Fatalf("CalculateBudgetSummary: %v", err)
	}
	if summary.Remaining != -time.Second || summary.ConsumedPercent != 110 || summary.RemainingPercent != -10 {
		t.Fatalf("over-budget summary = %+v, want 1s over and -10%% headroom", summary)
	}
}

func TestCalculateBudgetSummaryRejectsInvalidDurations(t *testing.T) {
	for name, durations := range map[string][2]time.Duration{
		"negative elapsed": {-time.Second, time.Second},
		"zero budget":      {time.Second, 0},
		"negative budget":  {time.Second, -time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CalculateBudgetSummary(durations[0], durations[1]); err == nil {
				t.Fatal("CalculateBudgetSummary returned nil error")
			}
		})
	}
}
