package ttscorpus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

func TestWaitReadyAndSynthesizeAgainstPinnedContract(t *testing.T) {
	sawRequest := map[string]any{}
	var sawReadyz bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/readyz":
			sawReadyz = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v1/audio/speech":
			if err := json.NewDecoder(r.Body).Decode(&sawRequest); err != nil {
				t.Errorf("decode request: %v", err)
			}
			w.Header().Set("Content-Type", "audio/wav")
			if err := wavio.Write(w, wavio.Rate24kHz, loudSamples(wavio.Rate24kHz, 1000)); err != nil {
				t.Errorf("write wav: %v", err)
			}
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	output := filepath.Join(t.TempDir(), "clip.wav")
	gen := NewGenerator(server.URL)
	if err := gen.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady() = %v", err)
	}
	if !sawReadyz {
		t.Fatal("readiness probe never hit /readyz")
	}
	if err := gen.Synthesize(context.Background(), Utterances[0], output); err != nil {
		t.Fatalf("Synthesize() = %v", err)
	}
	data, err := os.ReadFile(output)
	if err != nil || len(data) == 0 {
		t.Fatalf("synthesized clip missing: %v", err)
	}
	if sawRequest["model"] != "qwen3-tts-cpp" || sawRequest["input"] != Utterances[0] ||
		sawRequest["language"] != "English" || sawRequest["response_format"] != "wav" {
		t.Fatalf("request contract = %#v", sawRequest)
	}
	params, ok := sawRequest["params"].(map[string]any)
	if !ok || params["seed"] != "17" || params["temperature"] != "0.9" || len(params) != 6 {
		t.Fatalf("sampling params = %#v", sawRequest["params"])
	}
}

func TestSynthesizeNeverFabricatesAudioOnBackendFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "model exploded", http.StatusInternalServerError)
	}))
	defer server.Close()
	gen := NewGenerator(server.URL)
	if err := gen.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady() = %v", err)
	}
	output := filepath.Join(t.TempDir(), "clip.wav")
	err := gen.Synthesize(context.Background(), Utterances[0], output)
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("Synthesize() error = %v; want observed status failure", err)
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatal("failed synthesis must not write any audio")
	}
}

func TestWaitReadySurfacesObservedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "warming up", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	gen := NewGenerator(server.URL)
	gen.ReadyTimeout = 50 * time.Millisecond
	err := gen.WaitReady(context.Background())
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("WaitReady() error = %v; want bounded failure naming observed status", err)
	}
}

func TestEmitManifestHashesClosedSet(t *testing.T) {
	dir := t.TempDir()
	for i := range Utterances {
		for _, rate := range SampleRates {
			name := fmt.Sprintf("qwen_utt%02d_%dk.wav", i+1, rate/1000)
			var buf bytes.Buffer
			if err := wavio.Write(&buf, rate, loudSamples(rate, 1000)); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(dir, name), buf.Bytes())
		}
	}
	mustWrite(t, filepath.Join(dir, "stray.wav"), []byte("unmanifested"))
	if err := EmitManifest(dir); err == nil || !strings.Contains(err.Error(), "stray.wav") {
		t.Fatalf("EmitManifest() with stray file = %v; want unmanifested failure", err)
	}
	mustRemove(t, filepath.Join(dir, "stray.wav"))
	if err := EmitManifest(dir); err != nil {
		t.Fatalf("EmitManifest() = %v", err)
	}
	data := mustRead(t, filepath.Join(dir, "manifest.json"))
	var manifest corpusManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 1 || len(manifest.Files) != len(Utterances)*len(SampleRates) {
		t.Fatalf("manifest = schema %d files %d", manifest.SchemaVersion, len(manifest.Files))
	}
	if manifest.CorpusByteTotal <= 0 || manifest.CorpusByteTotal >= MaxCorpusBytes {
		t.Fatalf("corpus_byte_total = %d", manifest.CorpusByteTotal)
	}
	for _, file := range manifest.Files {
		if len(file.SHA256) != 64 || file.SampleRateHz == 0 || file.DurationSeconds <= 0 || file.ByteSize <= 0 {
			t.Fatalf("incomplete entry: %+v", file)
		}
	}
}

func TestEmitManifestFailsOnMissingClip(t *testing.T) {
	dir := t.TempDir()
	err := EmitManifest(dir)
	if err == nil || !strings.Contains(err.Error(), "qwen_utt01_16k.wav") {
		t.Fatalf("EmitManifest() on empty dir = %v", err)
	}
}

func TestPinnedRequestMatchesPinDoc(t *testing.T) {
	request := pinnedRequest("probe text")
	if request["input"] != "probe text" || request["speed"] != 1.0 {
		t.Fatalf("pinnedRequest = %#v", request)
	}
	params := request["params"].(map[string]string)
	want := map[string]string{"temperature": "0.9", "top_k": "50", "top_p": "1.0", "repetition_penalty": "1.05", "max_new_tokens": "512", "seed": "17"}
	for key, value := range want {
		if params[key] != value {
			t.Fatalf("params[%q] = %q, want %q", key, params[key], value)
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
