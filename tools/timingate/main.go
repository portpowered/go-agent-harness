package main

import (
	"errors"
	"fmt"
	"io"
	"os"
)

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(input io.Reader, output io.Writer) error {
	result, err := Measure(input)
	if err == nil {
		return WriteSuccess(output, result)
	}

	var budgetError *BudgetExceededError
	if errors.As(err, &budgetError) {
		if reportErr := WriteBudgetReport(output, budgetError); reportErr != nil {
			return reportErr
		}
	}
	return err
}
