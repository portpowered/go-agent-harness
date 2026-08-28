package webmcp

import (
	"errors"
	"strings"
)

func classifyOperation(descriptor ToolDescriptor) OperationClass {
	if descriptor.Annotations.ReadOnly == nil {
		return OperationUnknown
	}
	if *descriptor.Annotations.ReadOnly {
		return OperationReadOnly
	}
	return OperationMutating
}

func lifecycleInvocationErrorCode(reason string, fallback ErrorCode) ErrorCode {
	switch strings.ToLower(reason) {
	case "disconnect", "disconnected", "browser_disconnected":
		return ErrorBrowserDisconnected
	case "detach", "detached", "target_detached":
		return ErrorTargetDetached
	default:
		return fallback
	}
}

func errorCodeFor(err error, fallback ErrorCode) ErrorCode {
	var classifiedError *ClassifiedError
	if errors.As(err, &classifiedError) && classifiedError != nil && IsKnownErrorCode(classifiedError.Code) {
		return classifiedError.Code
	}
	return fallback
}

func safePageErrorCode(code string) string {
	if code == "" {
		return ""
	}
	if len(code) > 64 {
		return code[:64]
	}
	for _, character := range code {
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '_' && character != '-' && character != '.' {
			return "unknown"
		}
	}
	return code
}
