package fleet

import (
	"context"
	"fmt"
	"sync"
)

// EntryOutcome is the result returned by one fleet entry executor. An error
// belongs to that entry: it is recorded as a failed result and does not stop
// unrelated entries from running.
type EntryOutcome struct {
	Pass bool
	Err  error
}

// EntryExecutor executes one explicit manifest entry. The entry is already
// validated by Execute, and callers may use its full coordinates to select a
// transport or fixture.
type EntryExecutor func(context.Context, Entry) (EntryOutcome, error)

// EntryResult is the durable, coordinate-complete result for one manifest
// entry. ID is the stable key used to reconcile results with the manifest.
type EntryResult struct {
	ID           string    `json:"id"`
	ScenarioID   string    `json:"scenario_id"`
	ScenarioName string    `json:"scenario_name,omitempty"`
	ScenarioPath string    `json:"scenario_path"`
	Transport    Transport `json:"transport"`
	RepeatIndex  int       `json:"repeat_index"`
	Pass         bool      `json:"pass"`
	Error        string    `json:"error,omitempty"`
}

// Execution contains one result for every entry in the validated manifest.
// Results retain manifest order even though execution itself is concurrent.
type Execution struct {
	Results []EntryResult
}

// Result is the aggregate fleet result suitable for JSON output. Entries are
// coordinate-complete and are counted from the same slice as the summary, so
// total, passed, and failed cannot describe a different set of work.
type Result struct {
	Total   int           `json:"total"`
	Passed  int           `json:"passed"`
	Failed  int           `json:"failed"`
	Status  string        `json:"status"`
	Entries []EntryResult `json:"entries"`
}

// Execute runs every manifest entry with at most manifest.Concurrency calls
// to the supplied executor in flight. Executor errors are represented as
// failed entry results; only invalid setup (or a missing executor) returns an
// error before execution starts.
func Execute(ctx context.Context, manifest Manifest, executor EntryExecutor) (Execution, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := manifest.Validate(); err != nil {
		return Execution{}, fmt.Errorf("validate fleet before execution: %w", err)
	}
	if executor == nil {
		return Execution{}, fmt.Errorf("fleet execution requires an entry executor")
	}

	results := make([]EntryResult, len(manifest.Entries))
	jobs := make(chan int, len(manifest.Entries))
	for index := range manifest.Entries {
		jobs <- index
	}
	close(jobs)

	workerCount := manifest.Concurrency
	if workerCount > len(manifest.Entries) {
		workerCount = len(manifest.Entries)
	}
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func() {
			defer workers.Done()
			for index := range jobs {
				entry := manifest.Entries[index]
				outcome, execErr := executeEntry(ctx, executor, entry)
				results[index] = entryResult(entry, outcome, execErr)
			}
		}()
	}
	workers.Wait()

	return Execution{Results: results}, nil
}

// Run is a function-shaped alias for Execute.
func Run(ctx context.Context, manifest Manifest, executor EntryExecutor) (Execution, error) {
	return Execute(ctx, manifest, executor)
}

func executeEntry(ctx context.Context, executor EntryExecutor, entry Entry) (outcome EntryOutcome, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("fleet entry %q executor panicked: %v", entry.ID, recovered)
			outcome = EntryOutcome{}
		}
	}()
	return executor(ctx, entry)
}

func entryResult(entry Entry, outcome EntryOutcome, execErr error) EntryResult {
	result := EntryResult{
		ID:           entry.ID,
		ScenarioID:   entry.ScenarioID,
		ScenarioName: entry.ScenarioName,
		ScenarioPath: entry.ScenarioPath,
		Transport:    entry.Transport,
		RepeatIndex:  entry.RepeatIndex,
		Pass:         outcome.Pass,
	}
	if outcome.Err != nil {
		result.Pass = false
		result.Error = outcome.Err.Error()
	}
	if execErr != nil {
		result.Pass = false
		result.Error = execErr.Error()
	}
	return result
}

// Result aggregates an execution into a JSON-ready fleet result.
func (e Execution) Result() Result {
	result := Result{
		Entries: append([]EntryResult(nil), e.Results...),
		Status:  "fail",
	}
	result.Total = len(result.Entries)
	for _, entry := range result.Entries {
		if entry.Pass {
			result.Passed++
		} else {
			result.Failed++
		}
	}
	if result.Total > 0 && result.Failed == 0 {
		result.Status = "pass"
	}
	return result
}
