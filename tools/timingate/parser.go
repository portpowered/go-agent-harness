package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

type rawEvent struct {
	Action  string          `json:"Action"`
	Package string          `json:"Package"`
	Test    string          `json:"Test"`
	Elapsed json.RawMessage `json:"Elapsed"`
}

type packageState struct {
	pending   bool
	completed bool
}

// Parse extracts every package-level terminal timing from go test -json output.
func Parse(r io.Reader) ([]Observation, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	states := make(map[string]packageState)
	observations := make([]Observation, 0)
	lineNumber := 0

	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		event, err := decodeEvent(line)
		if err != nil {
			return nil, &MalformedInputError{Line: lineNumber, Cause: err}
		}
		if len(event.Elapsed) > 0 {
			if _, err := parseElapsed(event.Elapsed); err != nil {
				return nil, &MalformedInputError{Line: lineNumber, Cause: fmt.Errorf("elapsed: %w", err)}
			}
		}
		if event.Package == "" {
			continue
		}

		state, seen := states[event.Package]
		if event.Action == "start" {
			if seen && state.pending {
				return nil, &MissingTimingError{Package: event.Package}
			}
			states[event.Package] = packageState{pending: true}
			continue
		}

		if !seen {
			state.pending = true
		}
		if isPackageTerminal(event.Action) && event.Test == "" {
			if len(event.Elapsed) == 0 {
				return nil, &MissingTimingError{Package: event.Package, Terminal: true}
			}
			elapsed, err := parseElapsed(event.Elapsed)
			if err != nil {
				return nil, &MalformedInputError{Line: lineNumber, Cause: fmt.Errorf("elapsed: %w", err)}
			}
			observations = append(observations, Observation{Package: event.Package, Elapsed: elapsed})
			state.pending = false
			state.completed = true
			states[event.Package] = state
			continue
		}

		if !state.completed {
			state.pending = true
		}
		states[event.Package] = state
	}
	if err := scanner.Err(); err != nil {
		return nil, &MalformedInputError{Line: lineNumber, Cause: err}
	}

	pending := make([]string, 0)
	for packagePath, state := range states {
		if state.pending {
			pending = append(pending, packagePath)
		}
	}
	if len(pending) > 0 {
		sort.Strings(pending)
		return nil, &MissingTimingError{Package: pending[0]}
	}
	if len(observations) == 0 {
		return nil, &EmptyRunError{}
	}
	return observations, nil
}

func decodeEvent(line []byte) (rawEvent, error) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return rawEvent{}, fmt.Errorf("expected a JSON object")
	}
	var event rawEvent
	if err := json.Unmarshal(trimmed, &event); err != nil {
		return rawEvent{}, fmt.Errorf("decode event: %w", err)
	}
	return event, nil
}

func isPackageTerminal(action string) bool {
	switch action {
	case "pass", "fail", "skip":
		return true
	default:
		return false
	}
}

func parseElapsed(raw json.RawMessage) (time.Duration, error) {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return 0, fmt.Errorf("must be a JSON number")
	}
	seconds, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0, fmt.Errorf("must be a finite JSON number, got %q", text)
	}
	if seconds < 0 {
		return 0, fmt.Errorf("must not be negative, got %q", text)
	}
	nanoseconds := seconds * float64(time.Second)
	maxDuration := float64(time.Duration(1<<63 - 1))
	if nanoseconds > maxDuration {
		return 0, fmt.Errorf("duration is too large, got %q", text)
	}
	return time.Duration(math.Round(nanoseconds)), nil
}
