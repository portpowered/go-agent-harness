package audiofixture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

func TestLoadCommittedFixtureByID(t *testing.T) {
	source, err := Load("utt_short_16k")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if source.ID != "utt_short_16k" || source.SampleRate != SampleRate || source.Channels != Channels {
		t.Fatalf("source metadata = %#v, want ID/rate/channels", source)
	}
	if source.SampleCount() == 0 || !hasNonZero(source.SamplesCopy()) {
		t.Fatal("committed fixture has no decoded non-zero samples")
	}
	frame := make([]int16, FrameSize)
	if err := source.ReadFrame(context.Background(), frame); err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if !hasNonZero(frame) {
		t.Fatal("first committed frame has no sample evidence")
	}
}

func TestLoadNormalizes24KFixtureToSourceContract(t *testing.T) {
	samples := []int16{0, 3000, -6000, 12000}
	root, want := writeCorpus(t, "fixture_24k", wavio.Rate24kHz, samples)
	source, err := NewLoader(root).Load("fixture_24k")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want, err = wavio.Resample(want, wavio.Rate24kHz, SampleRate)
	if err != nil {
		t.Fatalf("Resample() error = %v", err)
	}
	if source.SampleRate != SampleRate || source.Channels != Channels || !reflect.DeepEqual(source.SamplesCopy(), want) {
		t.Fatalf("normalized source = rate %d channels %d samples %v, want 16k mono %v", source.SampleRate, source.Channels, source.SamplesCopy(), want)
	}
}

func TestLoadS4ErrorPathTable(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
		check  func(*testing.T, error)
	}{
		{
			name:   "unknown ID",
			mutate: func(*testing.T, string) {},
			check: func(t *testing.T, err error) {
				var typed *UnknownIDError
				if !errors.As(err, &typed) || typed.ID != "does-not-exist" || !errors.Is(err, ErrUnknownID) {
					t.Fatalf("error = %T %v, want typed unknown ID", err, err)
				}
			},
		},
		{
			name: "missing file",
			mutate: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "fixture.wav")); err != nil {
					t.Fatalf("remove fixture: %v", err)
				}
			},
			check: func(t *testing.T, err error) {
				var typed *MissingFileError
				if !errors.As(err, &typed) || typed.ID != "fixture" || typed.Path != "fixture.wav" || !errors.Is(err, ErrMissingFile) {
					t.Fatalf("error = %T %v, want typed missing file", err, err)
				}
			},
		},
		{
			name: "unmanifested file",
			mutate: func(t *testing.T, root string) {
				writeWAV(t, filepath.Join(root, "extra.wav"), wavio.Rate16kHz, []int16{9, -9})
			},
			check: func(t *testing.T, err error) {
				var typed *UnmanifestedFileError
				if !errors.As(err, &typed) || typed.Path != "extra.wav" || !errors.Is(err, ErrUnmanifestedFile) {
					t.Fatalf("error = %T %v, want typed unmanifested file", err, err)
				}
			},
		},
		{
			name: "hash mismatch",
			mutate: func(t *testing.T, root string) {
				path := filepath.Join(root, "fixture.wav")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read fixture: %v", err)
				}
				data[44]++
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatalf("mutate fixture: %v", err)
				}
			},
			check: func(t *testing.T, err error) {
				var typed *HashMismatchError
				if !errors.As(err, &typed) || typed.ID != "fixture" || typed.Path != "fixture.wav" || len(typed.Expected) != 64 || len(typed.Actual) != 64 || !errors.Is(err, ErrHashMismatch) {
					t.Fatalf("error = %T %v, want typed hash mismatch", err, err)
				}
			},
		},
		{
			name: "malformed manifest",
			mutate: func(t *testing.T, root string) {
				if err := os.WriteFile(filepath.Join(root, manifestFile), []byte("{"), 0o600); err != nil {
					t.Fatalf("write malformed manifest: %v", err)
				}
			},
			check: func(t *testing.T, err error) {
				var typed *MalformedManifestError
				if !errors.As(err, &typed) || typed.Path != manifestFile || typed.Field != "json" || !errors.Is(err, ErrMalformedManifest) {
					t.Fatalf("error = %T %v, want typed malformed manifest", err, err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, want := writeCorpus(t, "fixture", wavio.Rate16kHz, patternSamples())
			positive, err := NewLoader(root).Load("fixture")
			if err != nil {
				t.Fatalf("positive control Load() error = %v", err)
			}
			assertSourceFrames(t, positive, want)
			test.mutate(t, root)
			got, err := NewLoader(root).Load(map[string]string{"unknown ID": "does-not-exist"}[test.name])
			if test.name != "unknown ID" {
				got, err = NewLoader(root).Load("fixture")
			}
			if got != nil {
				t.Fatalf("error path returned usable source: %#v", got)
			}
			if err == nil {
				t.Fatal("error path returned nil error")
			}
			test.check(t, err)
		})
	}
}

