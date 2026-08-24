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
	"os"
	"path/filepath"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/ttscorpus"
)

func main() {
	modelsRoot := flag.String("models", os.Getenv("LOCALAI_MODELS_DIR"), "LocalAI models directory containing qwen3-tts-cpp/{talker,tokenizer} GGUFs")
	endpoint := flag.String("endpoint", ttscorpus.DefaultEndpoint, "base URL of the pinned LocalAI backend")
	output := flag.String("output", "", "directory for generated WAVs and manifest.json")
	flag.Parse()
	if err := run(*modelsRoot, *endpoint, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(modelsRoot, endpoint, output string) error {
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
		fmt.Printf("ARTIFACT_HASH=PASS role=%s sha256=%s\n", artifact.Role, artifact.Actual)
	}
	generator := ttscorpus.NewGenerator(endpoint)
	ctx := context.Background()
	if err := generator.WaitReady(ctx); err != nil {
		return err
	}
	for i, text := range ttscorpus.Utterances {
		for _, rate := range ttscorpus.SampleRates {
			name := fmt.Sprintf("qwen_utt%02d_%dk.wav", i+1, rate/1000)
			path := filepath.Join(output, name)
			fmt.Printf("SYNTHESIZE file=%s rate=%d\n", name, rate)
			if err := generator.Synthesize(ctx, text, path); err != nil {
				return err
			}
		}
	}
	if err := ttscorpus.EmitManifest(output); err != nil {
		return err
	}
	fmt.Println("CORPUS=PASS")
	return nil
}
