// Package ttscorpus generates the pinned qwen3-tts test-audio corpus: it
// verifies pinned GGUF checksums before synthesis, refuses legacy weights,
// guards the linux/amd64 platform requirement, synthesizes a closed utterance
// set from the pinned LocalAI backend, validates clip sanity, and emits a
// hash-asserted manifest.json.
package ttscorpus

// Pin values come from docs/architecture/s2s-tts-pinning.md and
// deploy/localai/models/qwen3-tts-pinned.json. They are immutable.
const (
	// TalkerArtifactPath is the talker GGUF path relative to the LocalAI models root.
	TalkerArtifactPath = "qwen3-tts-cpp/qwen-talker-0.6b-base-Q8_0.gguf"
	// TalkerArtifactSHA256 is the pinned talker checksum.
	TalkerArtifactSHA256 = "d54dbaf10591421fa764ed630d764efa717ae40cd959bd48c66d4eb1af226426"
	// TokenizerArtifactPath is the tokenizer/codec GGUF path relative to the models root.
	TokenizerArtifactPath = "qwen3-tts-cpp/qwen-tokenizer-12hz-Q8_0.gguf"
	// TokenizerArtifactSHA256 is the pinned tokenizer/codec checksum.
	TokenizerArtifactSHA256 = "1883beeed99348fc35e23dd225e9082f93f6f8c109330a33d935baa8acdbfd94"
	// LegacyF16GalleryURI is the refused pre-migration endo5501 F16 artifact.
	LegacyF16GalleryURI = "huggingface://endo5501/qwen3-tts.cpp/qwen3-tts-0.6b-f16.gguf"
	// LegacyF16SHA256 is the checksum of the incompatible legacy F16 weights.
	LegacyF16SHA256 = "0b89770118463af8f2467d824a8de57d96df6a09f927a9769a3f7b7fffa7087d"
	// LegacyF16Filename is the on-disk name of the incompatible legacy weights.
	LegacyF16Filename = "qwen3-tts-0.6b-f16.gguf"
	// PinDocPath points maintainers at the pin contract when verification fails.
	PinDocPath = "docs/architecture/s2s-tts-pinning.md"
)
