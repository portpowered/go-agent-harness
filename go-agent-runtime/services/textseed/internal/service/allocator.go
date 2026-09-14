package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"
)

type sequenceAllocator struct {
	prefix string
	next   atomic.Uint64
}

func newSequenceAllocator(prefix string) *sequenceAllocator {
	return &sequenceAllocator{prefix: prefix}
}

func (a *sequenceAllocator) Allocate() string {
	if a == nil {
		return ""
	}
	return a.prefix + strconv.FormatUint(a.next.Add(1), 10)
}

func newDefaultAllocator() *sequenceAllocator {
	a := newSequenceAllocator("")
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err == nil {
		a.prefix = fmt.Sprintf("\x00agent-cli-session-text-seed:%s:%p:", hex.EncodeToString(nonce[:]), a)
		return a
	}
	// A crypto source is available on supported Go platforms. Keep a
	// constructor-local fallback for constrained test hosts without a shared
	// counter or hidden initialization; the live allocator address prevents
	// two simultaneously live services from sharing the fallback namespace.
	a.prefix = fmt.Sprintf("\x00agent-cli-session-text-seed:%d:%p:", time.Now().UnixNano(), a)
	return a
}
