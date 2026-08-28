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

// DeterministicIDs produces reproducible, collision-free IDs. Tool refs use
// the exact 16-byte, unpadded base64url token required by the C0 contract.
type DeterministicIDs struct {
	mu             sync.Mutex
	nextRef        uint64
	nextInvocation uint64
}

func NewDeterministicIDs() *DeterministicIDs { return &DeterministicIDs{} }

// NewDeterministicIDSource is a descriptive constructor alias.
func NewDeterministicIDSource() *DeterministicIDs { return NewDeterministicIDs() }

func (s *DeterministicIDs) NewToolRef() (webmcp.ToolRef, error) {
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

func (s *DeterministicIDs) NewInvocationID() (webmcp.InvocationID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextInvocation == math.MaxUint64 {
		return "", ErrIDExhausted
	}
	s.nextInvocation++
	return webmcp.InvocationID(fmt.Sprintf("inv-%06d", s.nextInvocation)), nil
}

var _ webmcp.IDSource = (*DeterministicIDs)(nil)
