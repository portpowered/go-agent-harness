package ttscorpus

import (
	"errors"
	"fmt"
	"runtime"
)

// ErrUnsupportedPlatform marks hosts that cannot run the pinned backend.
var ErrUnsupportedPlatform = errors.New("ttscorpus: pinned qwen3-tts backend platform mismatch")

// RequiredPlatform describes the only host platform able to run the pinned
// LocalAI image.
const (
	RequiredGOOS   = "linux"
	RequiredGOARCH = "amd64"
)

// CheckPlatform fails loudly on hosts other than linux/amd64, naming the
// observed platform and how to run the generator where the pinned image runs.
// It never silently skips generation and never substitutes another synthesizer.
func CheckPlatform() error {
	return checkPlatform(runtime.GOOS, runtime.GOARCH)
}

func checkPlatform(goos, goarch string) error {
	if goos == RequiredGOOS && goarch == RequiredGOARCH {
		return nil
	}
	return fmt.Errorf("%w: pinned LocalAI v4.8.2 qwen3-tts-cpp image is linux/amd64 but this host is %s/%s; run scripts/generate-audio-corpus.sh on a linux/amd64 host or in a linux/amd64 container (emulation such as Rosetta/QEMU is unsupported and must not fabricate audio); see docs/architecture/s2s-tts-pinning.md", ErrUnsupportedPlatform, goos, goarch)
}
