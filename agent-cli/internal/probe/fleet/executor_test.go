package fleet

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
)

func executionTestManifest(t *testing.T, entries, concurrency int) Manifest {
	t.Helper()
	manifest := Manifest{
		SchemaVersion: SchemaVersion,
		Scenarios:     []ScenarioRef{{ID: "scenario", Path: "scenario.json"}},
		Transports:    []Transport{TransportReplay},
		RepeatCount:   entries,
		Concurrency:   concurrency,
		EntryLimit:    entries,
	}
	for repeatIndex := 0; repeatIndex < entries; repeatIndex++ {
		manifest.Entries = append(manifest.Entries, Entry{
			ID:           EntryID("scenario", TransportReplay, repeatIndex),
			ScenarioID:   "scenario",
			ScenarioPath: "scenario.json",
			Transport:    TransportReplay,
			RepeatIndex:  repeatIndex,
		})
	}
	return manifest
}

func TestExecuteHonorsConcurrencyAndPreservesManifestOrder(t *testing.T) {
	const entryCount = 8
	const concurrency = 3
	manifest := executionTestManifest(t, entryCount, concurrency)
	started := make(chan struct{}, entryCount)
	release := make(chan struct{})
	var active atomic.Int32
	var peak atomic.Int32

	executionDone := make(chan Execution, 1)
	go func() {
		execution, err := Execute(context.Background(), manifest, func(ctx context.Context, entry Entry) (EntryOutcome, error) {
			current := active.Add(1)
			for {
				observed := peak.Load()
				if current <= observed || peak.CompareAndSwap(observed, current) {
					break
				}
			}
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
			active.Add(-1)
			if entry.RepeatIndex == 4 {
				return EntryOutcome{}, fmt.Errorf("repeat %d failed", entry.RepeatIndex)
			}
			return EntryOutcome{Pass: true}, nil
		})
		if err != nil {
			t.Errorf("Execute: %v", err)
		}
		executionDone <- execution
	}()

	for index := 0; index < concurrency; index++ {
		<-started
	}
	if got := peak.Load(); got > concurrency {
		t.Fatalf("peak concurrency = %d, want at most %d", got, concurrency)
	}
	close(release)

	execution := <-executionDone
	if len(execution.Results) != entryCount {
		t.Fatalf("result count = %d, want %d", len(execution.Results), entryCount)
	}
	for index, result := range execution.Results {
		wantID := manifest.Entries[index].ID
		if result.ID != wantID {
			t.Fatalf("result %d ID = %q, want %q", index, result.ID, wantID)
		}
		if index == 4 && result.Pass {
			t.Fatalf("failed entry %q unexpectedly passed", result.ID)
		}
		if index != 4 && !result.Pass {
			t.Fatalf("unrelated entry %q unexpectedly failed: %s", result.ID, result.Error)
		}
	}
	aggregate := execution.Result()
	if aggregate.Total != entryCount || aggregate.Passed != entryCount-1 || aggregate.Failed != 1 || aggregate.Status != "fail" {
		t.Fatalf("aggregate = %+v, want total=%d passed=%d failed=1 status=fail", aggregate, entryCount, entryCount-1)
	}
}

func TestExecuteConvertsExecutorPanicIntoFailedEntry(t *testing.T) {
	manifest := executionTestManifest(t, 1, 1)
	execution, err := Execute(context.Background(), manifest, func(context.Context, Entry) (EntryOutcome, error) {
		panic("boom")
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(execution.Results) != 1 || execution.Results[0].Pass {
		t.Fatalf("panic result = %+v, want one failed result", execution.Results)
	}
	if execution.Results[0].Error == "" {
		t.Fatal("panic result did not preserve an error")
	}
}
