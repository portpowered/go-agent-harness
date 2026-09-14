package service

import (
	"context"
	"errors"
	"reflect"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/terminaloutcome"
)

const maxErrorTraversalNodes = 128

func hasIndependentFailure(err error, ignored ...error) bool {
	if err == nil {
		return false
	}
	leaves, bounded := errorLeaves(err)
	if !bounded {
		return true
	}
	for _, leaf := range leaves {
		if leaf == nil || errors.Is(leaf, context.Canceled) || errors.Is(leaf, context.DeadlineExceeded) ||
			errors.Is(leaf, terminaloutcome.ErrSessionMaxDurationExpired) || hasIgnoredError(leaf, ignored) {
			continue
		}
		return true
	}
	return false
}

func hasIgnoredError(leaf error, ignored []error) bool {
	for _, sentinel := range ignored {
		if sentinel != nil && errors.Is(leaf, sentinel) {
			return true
		}
	}
	return false
}

func sessionErrorIsCancellation(err error) bool {
	if err == nil || hasIndependentFailure(err) {
		return false
	}
	leaves, bounded := errorLeaves(err)
	if !bounded {
		return false
	}
	for _, leaf := range leaves {
		if errors.Is(leaf, context.Canceled) || errors.Is(leaf, context.DeadlineExceeded) {
			return true
		}
	}
	return false
}

type errorIdentity struct {
	typ  reflect.Type
	kind reflect.Kind
	ptr  uintptr
}

type errorWalkState struct {
	leaves          []error
	activePointers  map[errorIdentity]struct{}
	activeValueKeys map[error]struct{}
	nodes           int
}

func errorLeaves(err error) ([]error, bool) {
	if err == nil {
		return nil, true
	}
	state := errorWalkState{
		leaves:          make([]error, 0, 2),
		activePointers:  make(map[errorIdentity]struct{}),
		activeValueKeys: make(map[error]struct{}),
	}
	bounded := state.walk(err)
	return state.leaves, bounded
}

func (s *errorWalkState) walk(current error) bool {
	if current == nil {
		return true
	}
	if s.nodes >= maxErrorTraversalNodes || typedNilError(current) {
		return false
	}
	s.nodes++
	if key, ok := pointerErrorIdentity(current); ok {
		return s.walkPointer(current, key)
	}
	if reflect.TypeOf(current).Comparable() {
		return s.walkComparable(current)
	}
	return s.walkChildren(current)
}

func (s *errorWalkState) walkPointer(current error, key errorIdentity) bool {
	if _, exists := s.activePointers[key]; exists {
		return false
	}
	s.activePointers[key] = struct{}{}
	defer delete(s.activePointers, key)
	return s.walkChildren(current)
}

func (s *errorWalkState) walkComparable(current error) bool {
	if _, exists := s.activeValueKeys[current]; exists {
		return false
	}
	s.activeValueKeys[current] = struct{}{}
	defer delete(s.activeValueKeys, current)
	return s.walkChildren(current)
}

func (s *errorWalkState) walkChildren(current error) bool {
	if joined, ok := current.(interface{ Unwrap() []error }); ok {
		return s.walkJoined(joined.Unwrap())
	}
	if unwrapped := errors.Unwrap(current); unwrapped != nil {
		return s.walk(unwrapped)
	}
	s.leaves = append(s.leaves, current)
	return true
}

func (s *errorWalkState) walkJoined(children []error) bool {
	if len(children) == 0 {
		return false
	}
	for _, child := range children {
		if !s.walk(child) {
			return false
		}
	}
	return true
}

func pointerErrorIdentity(err error) (errorIdentity, bool) {
	value := reflect.ValueOf(err)
	if !value.IsValid() {
		return errorIdentity{}, false
	}
	kind := value.Kind()
	if kind != reflect.Pointer && kind != reflect.Chan && kind != reflect.Func && kind != reflect.UnsafePointer {
		return errorIdentity{}, false
	}
	if value.IsNil() {
		return errorIdentity{}, false
	}
	return errorIdentity{typ: value.Type(), kind: kind, ptr: value.Pointer()}, true
}

func typedNilError(err error) bool {
	value := reflect.ValueOf(err)
	if !value.IsValid() {
		return false
	}
	kind := value.Kind()
	if kind != reflect.Chan && kind != reflect.Func && kind != reflect.Interface && kind != reflect.Map && kind != reflect.Pointer && kind != reflect.Slice {
		return false
	}
	return value.IsNil()
}
