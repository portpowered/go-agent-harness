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

func errorLeaves(err error) ([]error, bool) {
	if err == nil {
		return nil, true
	}
	leaves := make([]error, 0, 2)
	activePointers := make(map[errorIdentity]struct{})
	activeValues := make(map[error]struct{})
	nodes := 0
	var walk func(error) bool
	walk = func(current error) bool {
		if current == nil {
			return true
		}
		if nodes >= maxErrorTraversalNodes || typedNilError(current) {
			return false
		}
		nodes++
		if key, ok := pointerErrorIdentity(current); ok {
			if _, exists := activePointers[key]; exists {
				return false
			}
			activePointers[key] = struct{}{}
			defer delete(activePointers, key)
		} else if reflect.TypeOf(current).Comparable() {
			if _, exists := activeValues[current]; exists {
				return false
			}
			activeValues[current] = struct{}{}
			defer delete(activeValues, current)
		}

		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			children := joined.Unwrap()
			if len(children) == 0 {
				return false
			}
			for _, child := range children {
				if !walk(child) {
					return false
				}
			}
			return true
		}
		if unwrapped := errors.Unwrap(current); unwrapped != nil {
			return walk(unwrapped)
		}
		leaves = append(leaves, current)
		return true
	}
	return leaves, walk(err)
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
