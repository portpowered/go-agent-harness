//go:build linux

package testnet

import "syscall"

// segmentSizeEnforced reports whether this platform applies WANSegmentBytes.
const segmentSizeEnforced = true

func setWANSegmentSize(fd uintptr) error {
	return syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_MAXSEG, WANSegmentBytes)
}

func segmentBytes(fd uintptr) (int, error) {
	return syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_MAXSEG)
}
