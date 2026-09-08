package stream

// AnalyzePCM16 measures and evaluates one explicit mono PCM16 stream. It is
// side-effect-free: the input sample slice and all annotation slices remain
// untouched, and the returned report owns its slices.
func AnalyzePCM16(input PCM16Input, config PCM16AnalysisConfig) (PCM16Analysis, error) {
	prepared, err := preparePCM16Analysis(input, config)
	if err != nil {
		return PCM16Analysis{}, err
	}
	analysis := newPCM16Analysis(prepared)
	measurePCM16Frames(&analysis, prepared)
	appendPCM16ClipFailure(&analysis, prepared)
	measurePCM16Silence(&analysis, prepared)
	measurePCM16Boundaries(&analysis, prepared)
	measurePCM16Edges(&analysis, prepared)
	return analysis, nil
}

// Analyze is a concise alias for AnalyzePCM16.
func Analyze(input PCM16Input, config PCM16AnalysisConfig) (PCM16Analysis, error) {
	return AnalyzePCM16(input, config)
}

// AssertPCM16 evaluates a stream and returns a typed error for any measured
// property violation. The report-oriented AnalyzePCM16 function remains
// available when callers need all measurements and failures.
func AssertPCM16(input PCM16Input, config PCM16AnalysisConfig) error {
	analysis, err := AnalyzePCM16(input, config)
	if err != nil {
		return err
	}
	if analysis.Passed() {
		return nil
	}
	return &PCM16AssertionError{StreamID: analysis.StreamID, Failures: analysis.FailuresCopy()}
}

// ValidatePCM16 is an assertion-oriented alias for AssertPCM16.
func ValidatePCM16(input PCM16Input, config PCM16AnalysisConfig) error {
	return AssertPCM16(input, config)
}
