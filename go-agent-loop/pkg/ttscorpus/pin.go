package ttscorpus

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type fixtureError string

func (e fixtureError) Error() string { return string(e) }

// Artifact is one verified pinned model file.
type Artifact struct {
	Role     string
	Path     string
	Expected string
	Actual   string
}

// VerifyArtifacts verifies both pinned GGUFs under modelsRoot against the
// immutable checksums from docs/architecture/s2s-tts-pinning.md.
func VerifyArtifacts(modelsRoot string) ([]Artifact, error) {
	return VerifyArtifactsWithPins(modelsRoot, TalkerArtifactSHA256, TokenizerArtifactSHA256)
}

// VerifyArtifactsWithPins verifies both GGUFs against caller-provided pins;
// production callers use VerifyArtifacts so the immutable doc pins apply.
func VerifyArtifactsWithPins(modelsRoot, talkerSHA256, tokenizerSHA256 string) ([]Artifact, error) {
	if strings.TrimSpace(modelsRoot) == "" {
		return nil, fmt.Errorf("ttscorpus: models root is required; set it to the LocalAI models directory (see %s)", PinDocPath)
	}
	for _, legacy := range findLegacyF16(modelsRoot) {
		return nil, fmt.Errorf("ttscorpus: refusing legacy endo5501 F16 weights %q (gallery %s, sha256 %s): incompatible with the qwen3-tts-cpp backend; migrate with 'local-ai models install qwen3-tts-cpp' per %s", legacy, LegacyF16GalleryURI, LegacyF16SHA256, PinDocPath)
	}
	pinned := []Artifact{
		{Role: "talker", Path: TalkerArtifactPath, Expected: talkerSHA256},
		{Role: "tokenizer", Path: TokenizerArtifactPath, Expected: tokenizerSHA256},
	}
	verified := make([]Artifact, 0, len(pinned))
	for _, artifact := range pinned {
		path := filepath.Join(modelsRoot, filepath.FromSlash(artifact.Path))
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("ttscorpus: read pinned %s artifact %q: %w; install with 'local-ai models install qwen3-tts-cpp' per %s", artifact.Role, artifact.Path, err, PinDocPath)
		}
		digest := sha256.Sum256(data)
		artifact.Actual = hex.EncodeToString(digest[:])
		if artifact.Actual != artifact.Expected {
			return nil, &HashMismatchError{
				fixtureError: fixtureError(fmt.Sprintf("ttscorpus: pinned %s artifact %q hash mismatch: expected %s got %s; see %s", artifact.Role, artifact.Path, artifact.Expected, artifact.Actual, PinDocPath)),
				Role:         artifact.Role,
				Path:         artifact.Path,
				Expected:     artifact.Expected,
				Actual:       artifact.Actual,
			}
		}
		verified = append(verified, artifact)
	}
	return verified, nil
}

// HashMismatchError reports a pinned GGUF whose observed checksum differs from
// the immutable pin.
type HashMismatchError struct {
	fixtureError
	Role     string
	Path     string
	Expected string
	Actual   string
}

func findLegacyF16(modelsRoot string) []string {
	var found []string
	_ = filepath.WalkDir(modelsRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		if strings.EqualFold(entry.Name(), LegacyF16Filename) {
			found = append(found, path)
		}
		return nil
	})
	return found
}
