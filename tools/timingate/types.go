package main

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrMalformedInput = errors.New("malformed timing input")
	ErrMissingTiming  = errors.New("missing package timing")
	ErrEmptyRun       = errors.New("empty timing run")
	ErrOverBudget     = errors.New("package-time budget exceeded")
)

// MalformedInputError identifies an event that cannot be trusted as timing evidence.
type MalformedInputError struct {
	Line  int
	Cause error
}

func (e *MalformedInputError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("malformed timing input on line %d: %v", e.Line, e.Cause)
	}
	return fmt.Sprintf("malformed timing input: %v", e.Cause)
}

func (e *MalformedInputError) Is(target error) bool { return target == ErrMalformedInput }
func (e *MalformedInputError) Unwrap() error        { return e.Cause }

// MissingTimingError identifies a package that did not provide complete timing evidence.
type MissingTimingError struct {
	Package  string
	Terminal bool
}

func (e *MissingTimingError) Error() string {
	if e.Terminal {
		return fmt.Sprintf("package %q has a terminal record without an explicit elapsed duration", e.Package)
	}
	return fmt.Sprintf("package %q did not produce a timed terminal record", e.Package)
}

func (e *MissingTimingError) Is(target error) bool { return target == ErrMissingTiming }

// EmptyRunError prevents a run with no package completions from passing as fast.
type EmptyRunError struct{}

func (e *EmptyRunError) Error() string        { return "timing input contained no package completions" }
func (e *EmptyRunError) Is(target error) bool { return target == ErrEmptyRun }

// Observation is one package completion from one executed test scope.
type Observation struct {
	Package string
	Elapsed time.Duration
}

// PackageTotal is the aggregate of all observations for one import path.
type PackageTotal struct {
	Package string
	Elapsed time.Duration
	Count   int
}

// Result contains both the total and deterministic package diagnostics.
type Result struct {
	Total        time.Duration
	Observations int
	PackageCount int
	Packages     []PackageTotal
}

// BudgetExceededError carries the data needed to render an actionable report.
type BudgetExceededError struct {
	Total        time.Duration
	Limit        time.Duration
	Observations int
	Packages     []PackageTotal
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf("PR-tier package-time total %s exceeds budget %s", e.Total, e.Limit)
}

func (e *BudgetExceededError) Is(target error) bool { return target == ErrOverBudget }
