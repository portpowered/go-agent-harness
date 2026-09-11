package service

import (
	"fmt"
	"reflect"
	"strings"
)

// Browser conversation IDs and invocation states are intentionally opaque at
// this boundary. Hosts can retain their named string values without bringing
// a browser transport package into the runtime contract.
const (
	browserConversationStaleToolRef           = "stale_tool_ref"
	browserConversationInvocationCompleted    = "completed"
	browserConversationInvocationError        = "error"
	browserConversationInvocationCanceled     = "canceled"
	browserConversationInvocationTimedOut     = "timed_out"
	browserConversationInvocationOrphaned     = "orphaned"
	browserConversationInvocationPolicyDenied = "policy_denied"
	browserConversationMaxSafeTextBytes       = 256
)

func browserConversationOpaqueString(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func safeBrowserConversationError(err error) string {
	if err == nil {
		return ""
	}
	return safeBrowserConversationText(err.Error())
}

func safeBrowserConversationText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if browserConversationContainsCredentialMarker(value) {
		return "[redacted]"
	}
	var builder strings.Builder
	for _, char := range value {
		switch char {
		case '\n', '\r', '\t':
			builder.WriteByte(' ')
		default:
			if char < 0x20 || char == 0x7f {
				builder.WriteByte(' ')
			} else {
				builder.WriteRune(char)
			}
		}
		if builder.Len() >= browserConversationMaxSafeTextBytes {
			break
		}
	}
	result := strings.TrimSpace(builder.String())
	if result == "" {
		return "unknown error"
	}
	return result
}

func browserConversationOpaqueLen(value any) int {
	if value == nil {
		return 0
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return 0
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Array, reflect.Slice, reflect.Map, reflect.String:
		return rv.Len()
	case reflect.Invalid, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128,
		reflect.Chan, reflect.Func, reflect.Interface, reflect.Pointer, reflect.Struct, reflect.UnsafePointer:
		return 0
	}
	return 0
}

func cloneBrowserConversationOpaque(value any) any {
	if value == nil {
		return nil
	}
	cloned := cloneBrowserConversationReflect(reflect.ValueOf(value))
	if !cloned.IsValid() {
		return nil
	}
	return cloned.Interface()
}

func cloneBrowserConversationReflect(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return reflect.Value{}
	}
	switch value.Kind() {
	case reflect.Interface:
		return cloneBrowserConversationInterface(value)
	case reflect.Pointer:
		return cloneBrowserConversationPointer(value)
	case reflect.Slice:
		return cloneBrowserConversationSlice(value)
	case reflect.Array:
		return cloneBrowserConversationArray(value)
	case reflect.Map:
		return cloneBrowserConversationMap(value)
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return value
	case reflect.Invalid, reflect.Complex64, reflect.Complex128, reflect.Chan, reflect.Func,
		reflect.Struct, reflect.UnsafePointer:
		return value
	}
	return reflect.Value{}
}

func cloneBrowserConversationInterface(value reflect.Value) reflect.Value {
	if value.IsNil() {
		return reflect.Zero(value.Type())
	}
	cloned := cloneBrowserConversationReflect(value.Elem())
	wrapped := reflect.New(value.Type()).Elem()
	wrapped.Set(cloned)
	return wrapped
}

func cloneBrowserConversationPointer(value reflect.Value) reflect.Value {
	if value.IsNil() {
		return reflect.Zero(value.Type())
	}
	cloned := reflect.New(value.Type().Elem())
	cloned.Elem().Set(cloneBrowserConversationReflect(value.Elem()))
	return cloned
}

func cloneBrowserConversationSlice(value reflect.Value) reflect.Value {
	if value.IsNil() {
		return reflect.Zero(value.Type())
	}
	cloned := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
	for index := 0; index < value.Len(); index++ {
		cloned.Index(index).Set(cloneBrowserConversationReflect(value.Index(index)))
	}
	return cloned
}

func cloneBrowserConversationArray(value reflect.Value) reflect.Value {
	cloned := reflect.New(value.Type()).Elem()
	for index := 0; index < value.Len(); index++ {
		cloned.Index(index).Set(cloneBrowserConversationReflect(value.Index(index)))
	}
	return cloned
}

func cloneBrowserConversationMap(value reflect.Value) reflect.Value {
	if value.IsNil() {
		return reflect.Zero(value.Type())
	}
	cloned := reflect.MakeMapWithSize(value.Type(), value.Len())
	iter := value.MapRange()
	for iter.Next() {
		cloned.SetMapIndex(cloneBrowserConversationReflect(iter.Key()), cloneBrowserConversationReflect(iter.Value()))
	}
	return cloned
}

func opaqueEqual(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return strings.TrimSpace(browserConversationOpaqueString(left)) == strings.TrimSpace(browserConversationOpaqueString(right))
}