func TestMalformedManifestRequiredFields(t *testing.T) {
	root, _ := writeCorpus(t, "fixture", wavio.Rate16kHz, patternSamples())
	invalid := testManifest{SchemaVersion: 1, Files: []testEntry{{ID: "fixture", Path: "fixture.wav", SHA256: "not-a-hash"}}}
	data, err := json.Marshal(invalid)
	if err != nil {
		t.Fatalf("marshal invalid manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, manifestFile), data, 0o600); err != nil {
		t.Fatalf("write invalid manifest: %v", err)
	}
	source, err := NewLoader(root).Load("fixture")
	if source != nil {
		t.Fatalf("invalid manifest returned source: %#v", source)
	}
	var typed *MalformedManifestError
	if !errors.As(err, &typed) || typed.Field != "files[0].sha256" {
		t.Fatalf("error = %T %v, want invalid hash field", err, err)
	}
}

func TestManifestValidationFailures(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const entry = `{"id":"fixture","path":"fixture.wav","sha256":"` + hash + `"}`
	tests := []struct {
		name  string
		doc   string
		field string
	}{
		{name: "trailing JSON", doc: `{"schema_version":1,"files":[` + entry + `]} {}`, field: "json"},
		{name: "unknown field", doc: `{"schema_version":1,"unexpected":true,"files":[` + entry + `]}`, field: "json"},
		{name: "schema version", doc: `{"schema_version":2,"files":[` + entry + `]}`, field: "schema_version"},
		{name: "empty files", doc: `{"schema_version":1,"files":[]}`, field: "files"},
		{name: "empty ID", doc: `{"schema_version":1,"files":[{"id":"","path":"fixture.wav","sha256":"` + hash + `"}]}`, field: "files[0].id"},
		{name: "duplicate ID", doc: `{"schema_version":1,"files":[` + entry + `,` + entry + `]}`, field: "files[1].id"},
		{name: "unsafe path", doc: `{"schema_version":1,"files":[{"id":"fixture","path":"../fixture.wav","sha256":"` + hash + `"}]}`, field: "files[0].path"},
		{name: "non-WAV path", doc: `{"schema_version":1,"files":[{"id":"fixture","path":"fixture.pcm","sha256":"` + hash + `"}]}`, field: "files[0].path"},
		{name: "duplicate path", doc: `{"schema_version":1,"files":[` + entry + `,{"id":"other","path":"fixture.wav","sha256":"` + hash + `"}]}`, field: "files[1].path"},
		{name: "uppercase hash", doc: `{"schema_version":1,"files":[{"id":"fixture","path":"fixture.wav","sha256":"` + strings.ToUpper(hash) + `"}]}`, field: "files[0].sha256"},
		{name: "non-hex hash", doc: `{"schema_version":1,"files":[{"id":"fixture","path":"fixture.wav","sha256":"` + strings.Repeat("z", 64) + `"}]}`, field: "files[0].sha256"},
		{name: "unsupported rate", doc: `{"schema_version":1,"files":[{"id":"fixture","path":"fixture.wav","sample_rate_hz":44100,"sha256":"` + hash + `"}]}`, field: "files[0].sample_rate_hz"},
		{name: "stereo metadata", doc: `{"schema_version":1,"files":[{"id":"fixture","path":"fixture.wav","channels":2,"sha256":"` + hash + `"}]}`, field: "files[0].channels"},
		{name: "non-PCM16 metadata", doc: `{"schema_version":1,"files":[{"id":"fixture","path":"fixture.wav","bits_per_sample":8,"sha256":"` + hash + `"}]}`, field: "files[0].bits_per_sample"},
		{name: "negative metadata", doc: `{"schema_version":1,"files":[{"id":"fixture","path":"fixture.wav","sample_count":-1,"sha256":"` + hash + `"}]}`, field: "files[0]"},
		{name: "bad format container", doc: `{"schema_version":1,"files":[{"id":"fixture","path":"fixture.wav","format":{"container":"aiff"},"sha256":"` + hash + `"}]}`, field: "files[0].format.container"},
		{name: "bad format encoding", doc: `{"schema_version":1,"files":[{"id":"fixture","path":"fixture.wav","format":{"encoding":"float"},"sha256":"` + hash + `"}]}`, field: "files[0].format.encoding"},
		{name: "bad format sample format", doc: `{"schema_version":1,"files":[{"id":"fixture","path":"fixture.wav","format":{"sample_format":"s24le"},"sha256":"` + hash + `"}]}`, field: "files[0].format.sample_format"},
		{name: "bad format channels", doc: `{"schema_version":1,"files":[{"id":"fixture","path":"fixture.wav","format":{"channels":2},"sha256":"` + hash + `"}]}`, field: "files[0].format.channels"},
		{name: "bad format bits", doc: `{"schema_version":1,"files":[{"id":"fixture","path":"fixture.wav","format":{"bits_per_sample":8},"sha256":"` + hash + `"}]}`, field: "files[0].format.bits_per_sample"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, manifestFile), []byte(test.doc), 0o600); err != nil {
				t.Fatalf("write manifest: %v", err)
			}
			source, err := NewLoader(root).Load("fixture")
			if source != nil {
				t.Fatalf("invalid manifest returned source: %#v", source)
			}
			var typed *MalformedManifestError
			if !errors.As(err, &typed) || typed.Field != test.field {
				t.Fatalf("error = %T %v, want field %q", err, err, test.field)
			}
		})
	}
}

