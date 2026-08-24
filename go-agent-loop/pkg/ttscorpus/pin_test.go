package ttscorpus

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	talkerSeed    = "talker-gguf-payload"
	tokenizerSeed = "tokenizer-gguf-payload"
	corruptSeed   = "corrupted-gguf-payload"
)

func seedHash(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}

func TestVerifyArtifactsPassesMatchingFiles(t *testing.T) {
	root := writeModelRoot(t, talkerSeed, tokenizerSeed)
	artifacts, err := VerifyArtifactsWithPins(root, seedHash(talkerSeed), seedHash(tokenizerSeed))
	if err != nil {
		t.Fatalf("VerifyArtifacts() error = %v", err)
	}
	want := []Artifact{
		{Role: "talker", Path: TalkerArtifactPath, Expected: seedHash(talkerSeed), Actual: seedHash(talkerSeed)},
		{Role: "tokenizer", Path: TokenizerArtifactPath, Expected: seedHash(tokenizerSeed), Actual: seedHash(tokenizerSeed)},
	}
	if len(artifacts) != 2 || artifacts[0].Role != want[0].Role || artifacts[1].Actual != want[1].Actual {
		t.Fatalf("VerifyArtifacts() = %+v", artifacts)
	}
}

func TestVerifyArtifactsUsesImmutableDocPins(t *testing.T) {
	for _, pin := range []string{TalkerArtifactSHA256, TokenizerArtifactSHA256} {
		if len(pin) != 64 || strings.ToLower(pin) != pin {
			t.Fatalf("pinned checksum constant %q must be lowercase SHA-256 hex", pin)
		}
	}
}

func TestVerifyArtifactsRejectsCorruption(t *testing.T) {
	pins := map[string]string{"talker": seedHash(talkerSeed), "tokenizer": seedHash(tokenizerSeed)}
	seeds := map[string]string{"talker": talkerSeed, "tokenizer": tokenizerSeed}
	for role, pin := range pins {
		t.Run(role, func(t *testing.T) {
			actualSeeds := map[string]string{"talker": seeds["talker"], "tokenizer": seeds["tokenizer"]}
			actualSeeds[role] = corruptSeed
			root := writeModelRoot(t, actualSeeds["talker"], actualSeeds["tokenizer"])
			_, err := VerifyArtifactsWithPins(root, pins["talker"], pins["tokenizer"])
			var mismatch *HashMismatchError
			if err == nil || !errors.As(err, &mismatch) {
				t.Fatalf("VerifyArtifacts() error = %v; want hash mismatch", err)
			}
			if mismatch.Expected != pin || mismatch.Actual != seedHash(corruptSeed) || mismatch.Actual == mismatch.Expected {
				t.Fatalf("mismatch = expected %q actual %q", mismatch.Expected, mismatch.Actual)
			}
			if !strings.Contains(err.Error(), mismatch.Expected) || !strings.Contains(err.Error(), mismatch.Actual) {
				t.Fatalf("error %v must name expected and actual hashes", err)
			}
			if !strings.Contains(err.Error(), PinDocPath) {
				t.Fatalf("error %v must point at the pin doc", err)
			}
		})
	}
}

func TestVerifyArtifactsRejectsLegacyF16(t *testing.T) {
	root := writeModelRoot(t, talkerSeed, tokenizerSeed)
	mustWrite(t, filepath.Join(root, LegacyF16Filename), []byte("legacy f16 weights"))
	_, err := VerifyArtifactsWithPins(root, seedHash(talkerSeed), seedHash(tokenizerSeed))
	if err == nil {
		t.Fatal("VerifyArtifacts() accepted legacy F16 weights")
	}
	for _, want := range []string{LegacyF16Filename, LegacyF16GalleryURI, PinDocPath} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %v must mention %q", err, want)
		}
	}
}

func TestCheckPlatform(t *testing.T) {
	if err := checkPlatform("linux", "amd64"); err != nil {
		t.Fatalf("checkPlatform(linux, amd64) = %v", err)
	}
	err := checkPlatform("darwin", "arm64")
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("checkPlatform(darwin, arm64) = %v", err)
	}
	for _, want := range []string{"darwin/arm64", "linux/amd64"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %v must mention %q", err, want)
		}
	}
}

func writeModelRoot(t *testing.T, talkerSeed, tokenizerSeed string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "qwen3-tts-cpp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, filepath.Base(TalkerArtifactPath)), []byte(talkerSeed))
	mustWrite(t, filepath.Join(dir, filepath.Base(TokenizerArtifactPath)), []byte(tokenizerSeed))
	return root
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
