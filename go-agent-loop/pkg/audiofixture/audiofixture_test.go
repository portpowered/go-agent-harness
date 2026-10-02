package audiofixture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func TestLoadReadsExactFrames(t *testing.T) {
	want := patternSamples()
	root, _ := writeCorpus(t, fixtureID, wavio.Rate16kHz, want)
	source := load(t, root, fixtureID)
	if source.SampleRate != SampleRate || source.Channels != Channels || len(source.samples) != len(want) || slices.IndexFunc(source.samples, func(v int16) bool { return v != 0 }) < 0 {
		t.Fatalf("source contract = %#v", source)
	}
	assertFrames(t, source, want)
	source, err := Load("utt_short_16k")
	if err != nil || source.ID != "utt_short_16k" || slices.IndexFunc(source.samples, func(v int16) bool { return v != 0 }) < 0 {
		t.Fatalf("Load() = %#v, %v", source, err)
	}
	want24 := []int16{0, 3000, -6000, 12000}
	root24, _ := writeCorpus(t, fixtureID, wavio.Rate24kHz, want24)
	normalized := load(t, root24, fixtureID)
	want24, err = wavio.Resample(want24, wavio.Rate24kHz, SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(normalized.samples, want24) {
		t.Fatalf("normalized samples = %v, want %v", normalized.samples, want24)
	}
}
func TestAudioFixtureErrorPaths(t *testing.T) {
	for _, name := range []string{"unknown ID", "missing file", "unmanifested file", "hash mismatch", "malformed manifest", "invalid audio"} {
		t.Run(name, func(t *testing.T) {
			root, want := writeCorpus(t, fixtureID, wavio.Rate16kHz, patternSamples())
			assertFrames(t, load(t, root, fixtureID), want)
			mutateS4(t, name, root)
			id := map[bool]string{name == "unknown ID": "does-not-exist"}[true]
			if id == "" {
				id = fixtureID
			}
			source, err := NewLoader(root).Load(id)
			if source != nil || err == nil {
				t.Fatalf("Load() = %#v, %v; want typed failure", source, err)
			}
			checkS4Error(t, name, err)
		})
	}
}
func TestMalformedManifestRequiredFields(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	entry := func(id, path, digest string) []byte {
		return mustJSON(t, manifest{SchemaVersion: 1, Files: []manifestEntry{{ID: id, Path: path, SHA256: digest}}})
	}
	docs := []struct {
		data  []byte
		field string
	}{
		{[]byte("{"), "json"}, {mustJSON(t, manifest{SchemaVersion: 2}), "schema_version"}, {mustJSON(t, manifest{SchemaVersion: 1}), "files"},
		{entry("", "fixture.wav", hash), "files[0].id"}, {entry(fixtureID, "../fixture.wav", hash), "files[0].path"}, {entry(fixtureID, "fixture.wav", "not-a-hash"), "files[0].sha256"},
	}
	for _, doc := range docs {
		root := t.TempDir()
		mustOK(t, os.WriteFile(filepath.Join(root, manifestFile), doc.data, 0o600))
		source, err := NewLoader(root).Load(fixtureID)
		var typed *MalformedManifestError
		ok := errors.As(err, &typed)
		if source != nil || !ok || typed.Field != doc.field {
			t.Fatalf("Load() = %#v, %v; want field %q", source, err, doc.field)
		}
	}
}
func mutateS4(t *testing.T, name, root string) {
	t.Helper()
	switch name {
	case "missing file":
		mustOK(t, os.Remove(filepath.Join(root, "fixture.wav")))
	case "unmanifested file":
		writeWAV(t, filepath.Join(root, "extra.wav"), wavio.Rate16kHz, []int16{9})
	case "hash mismatch":
		path := filepath.Join(root, "fixture.wav")
		data, err := os.ReadFile(path)
		mustOK(t, err)
		data[44]++
		mustOK(t, os.WriteFile(path, data, 0o600))
	case "malformed manifest":
		mustOK(t, os.WriteFile(filepath.Join(root, manifestFile), []byte("{"), 0o600))
	case "invalid audio":
		data := []byte("not a WAV")
		digest := sha256.Sum256(data)
		mustOK(t, os.WriteFile(filepath.Join(root, "fixture.wav"), data, 0o600))
		writeManifest(t, root, manifest{SchemaVersion: 1, Files: []manifestEntry{{ID: fixtureID, Path: "fixture.wav", SHA256: hex.EncodeToString(digest[:])}}})
	}
}
func checkS4Error(t *testing.T, name string, err error) {
	t.Helper()
	check := func(ok bool) {
		if !ok {
			t.Fatalf("error = %v", err)
		}
	}
	var (
		unknown      *UnknownIDError
		missing      *MissingFileError
		unmanifested *UnmanifestedFileError
		hash         *HashMismatchError
		malformed    *MalformedManifestError
	)
	switch {
	case errors.As(err, &unknown):
		check(name == "unknown ID" && unknown.ID == "does-not-exist")
	case errors.As(err, &missing):
		check(name == "missing file" && missing.ID == fixtureID && missing.Path == "fixture.wav")
	case errors.As(err, &unmanifested):
		check(name == "unmanifested file" && unmanifested.Path == "extra.wav")
	case errors.As(err, &hash):
		check(name == "hash mismatch" && hash.ID == fixtureID && hash.Path == "fixture.wav" && len(hash.Expected) == 64 && len(hash.Actual) == 64 && hash.Expected != hash.Actual)
	case errors.As(err, &malformed):
		check(name == "malformed manifest" && malformed.Path == manifestFile && malformed.Field == "json")
	default:
		if name == "invalid audio" {
			return
		}
		t.Fatalf("error type = %T", err)
	}
	check(err.Error() != "")
}
func assertFrames(t *testing.T, source *Source, samples []int16) {
	t.Helper()
	for _, size := range []int{FrameSize - 1, FrameSize + 1} {
		if err := source.ReadFrame(context.Background(), make([]int16, size)); err == nil {
			t.Fatalf("ReadFrame() with %d samples succeeded; want exact frame-size error", size)
		}
	}
	for start := 0; start < len(samples); start += FrameSize {
		buf := make([]int16, FrameSize)
		for i := range buf {
			buf[i] = 12345
		}
		mustOK(t, source.ReadFrame(context.Background(), buf))
		want := make([]int16, FrameSize)
		copy(want, samples[start:])
		if !slices.Equal(buf, want) {
			t.Fatalf("frame at %d = %v, want %v", start, buf, want)
		}
	}
	if err := source.ReadFrame(context.Background(), make([]int16, FrameSize)); !errors.Is(err, io.EOF) {
		t.Fatalf("after final frame = %v", err)
	}
}
func writeCorpus(t *testing.T, id string, rate int, samples []int16) (string, []int16) {
	t.Helper()
	root := t.TempDir()
	encoded := writeWAV(t, filepath.Join(root, "fixture.wav"), rate, samples)
	digest := sha256.Sum256(encoded)
	writeManifest(t, root, manifest{SchemaVersion: 1, Files: []manifestEntry{{ID: id, Path: "fixture.wav", SHA256: hex.EncodeToString(digest[:])}}})
	return root, append([]int16(nil), samples...)
}
func writeManifest(t *testing.T, root string, value manifest) {
	t.Helper()
	data := mustJSON(t, value)
	mustOK(t, os.WriteFile(filepath.Join(root, manifestFile), data, 0o600))
}
func writeWAV(t *testing.T, path string, rate int, samples []int16) []byte {
	t.Helper()
	var data bytes.Buffer
	mustOK(t, wavio.Write(&data, rate, samples))
	mustOK(t, os.WriteFile(path, data.Bytes(), 0o600))
	return data.Bytes()
}
func load(t *testing.T, root, id string) *Source {
	t.Helper()
	source, err := NewLoader(root).Load(id)
	mustOK(t, err)
	return source
}
func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	mustOK(t, err)
	return data
}

const fixtureID = "fixture"

func patternSamples() []int16 {
	samples := make([]int16, FrameSize+7)
	for i := range samples {
		samples[i] = int16(i - 200)
	}
	return samples
}
