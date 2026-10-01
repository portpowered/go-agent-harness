// Command ttscorpus generates the pinned qwen3-tts test-audio corpus.
//
// It refuses to run anywhere except linux/amd64, verifies both pinned GGUF
// checksums before any synthesis, brings up readiness against the pinned
// LocalAI backend, synthesizes the closed utterance set at session sample
// rates, validates clip sanity, and emits manifest.json under 25 MB.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/ttscorpus"
)

func main() {
	modelsRoot := flag.String("models", os.Getenv("LOCALAI_MODELS_DIR"), "LocalAI models directory containing qwen3-tts-cpp/{talker,tokenizer} GGUFs")
	endpoint := flag.String("endpoint", ttscorpus.DefaultEndpoint, "base URL of the pinned LocalAI backend")
	output := flag.String("output", "", "directory for generated WAVs and manifest.json")
	flag.Parse()
	if err := run(context.Background(), os.Stdout, *modelsRoot, *endpoint, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run generates the corpus, reporting progress lines to stdout.
func run(ctx context.Context, stdout io.Writer, modelsRoot, endpoint, output string) error {
	if err := ttscorpus.CheckPlatform(); err != nil {
		return err
	}
	if output == "" {
		return fmt.Errorf("ttscorpus: -output is required")
	}
	verified, err := ttscorpus.VerifyArtifacts(modelsRoot)
	if err != nil {
		return err
	}
	for _, artifact := range verified {
		if _, err := fmt.Fprintf(stdout, "ARTIFACT_HASH=PASS role=%s sha256=%s\n", artifact.Role, artifact.Actual); err != nil {
			return fmt.Errorf("ttscorpus: write progress: %w", err)
		}
	}
	generator := ttscorpus.NewGenerator(endpoint)
	if err := generator.WaitReady(ctx); err != nil {
		return err
	}
	if err := synthesizeCorpus(ctx, stdout, generator, output); err != nil {
		return err
	}
	if err := ttscorpus.EmitManifest(output); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, "CORPUS=PASS"); err != nil {
		return fmt.Errorf("ttscorpus: write result: %w", err)
	}
	return nil
}

// synthesizeCorpus writes every utterance at every session sample rate into
// output, reporting each clip to stdout.
func synthesizeCorpus(ctx context.Context, stdout io.Writer, generator *ttscorpus.Generator, output string) error {
	for i, text := range ttscorpus.Utterances() {
		for _, rate := range ttscorpus.SampleRates() {
			name := ttscorpus.ClipName(i, rate)
			if _, err := fmt.Fprintf(stdout, "SYNTHESIZE file=%s rate=%d\n", name, rate); err != nil {
				return fmt.Errorf("ttscorpus: write progress: %w", err)
			}
			if err := generator.Synthesize(ctx, text, filepath.Join(output, name)); err != nil {
				return err
			}
		}
	}
	return nil
}
