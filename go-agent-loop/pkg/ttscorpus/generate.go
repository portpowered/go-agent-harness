package ttscorpus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

// Corpus budget from the program rules.
const MaxCorpusBytes = 25 * 1024 * 1024

// Default endpoint of the pinned LocalAI backend.
const DefaultEndpoint = "http://127.0.0.1:8080"

// SampleRates are the session-path sample rates the corpus must cover.
var SampleRates = []int{wavio.Rate16kHz, wavio.Rate24kHz}

// Utterances is the closed synthesis set; nothing outside it may be synthesized.
var Utterances = []string{
	"The timer is ready for the next step.",
	"Open the calendar.",
}

// pinnedRequest mirrors probe.request in deploy/localai/models/qwen3-tts-pinned.json.
func pinnedRequest(text string) map[string]any {
	return map[string]any{
		"model":           "qwen3-tts-cpp",
		"input":           text,
		"language":        "English",
		"response_format": "wav",
		"speed":           1.0,
		"params": map[string]string{
			"temperature":        "0.9",
			"top_k":              "50",
			"top_p":              "1.0",
			"repetition_penalty": "1.05",
			"max_new_tokens":     "512",
			"seed":               "17",
		},
	}
}

// Generator drives the pinned LocalAI qwen3-tts-cpp backend.
type Generator struct {
	Endpoint        string
	Client          *http.Client
	ReadyTimeout    time.Duration
	GenerateTimeout time.Duration
}

// NewGenerator returns a generator for the pinned backend with bounded timeouts.
func NewGenerator(endpoint string) *Generator {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	return &Generator{
		Endpoint:        strings.TrimRight(endpoint, "/"),
		Client:          &http.Client{},
		ReadyTimeout:    30 * time.Second,
		GenerateTimeout: 150 * time.Second,
	}
}

// WaitReady polls /readyz until it answers 200 or the bounded timeout elapses;
// the observed error is always surfaced so failures are never silent.
func (g *Generator) WaitReady(ctx context.Context) error {
	deadline := time.Now().Add(g.ReadyTimeout)
	var lastErr error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.Endpoint+"/readyz", nil)
		if err != nil {
			return fmt.Errorf("ttscorpus: build readiness probe: %w", err)
		}
		resp, err := g.Client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("readyz status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ttscorpus: backend %s not ready within %s: last error: %v", g.Endpoint, g.ReadyTimeout, lastErr)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("ttscorpus: backend %s readiness canceled: %w", g.Endpoint, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Synthesize posts one utterance to /v1/audio/speech, writes the WAV, and
// validates clip sanity. Audio is never fabricated on failure.
func (g *Generator) Synthesize(ctx context.Context, text, outputPath string) error {
	body, err := json.Marshal(pinnedRequest(text))
	if err != nil {
		return fmt.Errorf("ttscorpus: encode synthesis request: %w", err)
	}
	synthCtx, cancel := context.WithTimeout(ctx, g.GenerateTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(synthCtx, http.MethodPost, g.Endpoint+"/v1/audio/speech", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("ttscorpus: build synthesis request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.Client.Do(req)
	if err != nil {
		return fmt.Errorf("ttscorpus: synthesis request failed against %s: %w", g.Endpoint, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("ttscorpus: read synthesis response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ttscorpus: synthesis failed with status %d: %s", resp.StatusCode, truncate(string(data), 512))
	}
	if err := validateWAVBytes(data); err != nil {
		return err
	}
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		return fmt.Errorf("ttscorpus: write %s: %w", outputPath, err)
	}
	return nil
}

type corpusFile struct {
	ID              string       `json:"id"`
	Path            string       `json:"path"`
	Class           string       `json:"class"`
	Source          string       `json:"source"`
	Format          corpusFormat `json:"format"`
	SampleRateHz    int          `json:"sample_rate_hz"`
	Channels        int          `json:"channels"`
	BitsPerSample   int          `json:"bits_per_sample"`
	SampleCount     int          `json:"sample_count"`
	DurationSeconds float64      `json:"duration_seconds"`
	RMSEnergy       float64      `json:"rms_energy"`
	ByteSize        int64        `json:"byte_size"`
	SHA256          string       `json:"sha256"`
}

type corpusFormat struct {
	Container     string `json:"container"`
	Encoding      string `json:"encoding"`
	SampleFormat  string `json:"sample_format"`
	Channels      int    `json:"channels"`
	BitsPerSample int    `json:"bits_per_sample"`
}

type corpusManifest struct {
	SchemaVersion   int          `json:"schema_version"`
	CorpusByteTotal int64        `json:"corpus_byte_total"`
	SampleRatesHz   []int        `json:"sample_rates_hz"`
	Files           []corpusFile `json:"files"`
}

// EmitManifest hashes every WAV under outputDir into manifest.json, asserting
// the closed set matches the expected utterance/rate matrix and stays under
// the 25 MB program budget.
func EmitManifest(outputDir string) error {
	var total int64
	files := make([]corpusFile, 0, len(Utterances)*len(SampleRates))
	for i, text := range Utterances {
		for _, rate := range SampleRates {
			name := fmt.Sprintf("qwen_utt%02d_%dk.wav", i+1, rate/1000)
			path := filepath.Join(outputDir, name)
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("ttscorpus: read generated clip: %w", err)
			}
			sampleFileRate, samples, err := wavio.Read(bytes.NewReader(data))
			if err != nil {
				return fmt.Errorf("ttscorpus: decode %s: %w", name, err)
			}
			if sampleFileRate != rate {
				return fmt.Errorf("ttscorpus: %s decoded at %d Hz; want %d Hz", name, sampleFileRate, rate)
			}
			digest := sha256.Sum256(data)
			total += int64(len(data))
			files = append(files, corpusFile{
				ID:              strings.TrimSuffix(name, ".wav"),
				Path:            name,
				Class:           "utterance",
				Source:          text,
				Format:          corpusFormat{Container: "wav", Encoding: "PCM", SampleFormat: "s16le", Channels: 1, BitsPerSample: 16},
				SampleRateHz:    sampleFileRate,
				Channels:        1,
				BitsPerSample:   16,
				SampleCount:     len(samples),
				DurationSeconds: float64(len(samples)) / float64(sampleFileRate),
				RMSEnergy:       RMS(samples),
				ByteSize:        int64(len(data)),
				SHA256:          hex.EncodeToString(digest[:]),
			})
		}
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return fmt.Errorf("ttscorpus: enumerate corpus: %w", err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.EqualFold(filepath.Ext(entry.Name()), ".wav") {
			continue
		}
		declared := false
		for _, file := range files {
			if file.Path == entry.Name() {
				declared = true
				break
			}
		}
		if !declared {
			return fmt.Errorf("ttscorpus: unmanifested generated audio %q", entry.Name())
		}
	}
	if total >= MaxCorpusBytes {
		return fmt.Errorf("ttscorpus: corpus totals %d bytes, over the %d byte budget", total, MaxCorpusBytes)
	}
	manifestBytes, err := json.MarshalIndent(corpusManifest{
		SchemaVersion:   1,
		CorpusByteTotal: total,
		SampleRatesHz:   SampleRates,
		Files:           files,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("ttscorpus: encode manifest: %w", err)
	}
	manifestBytes = append(manifestBytes, '\n')
	if err := os.WriteFile(filepath.Join(outputDir, "manifest.json"), manifestBytes, 0o644); err != nil {
		return fmt.Errorf("ttscorpus: write manifest.json: %w", err)
	}
	return nil
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