func TestInvalidAudioAndConvenienceLoaders(t *testing.T) {
	root := t.TempDir()
	bad := []byte("not a WAV")
	if err := os.WriteFile(filepath.Join(root, "fixture.wav"), bad, 0o600); err != nil {
		t.Fatalf("write invalid audio: %v", err)
	}
	digest := sha256.Sum256(bad)
	doc := testManifest{SchemaVersion: 1, Files: []testEntry{{ID: "fixture", Path: "fixture.wav", SHA256: hex.EncodeToString(digest[:])}}}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, manifestFile), data, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	source, err := New(root).Load("fixture")
	if source != nil {
		t.Fatalf("invalid audio returned source: %#v", source)
	}
	var invalid *InvalidAudioError
	if !errors.As(err, &invalid) || !errors.Is(err, ErrInvalidAudio) || invalid.ID != "fixture" || invalid.Path != "fixture.wav" {
		t.Fatalf("error = %T %v, want typed invalid audio", err, err)
	}
	if invalid.Error() == "" || invalid.Unwrap() == nil || !invalid.Is(wavio.ErrTruncated) {
		t.Fatalf("invalid audio error contract is incomplete: %#v", invalid)
	}

	if _, err := LoadFrom(root, "fixture"); err == nil {
		t.Fatal("LoadFrom() unexpectedly succeeded")
	}
	if _, err := LoadFromDir(root, "fixture"); err == nil {
		t.Fatal("LoadFromDir() unexpectedly succeeded")
	}
	if !strings.Contains(DefaultCorpusRoot(), filepath.Join("go-agent-loop", "testdata", "audio")) {
		t.Fatalf("DefaultCorpusRoot() = %q", DefaultCorpusRoot())
	}
	var nilLoader *Loader
	if _, err := nilLoader.Load("fixture"); !errors.Is(err, ErrCorpusIO) {
		t.Fatalf("nil loader error = %v, want ErrCorpusIO", err)
	}
	if _, err := NewLoader(t.TempDir()).Load("fixture"); !errors.Is(err, ErrMalformedManifest) {
		t.Fatalf("missing manifest error = %v, want ErrMalformedManifest", err)
	}
}

