package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
)

// go test -json lines can carry large test output; allow up to 16 MiB.
const (
	eventBufferInitial = 64 << 10
	eventBufferMax     = 16 << 20
)

func verifyEvents(name string, required []string, input io.Reader) error {
	report, err := parseEvents(name, required, input)
	if err != nil {
		return err
	}
	return report.verificationError()
}

func parseEvents(name string, required []string, input io.Reader) (*eventReport, error) {
	requiredSet := make(map[string]struct{}, len(required))
	for _, testName := range required {
		requiredSet[testName] = struct{}{}
	}
	results := make(map[string]testResult, len(required))
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, eventBufferInitial), eventBufferMax)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event testEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("decode go test JSON event on line %d: %w", lineNumber, err)
		}
		if _, ok := requiredSet[event.Test]; !ok {
			continue
		}
		result := results[event.Test]
		result.seen = true
		result.output += event.Output
		switch event.Action {
		case "pass":
			result.passed++
		case "skip":
			result.skipped = true
		case "fail":
			result.failed = true
		}
		results[event.Test] = result
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read go test JSON output: %w", err)
	}

	report := &eventReport{required: required, results: results}
	report.failure = classifyResults(name, required, results)
	return report, nil
}

func classifyResults(name string, required []string, results map[string]testResult) requiredTestsError {
	failure := requiredTestsError{name: name}
	for _, testName := range required {
		result := results[testName]
		switch {
		case result.skipped:
			failure.Skipped = append(failure.Skipped, testName)
		case result.failed:
			failure.Failed = append(failure.Failed, testName)
		case result.passed == 0:
			failure.Missing = append(failure.Missing, testName)
		case result.passed != 1:
			failure.Duplicate = append(failure.Duplicate, testName)
		}
	}
	return failure
}

func (r *eventReport) verificationError() error {
	if r == nil || r.failure.empty() {
		return nil
	}
	return &r.failure
}

func (r *eventReport) retryVerificationError(testName string) error {
	if r == nil {
		return errors.New("race retry produced no event report")
	}
	if !slices.Contains(r.required, testName) {
		return fmt.Errorf("race retry selected non-required test %q", testName)
	}
	for _, requiredTest := range r.required {
		if requiredTest == testName {
			continue
		}
		if r.results[requiredTest].seen {
			return fmt.Errorf("race retry ran unexpected required test %q", requiredTest)
		}
	}
	result := r.results[testName]
	switch {
	case result.skipped:
		return fmt.Errorf("race retry skipped required test %q", testName)
	case result.failed:
		return fmt.Errorf("race retry failed required test %q", testName)
	case result.passed == 0:
		return fmt.Errorf("race retry did not complete required test %q", testName)
	case result.passed != 1:
		return fmt.Errorf("race retry completed required test %q more than once", testName)
	default:
		return nil
	}
}

func (f *requiredTestsError) empty() bool {
	return len(f.Missing) == 0 && len(f.Skipped) == 0 && len(f.Failed) == 0 && len(f.Duplicate) == 0
}

func (f *requiredTestsError) Error() string {
	var parts []string
	appendPart := func(label string, values []string) {
		if len(values) == 0 {
			return
		}
		ordered := append([]string(nil), values...)
		sort.Strings(ordered)
		parts = append(parts, fmt.Sprintf("%s: %s", label, strings.Join(ordered, ", ")))
	}
	appendPart("missing required tests", f.Missing)
	appendPart("skipped required tests", f.Skipped)
	appendPart("failed required tests", f.Failed)
	appendPart("required tests completed more than once", f.Duplicate)
	return f.name + " failed; " + strings.Join(parts, "; ")
}
