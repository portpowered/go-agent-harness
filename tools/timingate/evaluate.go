package main

import (
	"fmt"
	"io"
	"sort"
	"time"
)

// Measure parses timing evidence and evaluates it against the shared budget.
func Measure(r io.Reader) (Result, error) {
	observations, err := Parse(r)
	if err != nil {
		return Result{}, err
	}
	return Evaluate(observations)
}

// Evaluate sums every package completion and builds the slowest-package ordering.
func Evaluate(observations []Observation) (Result, error) {
	if len(observations) == 0 {
		return Result{}, &EmptyRunError{}
	}

	result := Result{Observations: len(observations)}
	byPackage := make(map[string]PackageTotal)
	maxDuration := time.Duration(1<<63 - 1)
	for index, observation := range observations {
		if observation.Package == "" {
			return Result{}, &MalformedInputError{Line: index + 1, Cause: fmt.Errorf("package name is empty")}
		}
		if observation.Elapsed < 0 {
			return Result{}, &MalformedInputError{Line: index + 1, Cause: fmt.Errorf("elapsed duration is negative")}
		}
		if observation.Elapsed > maxDuration-result.Total {
			return Result{}, &MalformedInputError{Line: index + 1, Cause: fmt.Errorf("total duration overflows time.Duration")}
		}
		result.Total += observation.Elapsed

		packageTotal := byPackage[observation.Package]
		if observation.Elapsed > maxDuration-packageTotal.Elapsed {
			return Result{}, &MalformedInputError{Line: index + 1, Cause: fmt.Errorf("package duration overflows time.Duration")}
		}
		packageTotal.Package = observation.Package
		packageTotal.Elapsed += observation.Elapsed
		packageTotal.Count++
		byPackage[observation.Package] = packageTotal
	}

	result.PackageCount = len(byPackage)
	result.Packages = make([]PackageTotal, 0, len(byPackage))
	for _, packageTotal := range byPackage {
		result.Packages = append(result.Packages, packageTotal)
	}
	sort.Slice(result.Packages, func(i, j int) bool {
		if result.Packages[i].Elapsed == result.Packages[j].Elapsed {
			return result.Packages[i].Package < result.Packages[j].Package
		}
		return result.Packages[i].Elapsed > result.Packages[j].Elapsed
	})

	if result.Total > Budget {
		return result, &BudgetExceededError{
			Total:        result.Total,
			Limit:        Budget,
			Observations: result.Observations,
			Packages:     append([]PackageTotal(nil), result.Packages...),
		}
	}
	return result, nil
}
