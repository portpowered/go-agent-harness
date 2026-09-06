// Package wavio reads and writes the supported PCM16 mono WAV contract.
//
// The package accepts only little-endian RIFF/WAVE PCM audio with one channel,
// 16 bits per sample, and a 16 kHz or 24 kHz sample rate. Read and Write use
// caller-owned streams and never close them. Empty audio is intentionally not
// representable: Write rejects an empty sample slice and Read rejects a WAV
// data chunk with a zero length.
package wavio
