package audiofixture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLoadReadsExactFrames(t *testing.T) {
	want := patternSamples()
	root, _ := writeCorpus(t, "fixture", wavio.Rate16kHz, want)
	source := load(t, root, "fixture")
	if source.SampleRate != SampleRate || source.Channels != Channels || len(source.samples) != len(want) || slices.IndexFunc(source.samples, func(v int16) bool { return v != 0 }) < 0 {
		t.Fatalf("source contract = %#v", source)
	}
	assertFrames(t, source, want)
	source, err := Load("utt_short_16k")
	if err != nil || source.ID != "utt_short_16k" || slices.IndexFunc(source.samples, func(v int16) bool { return v != 0 }) < 0 {
		t.Fatalf("Load() = %#v, %v", source, err)
	}
	want24 := []int16{0, 3000, -6000, 12000}
	root24, _ := writeCorpus(t, "fixture", wavio.Rate24kHz, want24)
	normalized := load(t, root24, "fixture")
	want24, err = wavio.Resample(want24, wavio.Rate24kHz, SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(normalized.samples, want24) {
		t.Fatalf("normalized samples = %v, want %v", normalized.samples, want24)
	}
}
func TestS4ErrorPaths(t *testing.T) {
	for _, name := range []string{"unknown ID", "missing file", "unmanifested file", "hash mismatch", "malformed manifest", "invalid audio"} {
		t.Run(name, func(t *testing.T) {
			root, want := writeCorpus(t, "fixture", wavio.Rate16kHz, patternSamples())
			assertFrames(t, load(t, root, "fixture"), want)
			mutateS4(t, name, root)
			id := map[bool]string{name == "unknown ID": "does-not-exist"}[true]
			if id == "" {
				id = "fixture"
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
		return mustJSON(manifest{SchemaVersion: 1, Files: []manifestEntry{{ID: id, Path: path, SHA256: digest}}})
	}
	docs := []struct {
		data  []byte
		field string
	}{
		{[]byte("{"), "json"}, {mustJSON(manifest{SchemaVersion: 2}), "schema_version"}, {mustJSON(manifest{SchemaVersion: 1}), "files"},
		{entry("", "fixture.wav", hash), "files[0].id"}, {entry("fixture", "../fixture.wav", hash), "files[0].path"}, {entry("fixture", "fixture.wav", "not-a-hash"), "files[0].sha256"},
	}
	for _, doc := range docs {
		root := t.TempDir()
		mustOK(t, os.WriteFile(filepath.Join(root, manifestFile), doc.data, 0o600))
		source, err := NewLoader(root).Load("fixture")
		typed, ok := err.(*MalformedManifestError)
		if source != nil || !ok || typed.Field != doc.field {
			t.Fatalf("Load() = %#v, %v; want field %q", source, err, doc.field)
		}
	}
}
func mutateS4(t *testing.T, name, root string) {
	switch name {
	case "missing file":
		mustOK(t, os.Remove(filepath.Join(root, "fixture.wav")))
	case "unmanifested file":
		writeWAV(t, filepath.Join(root, "extra.wav"), wavio.Rate16kHz, []int16{9})
	case "hash mismatch":
		path := filepath.Join(root, "fixture.wav")
		data, _ := os.ReadFile(path)
		data[44]++
		mustOK(t, os.WriteFile(path, data, 0o600))
	case "malformed manifest":
		mustOK(t, os.WriteFile(filepath.Join(root, manifestFile), []byte("{"), 0o600))
	case "invalid audio":
		data := []byte("not a WAV")
		digest := sha256.Sum256(data)
		mustOK(t, os.WriteFile(filepath.Join(root, "fixture.wav"), data, 0o600))
		writeManifest(t, root, manifest{SchemaVersion: 1, Files: []manifestEntry{{ID: "fixture", Path: "fixture.wav", SHA256: hex.EncodeToString(digest[:])}}})
	}
}
func checkS4Error(t *testing.T, name string, err error) {
	check := func(ok bool) {
		if !ok {
			t.Fatalf("error = %v", err)
		}
	}
	switch e := err.(type) {
	case *UnknownIDError:
		check(name == "unknown ID" && e.ID == "does-not-exist")
	case *MissingFileError:
		check(name == "missing file" && e.ID == "fixture" && e.Path == "fixture.wav")
	case *UnmanifestedFileError:
		check(name == "unmanifested file" && e.Path == "extra.wav")
	case *HashMismatchError:
		check(name == "hash mismatch" && e.ID == "fixture" && e.Path == "fixture.wav" && len(e.Expected) == 64 && len(e.Actual) == 64 && e.Expected != e.Actual)
	case *MalformedManifestError:
		check(name == "malformed manifest" && e.Path == manifestFile && e.Field == "json")
	default:
		if name == "invalid audio" {
			return
		}
		t.Fatalf("error type = %T", err)
	}
	check(err.Error() != "")
}
func assertFrames(t *testing.T, source *Source, samples []int16) {
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
	if err := source.ReadFrame(context.Background(), make([]int16, FrameSize)); err != io.EOF {
		t.Fatalf("after final frame = %v", err)
	}
}
func writeCorpus(t *testing.T, id string, rate int, samples []int16) (string, []int16) {
	root := t.TempDir()
	encoded := writeWAV(t, filepath.Join(root, "fixture.wav"), rate, samples)
	digest := sha256.Sum256(encoded)
	writeManifest(t, root, manifest{SchemaVersion: 1, Files: []manifestEntry{{ID: id, Path: "fixture.wav", SHA256: hex.EncodeToString(digest[:])}}})
	return root, append([]int16(nil), samples...)
}
func writeManifest(t *testing.T, root string, value manifest) {
	data := mustJSON(value)
	mustOK(t, os.WriteFile(filepath.Join(root, manifestFile), data, 0o600))
}
func writeWAV(t *testing.T, path string, rate int, samples []int16) []byte {
	var data bytes.Buffer
	mustOK(t, wavio.Write(&data, rate, samples))
	mustOK(t, os.WriteFile(path, data.Bytes(), 0o600))
	return data.Bytes()
}
func load(t *testing.T, root, id string) *Source {
	source, _ := NewLoader(root).Load(id)
	return source
}
func mustOK(t *testing.T, err error) {
	if err != nil {
		t.Fatal(err)
	}
}
func mustJSON(value any) []byte { data, _ := json.Marshal(value); return data }
func patternSamples() []int16 {
	samples := make([]int16, FrameSize+7)
	for i := range samples {
		samples[i] = int16(i - 200)
	}
	return samples
}
