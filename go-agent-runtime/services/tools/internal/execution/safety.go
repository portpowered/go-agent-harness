package execution

import (
	"errors"
	"time"

	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

func clonePolicy(policy public.InteractiveToolPolicy) (clone public.InteractiveToolPolicy) {
	if policy == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			clone = nil
		}
	}()
	return policy.Clone()
}

func safePolicyTimeout(policy public.InteractiveToolPolicy, name string) (timeout time.Duration) {
	defer func() {
		if recover() != nil {
			timeout = 0
		}
	}()
	return policy.TimeoutForTool(name)
}

func safeTimeoutPolicy(policy public.ToolExecutionTimeoutPolicy, name string) (timeout time.Duration) {
	if policy == nil {
		return 0
	}
	defer func() {
		if recover() != nil {
			timeout = 0
		}
	}()
	return policy(name)
}

func safeSIGINT(intent public.ToolExecutionCancellationIntent) (received bool) {
	if intent == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			received = false
		}
	}()
	return intent.SIGINTReceived()
}

func isPageSightTool(executor any, name string) (page bool) {
	router, ok := executor.(interface{ IsPageSightTool(string) bool })
	if !ok || router == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			page = false
		}
	}()
	return router.IsPageSightTool(name)
}

func safePhysicalMatch(matcher public.PhysicalDisplayToolMatcher, name string) (physical bool) {
	if matcher == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			physical = false
		}
	}()
	return matcher(name)
}

func safeScreenErrorCode(code public.ScreenToolErrorCode, err error) (value string) {
	if code == nil {
		return ""
	}
	defer func() {
		if recover() != nil {
			value = ""
		}
	}()
	return code(err)
}

func safePermissionError(factory public.PermissionDeniedErrorFactory, reason string) (result error) {
	if factory == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			result = nil
		}
	}()
	return factory(reason)
}

func permissionError(factory public.PermissionDeniedErrorFactory, reason string) error {
	if err := safePermissionError(factory, reason); err != nil {
		return err
	}
	if reason == "" {
		return public.ErrToolExecutionPermissionDenied
	}
	return errors.Join(public.ErrToolExecutionPermissionDenied, errors.New(reason))
}
