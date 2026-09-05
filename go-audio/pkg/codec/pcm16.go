// Package codec contains bounded audio payload codecs shared by providers,
// transports, and the audio engine. Provider envelopes and transport headers
// remain owned by their respective adapters.
package codec

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	// MaxPayloadBytes bounds decoded audio payloads accepted from a remote
	// provider. The limit is deliberately large enough for ordinary audio
	// chunks while preventing a single malformed event from allocating without
	// bound.
	MaxPayloadBytes = 4 << 20
	// MaxPCM16Bytes is the default bound for a PCM16 payload.
	MaxPCM16Bytes = MaxPayloadBytes
)

var (
	ErrPayloadTooLarge     = errors.New("audio payload exceeds codec limit")
	ErrInvalidBase64       = errors.New("audio payload is not valid base64")
	ErrPCM16OddLength      = errors.New("PCM16 payload has odd byte length")
	ErrPCM16BufferTooSmall = errors.New("PCM16 destination buffer is too small")
	ErrInvalidPayloadLimit = errors.New("audio payload limit must be positive")
)

// EncodePCM16 returns signed little-endian PCM16 bytes. The returned buffer is
// independent of samples. Call EncodePCM16Into when the destination is
// already allocated and its size can be checked by the caller.
func EncodePCM16(samples []int16) []byte {
	if len(samples) == 0 {
		return nil
	}
	encoded := make([]byte, len(samples)*2)
	// This lossless convenience API intentionally does not apply the remote
	// payload limit. Use EncodePCM16WithLimit for network input boundaries.
	_ = encodePCM16Into(encoded, samples)
	return encoded
}

// EncodePCM16WithLimit is EncodePCM16 with an explicit decoded-byte bound.
func EncodePCM16WithLimit(samples []int16, maxBytes int) ([]byte, error) {
	if err := validateLimit(maxBytes); err != nil {
		return nil, err
	}
	if len(samples) > maxBytes/2 {
		return nil, fmt.Errorf("%w: %d bytes exceeds %d", ErrPayloadTooLarge, len(samples)*2, maxBytes)
	}
	if len(samples) == 0 {
		return nil, nil
	}
	encoded := make([]byte, len(samples)*2)
	if err := encodePCM16Into(encoded, samples); err != nil {
		return nil, err
	}
	return encoded, nil
}

// EncodePCM16Into writes samples to destination and rejects an undersized
// destination. It does not retain either argument.
func EncodePCM16Into(destination []byte, samples []int16) error {
	return encodePCM16Into(destination, samples)
}

func encodePCM16Into(destination []byte, samples []int16) error {
	if len(destination) < len(samples)*2 {
		return fmt.Errorf("%w: got %d bytes, want %d", ErrPCM16BufferTooSmall, len(destination), len(samples)*2)
	}
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(destination[index*2:], uint16(sample))
	}
	return nil
}

// DecodePCM16 validates alignment and returns fresh signed little-endian
// PCM16 samples. Empty input is accepted as an empty audio chunk.
func DecodePCM16(encoded []byte) ([]int16, error) {
	return DecodePCM16WithLimit(encoded, MaxPCM16Bytes)
}

// ValidatePCM16 checks the size constraints for a PCM16 payload without
// allocating or decoding its samples.
func ValidatePCM16(encoded []byte, maxBytes int) error {
	if err := validateLimit(maxBytes); err != nil {
		return err
	}
	if len(encoded) > maxBytes {
		return fmt.Errorf("%w: %d bytes exceeds %d", ErrPayloadTooLarge, len(encoded), maxBytes)
	}
	if len(encoded)%2 != 0 {
		return fmt.Errorf("%w: got %d bytes", ErrPCM16OddLength, len(encoded))
	}
	return nil
}

// DecodePCM16WithLimit is DecodePCM16 with an explicit decoded-byte bound.
func DecodePCM16WithLimit(encoded []byte, maxBytes int) ([]int16, error) {
	if err := ValidatePCM16(encoded, maxBytes); err != nil {
		return nil, err
	}
	samples := make([]int16, len(encoded)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(encoded[index*2:]))
	}
	return samples, nil
}

// DecodePCM16Into decodes encoded into destination. Destination must have at
// least len(encoded)/2 entries. Extra entries are left untouched.
func DecodePCM16Into(destination []int16, encoded []byte) error {
	if len(encoded)%2 != 0 {
		return fmt.Errorf("%w: got %d bytes", ErrPCM16OddLength, len(encoded))
	}
	if len(destination) < len(encoded)/2 {
		return fmt.Errorf("%w: got %d samples, want %d", ErrPCM16BufferTooSmall, len(destination), len(encoded)/2)
	}
	for index := 0; index < len(encoded)/2; index++ {
		destination[index] = int16(binary.LittleEndian.Uint16(encoded[index*2:]))
	}
	return nil
}