func TestErrorContracts(t *testing.T) {
	unknown := &UnknownIDError{ID: "missing"}
	if unknown.Error() == "" || !unknown.Is(ErrUnknownID) {
		t.Fatal("unknown ID error contract is incomplete")
	}
	malformed := &MalformedManifestError{Path: manifestFile, Field: "json", Reason: "bad", Err: io.EOF}
	if malformed.Error() == "" || malformed.Unwrap() != io.EOF || !malformed.Is(ErrMalformedManifest) {
		t.Fatal("malformed manifest error contract is incomplete")
	}
	missing := &MissingFileError{ID: "id", Path: "missing.wav"}
	if missing.Error() == "" || !missing.Is(ErrMissingFile) {
		t.Fatal("missing file error contract is incomplete")
	}
	unmanifested := &UnmanifestedFileError{Path: "extra.wav"}
	if unmanifested.Error() == "" || !unmanifested.Is(ErrUnmanifestedFile) {
		t.Fatal("unmanifested file error contract is incomplete")
	}
	hash := &HashMismatchError{ID: "id", Path: "fixture.wav", Expected: "expected", Actual: "actual"}
	if hash.Error() == "" || !hash.Is(ErrHashMismatch) {
		t.Fatal("hash error contract is incomplete")
	}
	invalid := &InvalidAudioError{ID: "id", Path: "fixture.wav"}
	if invalid.Error() == "" || invalid.Unwrap() != nil || !invalid.Is(ErrInvalidAudio) {
		t.Fatal("nil invalid-audio error contract is incomplete")
	}
	corpus := &CorpusIOError{Operation: "read", Path: "fixture.wav", Err: io.EOF}
	if corpus.Error() == "" || corpus.Unwrap() != io.EOF || !corpus.Is(ErrCorpusIO) {
		t.Fatal("corpus I/O error contract is incomplete")
	}
	if (&CorpusIOError{Operation: "read", Path: "fixture.wav"}).Error() == "" {
		t.Fatal("nil corpus I/O error has no message")
	}
	frame := &FrameSizeError{Operation: "read", Got: 1, Want: FrameSize}
	if frame.Error() == "" || !frame.Is(ErrInvalidFrameSize) {
		t.Fatal("frame error contract is incomplete")
	}
	closed := &ClosedError{Operation: "read"}
	if closed.Error() == "" || !closed.Is(ErrClosed) {
		t.Fatal("closed error contract is incomplete")
	}
	_ = fmt.Sprint(unknown, malformed, missing, unmanifested, hash, invalid, corpus, frame, closed)
}

func TestSourceConformanceAndTypedFrameErrors(t *testing.T) {
	want := patternSamples()
	source := newSource("fixture", "fixture.wav", want)
	if err := source.ReadFrame(context.Background(), make([]int16, FrameSize-1)); !errors.Is(err, ErrInvalidFrameSize) {
		t.Fatalf("invalid frame error = %v, want ErrInvalidFrameSize", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := source.ReadFrame(ctx, make([]int16, FrameSize)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled frame error = %v, want context.Canceled", err)
	}
	assertSourceFrames(t, source, want)
	if err := source.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := source.ReadFrame(context.Background(), make([]int16, FrameSize)); !errors.Is(err, ErrClosed) {
		t.Fatalf("read after close = %v, want ErrClosed", err)
	}
}

type testManifest struct {
	SchemaVersion int         `json:"schema_version"`
	Files         []testEntry `json:"files"`
}

type testEntry struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func writeCorpus(t *testing.T, id string, rate int, samples []int16) (string, []int16) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "fixture.wav")
	encoded := writeWAV(t, path, rate, samples)
	digest := sha256.Sum256(encoded)
	manifest := testManifest{SchemaVersion: 1, Files: []testEntry{{ID: id, Path: "fixture.wav", SHA256: hex.EncodeToString(digest[:])}}}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, manifestFile), data, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return root, append([]int16(nil), samples...)
}

func writeWAV(t *testing.T, path string, rate int, samples []int16) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := wavio.Write(&encoded, rate, samples); err != nil {
		t.Fatalf("wavio.Write() error = %v", err)
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatalf("write WAV: %v", err)
	}
	return encoded.Bytes()
}

func assertSourceFrames(t *testing.T, source *Source, samples []int16) {
	t.Helper()
	if source.SampleRate != SampleRate || source.Channels != Channels || source.SampleCount() != len(samples) {
		t.Fatalf("source metadata = rate %d channels %d count %d, want 16k mono count %d", source.SampleRate, source.Channels, source.SampleCount(), len(samples))
	}
	wantFrames := (len(samples) + FrameSize - 1) / FrameSize
	for frameIndex := 0; frameIndex < wantFrames; frameIndex++ {
		buf := make([]int16, FrameSize)
		for index := range buf {
			buf[index] = 12345
		}
		if err := source.ReadFrame(context.Background(), buf); err != nil {
			t.Fatalf("ReadFrame(%d) error = %v", frameIndex, err)
		}
		want := make([]int16, FrameSize)
		start := frameIndex * FrameSize
		copy(want, samples[start:minInt(start+FrameSize, len(samples))])
		if !reflect.DeepEqual(buf, want) {
			t.Fatalf("ReadFrame(%d) = %v, want %v", frameIndex, buf, want)
		}
	}
	if err := source.ReadFrame(context.Background(), make([]int16, FrameSize)); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadFrame after %d frames = %v, want io.EOF", wantFrames, err)
	}
}

func patternSamples() []int16 {
	samples := make([]int16, FrameSize+7)
	for index := range samples {
		samples[index] = int16(index - 200)
	}
	return samples
}

func hasNonZero(samples []int16) bool {
	for _, sample := range samples {
		if sample != 0 {
			return true
		}
	}
	return false
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
