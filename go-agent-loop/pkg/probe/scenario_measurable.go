package probe

func init() {
	typedExpectationFieldsByKind[ExpectAudioEnergy] = map[string]bool{}
	typedExpectationFieldsByKind[ExpectTranscriptContains] = map[string]bool{"text": true}
	typedExpectationFieldsByKind[ExpectToolCalled] = map[string]bool{"tool_name": true}
	typedExpectationFieldsByKind[ExpectLatencyWithinTicks] = map[string]bool{"at": true}
	typedExpectationFieldsByKind[ExpectTerminalReason] = map[string]bool{"value": true}
	typedExpectationFieldsByKind[ExpectFrameCount] = map[string]bool{}
}
