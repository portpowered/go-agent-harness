package parity

import (
	"fmt"
	"strings"
)

// FormatReport renders findings in their supplied order as a deterministic,
// human-readable parity diagnostic. It always ends with one newline.
func FormatReport(differences []Difference) string {
	var report strings.Builder
	findingWord := "differences"
	if len(differences) == 1 {
		findingWord = "difference"
	}
	fmt.Fprintf(&report, "Parity comparison: %d %s", len(differences), findingWord)
	if len(differences) == 0 {
		report.WriteString(" (projections agree)\n")
		return report.String()
	}
	report.WriteByte('\n')

	for index, difference := range differences {
		fmt.Fprintf(&report, "  %d. %s: expected %s; actual %s\n",
			index+1, difference.Path, difference.Expected, difference.Actual)
	}
	return report.String()
}
