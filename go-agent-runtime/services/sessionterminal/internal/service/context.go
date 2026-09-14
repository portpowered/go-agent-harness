package service

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
)

type reporterContextKey struct{}

func WithReporter(ctx context.Context, reporter sessionterminal.Reporter) context.Context {
	if reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, reporterContextKey{}, reporter)
}

func ReporterFromContext(ctx context.Context) sessionterminal.Reporter {
	if ctx == nil {
		return nil
	}
	reporter, _ := ctx.Value(reporterContextKey{}).(sessionterminal.Reporter)
	return reporter
}

func HasIndependentFailure(err error) bool {
	if err == nil {
		return false
	}
	var leaves []error
	collectErrorLeaves(err, &leaves)
	for _, leaf := range leaves {
		if leaf == nil || leaf == context.Canceled || leaf == context.DeadlineExceeded || errors.Is(leaf, sessionterminal.ErrDurationExpired) {
			continue
		}
		return true
	}
	return false
}

func collectErrorLeaves(err error, leaves *[]error) {
	if err == nil {
		return
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			collectErrorLeaves(child, leaves)
		}
		return
	}
	if unwrapped := errors.Unwrap(err); unwrapped != nil {
		collectErrorLeaves(unwrapped, leaves)
		return
	}
	*leaves = append(*leaves, err)
}
