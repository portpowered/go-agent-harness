package integration

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

// shortVoicedSlice is the length of the voiced input slice that replaces a
// multi-second committed WAV where a test's assertion does not depend on the
// input's duration. File input is paced in real time, so the slice turns a
// 2.75s or 4.84s wait into 0.3s while the replay fixture builders, which derive
// their expected input_audio_buffer.append frames from whatever WAV they are
// given, stay consistent with what the CLI streams.
const shortVoicedSlice = 300 * time.Millisecond

// writeVoicedWAVSlice writes the loudest window of the given duration from the
// WAV at sourcePath to a new WAV in t.TempDir() at the same rate and returns
// its path. Picking the loudest window keeps the slice genuinely voiced rather
// than leading silence; the same source and duration always yield the same
// slice.
func writeVoicedWAVSlice(t *testing.T, sourcePath string, duration time.Duration) string {
	t.Helper()
	encoded, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read WAV %s: %v", sourcePath, err)
	}
	rate, all, err := wavio.Read(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("parse WAV %s: %v", sourcePath, err)
	}
	window := loudestWindowSamplesIntegration(t, all, int(int64(rate)*int64(duration)/int64(time.Second)))
	var out bytes.Buffer
	if err := wavio.Write(&out, rate, window); err != nil {
		t.Fatalf("encode WAV slice of %s: %v", sourcePath, err)
	}
	name := strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath)) + "-slice.wav"
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatalf("write WAV slice %s: %v", path, err)
	}
	return path
}

// multiturnTurnSliceWAV returns a short voiced slice of one committed 0.72s
// multiturn corpus turn for scheduled-turn tests whose assertions compare the
// streamed turns against whatever WAVs were scheduled.
func multiturnTurnSliceWAV(t *testing.T, name string) string {
	t.Helper()
	return writeVoicedWAVSlice(t, locateCLIFixture(t, name), shortVoicedSlice)
}
