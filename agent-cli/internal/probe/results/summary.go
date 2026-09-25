package results

import (
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

const noneLabel = "(none)"

// RenderFrictionSummary renders the deterministic human-readable friction
// report summary.
func RenderFrictionSummary(report probe.FrictionReport) string {
	var summary strings.Builder
	summary.WriteString("Probe friction report\n")
	fmt.Fprintf(&summary, "Scenarios: %d total, %d passed, %d failed, %d stuck\n", report.Total, report.Passed, report.Failed, report.Stuck)

	writeSection(&summary, "Scenario rollups:", len(report.Scenarios), func(index int) string {
		scenario := report.Scenarios[index]
		return fmt.Sprintf("%s: total=%d passed=%d failed=%d stuck=%d", scenario.Name, scenario.Total, scenario.Passed, scenario.Failed, scenario.Stuck)
	})
	writeSection(&summary, "Terminal reasons:", len(report.TerminalReasons), func(index int) string {
		reason := report.TerminalReasons[index]
		return fmt.Sprintf("%s: %d", reason.Reason, reason.Count)
	})
	writeSection(&summary, "Error classes:", len(report.ErrorClasses), func(index int) string {
		class := report.ErrorClasses[index]
		return fmt.Sprintf("%s: %d", class.Class, class.Count)
	})
	writeSection(&summary, "Expectation misses:", len(report.ExpectationMisses), func(index int) string {
		miss := report.ExpectationMisses[index]
		return fmt.Sprintf("%s: %d (scenarios: %s)", miss.Kind, miss.Count, scenarioNames(miss.Scenarios))
	})
	writeSection(&summary, "Top frictions:", len(report.TopFrictions), func(index int) string {
		friction := report.TopFrictions[index]
		return fmt.Sprintf("%s/%s: %d (scenarios: %s)", friction.Category, friction.Key, friction.Count, scenarioNames(friction.Scenarios))
	})

	status := "pass"
	if report.Failed > 0 {
		status = "fail"
	}
	fmt.Fprintf(&summary, "Health: %s\n", status)
	return summary.String()
}

// writeSection writes one titled, indented section, or "(none)" when empty.
func writeSection(summary *strings.Builder, title string, count int, line func(int) string) {
	summary.WriteString(title + "\n")
	if count == 0 {
		summary.WriteString("  " + noneLabel + "\n")
		return
	}
	for index := range count {
		summary.WriteString("  " + line(index) + "\n")
	}
}

func scenarioNames(names []string) string {
	if len(names) == 0 {
		return noneLabel
	}
	return strings.Join(names, ", ")
}
