package embedding_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	devicewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// TestExternalHostSelectsFileInputPacing proves an independent host can turn
// its own option spelling into a pacing policy and apply it to every finite
// file input without reaching into runtime internals.
func TestExternalHostSelectsFileInputPacing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.pcm")
	if err := os.WriteFile(path, make([]byte, 960), 0o600); err != nil {
		t.Fatalf("write clip: %v", err)
	}
	scheduler := clock.NewDeterministic(time.Unix(0, 0), time.Millisecond)
	for _, test := range []struct {
		option string
		pace   bool
	}{{option: "realtime", pace: true}, {option: "20x", pace: true}, {option: "unpaced", pace: false}} {
		pacing, err := devices.ParseFilePacing(test.option)
		if err != nil {
			t.Fatalf("ParseFilePacing(%q): %v", test.option, err)
		}
		handle, err := devicewire.NewFileMediaService().OpenFileMedia(devices.FileMediaRequest{
			Input: &devices.FileMediaSource{Path: path}, InputTurns: []string{path}, Scheduler: scheduler, Pacing: pacing,
		})
		if err != nil {
			t.Fatalf("OpenFileMedia(%s): %v", test.option, err)
		}
		media := handle.Media()
		if media.Input.Pace != test.pace || media.InputTurns[0].Pace != test.pace || media.Input.Scheduler == nil {
			t.Fatalf("%s pacing admitted input %+v and turn %+v, want pace=%v with a scheduler", test.option, media.Input, media.InputTurns[0], test.pace)
		}
		if err := handle.Close(); err != nil {
			t.Fatalf("close %s media: %v", test.option, err)
		}
	}
}
