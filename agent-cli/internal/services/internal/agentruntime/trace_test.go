package agentruntime

import (
	public "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTraceStagesOutsideBundleThenAttaches(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "session")
	request := public.Request{RecordDirectory: bundle}
	options := SessionRunOptions{Clock: clock.NewDeterministic(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Millisecond)}
	trace, err := prepareTrace(&request, &options, options.Clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bundle); !os.IsNotExist(err) {
		t.Fatalf("recorder destination touched before publication: %v", err)
	}
	if options.RuntimeObserver == nil {
		t.Fatal("missing runtime observer")
	}
	if err := os.Mkdir(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	if err := trace.finish(bundle, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(bundle, "audio-trace", "timeline.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "2026-01-01") {
		t.Fatalf("injected clock missing: %s", data)
	}
}

func TestFailedSessionRetainsStagedTraceWithoutModifyingBundle(t *testing.T) {
	bundle := t.TempDir()
	request := public.Request{RecordDirectory: bundle}
	options := SessionRunOptions{Clock: clock.Real{}}
	trace, err := prepareTrace(&request, &options, options.Clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := trace.finish(bundle, false); err == nil {
		t.Fatal("missing retained evidence diagnostic")
	}
	if _, err := os.Stat(filepath.Join(bundle, "audio-trace")); !os.IsNotExist(err) {
		t.Fatalf("failed run modified bundle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(trace.path, "timeline.jsonl")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(trace.path) })
}
