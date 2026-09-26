//go:build !linux

package testnet

// segmentSizeEnforced reports whether this platform applies WANSegmentBytes.
const segmentSizeEnforced = false

func setWANSegmentSize(uintptr) error { return nil }

func segmentBytes(uintptr) (int, error) { return 0, nil }