// EncodeBase64 returns standard padded base64 for an audio payload.
func EncodeBase64(payload []byte) string {
	return base64.StdEncoding.EncodeToString(payload)
}

// DecodeBase64 validates and decodes a standard padded base64 audio payload,
// enforcing MaxPayloadBytes on decoded data.
func DecodeBase64(encoded string) ([]byte, error) {
	return DecodeBase64WithLimit(encoded, MaxPayloadBytes)
}

// DecodeLegacyBase64 accepts the four base64 spellings historically emitted
// by room replay fixtures: padded/unpadded standard and URL-safe alphabets.
// New provider and transport payloads should use DecodeBase64.
func DecodeLegacyBase64(encoded string) ([]byte, error) {
	return DecodeLegacyBase64WithLimit(encoded, MaxPayloadBytes)
}

// DecodeLegacyBase64WithLimit is DecodeLegacyBase64 with an explicit decoded
// byte bound.
func DecodeLegacyBase64WithLimit(encoded string, maxBytes int) ([]byte, error) {
	if err := validateLimit(maxBytes); err != nil {
		return nil, err
	}
	if encoded == "" {
		return nil, nil
	}
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	var lastErr error
	for _, encoding := range encodings {
		if encodedLengthExceedsLimit(len(encoded), maxBytes) {
			return nil, fmt.Errorf("%w: encoded length %d exceeds limit for %d bytes", ErrPayloadTooLarge, len(encoded), maxBytes)
		}
		decoded, err := encoding.DecodeString(encoded)
		if err == nil {
			if len(decoded) > maxBytes {
				return nil, fmt.Errorf("%w: %d bytes exceeds %d", ErrPayloadTooLarge, len(decoded), maxBytes)
			}
			return decoded, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("%w: %v", ErrInvalidBase64, lastErr)
}

// DecodeBase64WithLimit is DecodeBase64 with an explicit decoded-byte bound.
func DecodeBase64WithLimit(encoded string, maxBytes int) ([]byte, error) {
	if err := validateLimit(maxBytes); err != nil {
		return nil, err
	}
	if encoded == "" {
		return nil, nil
	}
	// Reject oversized inputs before DecodeString allocates its output. This
	// estimate intentionally errs on the strict side for malformed padding.
	if encodedLengthExceedsLimit(len(encoded), maxBytes) {
		return nil, fmt.Errorf("%w: encoded length %d exceeds limit for %d bytes", ErrPayloadTooLarge, len(encoded), maxBytes)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidBase64, err)
	}
	if len(decoded) > maxBytes {
		return nil, fmt.Errorf("%w: %d bytes exceeds %d", ErrPayloadTooLarge, len(decoded), maxBytes)
	}
	return decoded, nil
}

func encodedLengthExceedsLimit(encodedLength, maxBytes int) bool {
	maxInt := int(^uint(0) >> 1)
	if maxBytes > maxInt/4*3-2 {
		return false
	}
	return encodedLength > (maxBytes+2)/3*4
}

// EncodePCM16Base64 encodes signed little-endian PCM16 as standard padded
// base64. Empty samples produce an empty string.
func EncodePCM16Base64(samples []int16) string {
	return EncodeBase64(EncodePCM16(samples))
}

// EncodePCM16Base64WithLimit combines bounded PCM16 and base64 encoding.
func EncodePCM16Base64WithLimit(samples []int16, maxBytes int) (string, error) {
	encoded, err := EncodePCM16WithLimit(samples, maxBytes)
	if err != nil {
		return "", err
	}
	return EncodeBase64(encoded), nil
}

// DecodePCM16Base64 decodes a bounded base64 PCM16 payload into fresh samples.
func DecodePCM16Base64(encoded string) ([]int16, error) {
	return DecodePCM16Base64WithLimit(encoded, MaxPCM16Bytes)
}

// DecodePCM16Base64WithLimit combines base64 and PCM16 validation with an
// explicit decoded-byte bound.
func DecodePCM16Base64WithLimit(encoded string, maxBytes int) ([]int16, error) {
	decoded, err := DecodeBase64WithLimit(encoded, maxBytes)
	if err != nil {
		return nil, err
	}
	return DecodePCM16WithLimit(decoded, maxBytes)
}

func validateLimit(maxBytes int) error {
	if maxBytes <= 0 {
		return fmt.Errorf("%w: %d", ErrInvalidPayloadLimit, maxBytes)
	}
	return nil
}
