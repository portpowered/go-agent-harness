package wire

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// skipFileSync keeps fixtures off the fsync path. Production durability is
// covered by the tests in this file and by tests that use NewService.
func skipFileSync(roomevidence.DurableFile) error { return nil }

func newTestService() roomevidence.Service {
	return NewServiceWithOptions(roomevidence.ServiceOptions{SyncFile: skipFileSync})
}

func TestFileSyncNilSelectsOSFileSync(t *testing.T) {
	t.Parallel()
	file, err := os.CreateTemp(t.TempDir(), "sync-")
	if err != nil {
		t.Fatal(err)
	}
	if err := roomevidence.FileSync(nil).Sync(file); err != nil {
		t.Fatalf("default sync of open file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	// Only (*os.File).Sync reports os.ErrClosed here, proving nil delegates to
	// the real fsync rather than silently skipping durability.
	if err := roomevidence.FileSync(nil).Sync(file); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("default sync of closed file = %v, want os.ErrClosed", err)
	}
}

func TestServiceSyncsEveryRecordedArtifact(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	synced := map[string]int{}
	destination := filepath.Join(t.TempDir(), "bundle")
	service := NewServiceWithOptions(roomevidence.ServiceOptions{SyncFile: func(file roomevidence.DurableFile) error {
		relative, err := filepath.Rel(destination, file.Name())
		mu.Lock()
		synced[filepath.ToSlash(relative)]++
		mu.Unlock()
		return errors.Join(err, file.Sync())
	}})
	base := time.Date(2026, 9, 11, 20, 0, 0, 0, time.UTC)
	source := clock.NewDeterministic(base, time.Millisecond)
	recorder, err := service.Open(roomevidence.RecordingRequest{
		Destination: destination, Manifest: testManifest(),
		AudioFormat: rooms.AudioFormat{SampleRate: 24000, Channels: 1, FrameDuration: 20 * time.Millisecond},
		StartedAt:   base, Clock: source,
	})
	if err != nil {
		t.Fatal(err)
	}
	source.AdvanceBy(time.Second)
	if _, err := finalizeForTest(recorder, testResult(), nil, source.Now()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{roomevidence.TimelinePath}
	for _, id := range []string{"speaker", "listener"} {
		paths := recorder.Artifacts(id)
		for _, path := range []string{paths.WAV, paths.Diagnostics, paths.Deltas, paths.Events, paths.SentPCM, paths.ReceivedPCM} {
			want = append(want, filepath.ToSlash(path))
		}
	}
	for _, name := range want {
		if synced[name] != 1 {
			t.Fatalf("artifact %s synced %d times, want once: %v", name, synced[name], synced)
		}
	}
	for _, prefix := range []string{".run-manifest-", ".room-latency-"} {
		found := false
		for name := range synced {
			found = found || strings.HasPrefix(name, prefix)
		}
		if !found {
			t.Fatalf("temporary %s* was not synced before rename: %v", prefix, synced)
		}
	}
}
