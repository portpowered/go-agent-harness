package testkit

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

var ErrIDExhausted = errors.New("webmcp testkit: deterministic ID source exhausted")

// DeterministicIDSource produces reproducible, collision-free IDs for both
// the semantic recorder and the low-level browser runtime. Tool refs use the
// exact 16-byte, unpadded base64url token required by the C0 contract.
type DeterministicIDSource struct {
	mu             sync.Mutex
	prefix         string
	next           uint64
	nextRef        uint64
	nextInvocation uint64
}

// DeterministicIDs is the descriptive low-level runtime name retained for
// callers of the broker test seams.
type DeterministicIDs = DeterministicIDSource

// NewDeterministicIDSource accepts an optional recorder prefix. Omitting it
// preserves the low-level runtime constructor's default behavior.
func NewDeterministicIDSource(prefix ...string) *DeterministicIDSource {
	value := "fixture"
	if len(prefix) > 0 {
		value = normalizeIDPart(prefix[0])
		if value == "" {
			value = "fixture"
		}
	}
	return &DeterministicIDSource{prefix: value}
}

func NewDeterministicIDs() *DeterministicIDs { return NewDeterministicIDSource() }

// NewFakeIDs is a descriptive alias for NewDeterministicIDSource.
func NewFakeIDs(prefix string) *DeterministicIDSource {
	return NewDeterministicIDSource(prefix)
}

func (s *DeterministicIDSource) NextID(kind string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	kind = normalizeIDPart(kind)
	if kind == "" {
		kind = "id"
	}
	return fmt.Sprintf("%s-%s-%03d", s.prefix, kind, s.next)
}

func (s *DeterministicIDSource) NewToolRef() (webmcp.ToolRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextRef == math.MaxUint64 {
		return "", ErrIDExhausted
	}
	s.nextRef++
	var token [16]byte
	binary.BigEndian.PutUint64(token[8:], s.nextRef)
	return webmcp.ToolRef(webmcp.ToolRefPrefix + base64.RawURLEncoding.EncodeToString(token[:])), nil
}

func (s *DeterministicIDSource) NewInvocationID() (webmcp.InvocationID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextInvocation == math.MaxUint64 {
		return "", ErrIDExhausted
	}
	s.nextInvocation++
	return webmcp.InvocationID(fmt.Sprintf("inv-%06d", s.nextInvocation)), nil
}

var _ IDSource = (*DeterministicIDSource)(nil)
var _ webmcp.IDSource = (*DeterministicIDSource)(nil)
