package webmcp

import "errors"

var (
	ErrClosed             = errors.New("webmcp: closed")
	ErrBrowserNotFound    = errors.New("webmcp: browser not found")
	ErrTargetNotFound     = errors.New("webmcp: target not found")
	ErrInvocationNotFound = errors.New("webmcp: invocation not found")
	ErrEventBufferFull    = errors.New("webmcp: event buffer full")
)
