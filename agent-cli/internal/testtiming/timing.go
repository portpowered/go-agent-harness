package testtiming

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

const uncategorizedPath = "uncategorized"

// Event is the subset of go test -json output needed for timing reports.
type Event struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test,omitempty"`
	Output  string  `json:"Output,omitempty"`
	Elapsed float64 `json:"Elapsed,omitempty"`
}

// Entry records one package or test timing observation from go test -json.
type Entry struct {
	Package  string
	Test     string
	Action   string
	Elapsed  time.Duration
	Category string
}

// Summary contains package-level and test-level runtime evidence.
type Summary struct {
	Packages []Entry
	Tests    []Entry
}

// Parse consumes go test -json output and extracts package and test timings.
func Parse(r io.Reader) (Summary, error) {
	var summary Summary
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Package == "" || event.Elapsed <= 0 {
			continue
		}
		if !isTerminalAction(event.Action) {
			continue
		}

		entry := Entry{
			Package:  event.Package,
			Test:     event.Test,
			Action:   event.Action,
			Elapsed:  secondsToDuration(event.Elapsed),
			Category: Classify(event.Package, event.Test),
		}
		if event.Test == "" {
			summary.Packages = append(summary.Packages, entry)
			continue
		}
		summary.Tests = append(summary.Tests, entry)
	}
	if err := scanner.Err(); err != nil {
		return Summary{}, err
	}

	sortEntries(summary.Packages)
	sortEntries(summary.Tests)
	return summary, nil
}

// Classify names the likely runtime path behind a package or test.
func Classify(pkg, test string) string {
	name := strings.ToLower(pkg + " " + test)
	switch {
	case strings.Contains(name, "replay") || strings.Contains(name, "session"):
		return "session replay or session fixture"
	case strings.Contains(name, "integration"):
		return "integration fixture"
	case strings.Contains(name, "provider") || strings.Contains(name, "grok") || strings.Contains(name, "openrouter"):
		return "provider setup"
	case strings.Contains(name, "shell") || strings.Contains(name, "exectool") || strings.Contains(name, " exec "):
		return "shell execution"
	case strings.Contains(name, "filesystem") || strings.Contains(name, "file"):
		return "filesystem traversal"
	case strings.Contains(name, "sleep") || strings.Contains(name, "timeout") || strings.Contains(name, "retry") || strings.Contains(name, "interrupt"):
		return "sleep, retry, or cancellation wait"
	default:
		return uncategorizedPath
	}
}

// WriteReport emits a deterministic timing report suitable for local diagnostics.
func WriteReport(w io.Writer, summary Summary, preflightDuration, suiteDuration time.Duration, top int, testExitCode int) error {
	if top <= 0 {
		top = 20
	}

	if _, err := fmt.Fprintln(w, "Agent CLI test timing evidence"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "preflight no-test build/cache duration: %s\n", roundDuration(preflightDuration)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "suite wall duration: %s\n", roundDuration(suiteDuration)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "go test exit code: %d\n\n", testExitCode); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "Environment/product split: the preflight line runs `go test -run ^$` first so dependency download, package discovery, compilation, and first-run cache warmup are visible separately from the product test timings below. The suite run uses `-count=1` so reported test timings are not hidden by Go test result caching."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "No timing artifact is written by default; copy this console output only after checking it contains no local secrets."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}

	if err := writeEntries(w, "Slowest packages", summary.Packages, top); err != nil {
		return err
	}
	if err := writeEntries(w, "Slowest tests", summary.Tests, top); err != nil {
		return err
	}
	return writeCategories(w, summary)
}

func writeEntries(w io.Writer, title string, entries []Entry, top int) error {
	if _, err := fmt.Fprintln(w, title+":"); err != nil {
		return err
	}
	if len(entries) == 0 {
		_, err := fmt.Fprintln(w, "  (no timing events)")
		return err
	}
	limit := top
	if len(entries) < limit {
		limit = len(entries)
	}
	for i := 0; i < limit; i++ {
		entry := entries[i]
		name := entry.Package
		if entry.Test != "" {
			name += " " + entry.Test
		}
		if _, err := fmt.Fprintf(w, "  %2d. %-8s %-38s %s [%s]\n", i+1, roundDuration(entry.Elapsed), name, entry.Action, entry.Category); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w)
	return err
}

func writeCategories(w io.Writer, summary Summary) error {
	type aggregate struct {
		Count   int
		Elapsed time.Duration
	}
	byCategory := make(map[string]aggregate)
	for _, entry := range summary.Tests {
		agg := byCategory[entry.Category]
		agg.Count++
		agg.Elapsed += entry.Elapsed
		byCategory[entry.Category] = agg
	}

	categories := make([]string, 0, len(byCategory))
	for category := range byCategory {
		categories = append(categories, category)
	}
	sort.Slice(categories, func(i, j int) bool {
		left := byCategory[categories[i]]
		right := byCategory[categories[j]]
		if left.Elapsed == right.Elapsed {
			return categories[i] < categories[j]
		}
		return left.Elapsed > right.Elapsed
	})

	if _, err := fmt.Fprintln(w, "Named runtime paths from test names/packages:"); err != nil {
		return err
	}
	if len(categories) == 0 {
		_, err := fmt.Fprintln(w, "  (no test-level timing events)")
		return err
	}
	for _, category := range categories {
		agg := byCategory[category]
		if _, err := fmt.Fprintf(w, "  - %s: %s across %d test(s)\n", category, roundDuration(agg.Elapsed), agg.Count); err != nil {
			return err
		}
	}
	return nil
}

func isTerminalAction(action string) bool {
	switch action {
	case "pass", "fail", "skip":
		return true
	default:
		return false
	}
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Elapsed == entries[j].Elapsed {
			if entries[i].Package == entries[j].Package {
				return entries[i].Test < entries[j].Test
			}
			return entries[i].Package < entries[j].Package
		}
		return entries[i].Elapsed > entries[j].Elapsed
	})
}

func secondsToDuration(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}

func roundDuration(d time.Duration) time.Duration {
	return d.Round(time.Millisecond)
}
