package tools

// ClassifiedError carries safe, already-classified browser failure metadata
// across the neutral browser seam. Message and Details are model-visible and
// therefore must not contain secrets, raw input, or page output.
type ClassifiedError struct {
	Code      ErrorCode
	Message   string
	Retryable bool
	Details   map[string]any
	Cause     error
}

func (e *ClassifiedError) Error() string {
	if e == nil {
		return "webmcp classified error"
	}
	if e.Message != "" {
		return e.Message
	}
	return string(e.Code)
}

func (e *ClassifiedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
