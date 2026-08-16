package main

import (
	"fmt"
	"io"
)

func WriteSuccess(w io.Writer, result Result) error {
	_, err := fmt.Fprintf(w,
		"PR-tier package-time total: %s (%d package completions across %d packages)\nBudget: %s\n",
		result.Total, result.Observations, result.PackageCount, Budget)
	return err
}

func WriteBudgetReport(w io.Writer, failure *BudgetExceededError) error {
	if _, err := fmt.Fprintln(w, "PR-tier package-time budget exceeded"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Measured total: %s\nBudget: %s\nSlowest packages:\n", failure.Total, failure.Limit); err != nil {
		return err
	}
	for index, packageTotal := range failure.Packages {
		if _, err := fmt.Fprintf(w, "  %d. %s - %s (%d %s)\n", index+1, packageTotal.Elapsed, packageTotal.Package, packageTotal.Count, completionWord(packageTotal.Count)); err != nil {
			return err
		}
	}
	return nil
}

func completionWord(count int) string {
	if count == 1 {
		return "completion"
	}
	return "completions"
}
