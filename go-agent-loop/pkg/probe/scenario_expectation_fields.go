package probe

func expectationAllowed(fields ...string) map[string]bool {
	allowed := make(map[string]bool, len(fields)+len(expectationModifiers))
	for key := range expectationModifiers {
		allowed[key] = true
	}
	for _, key := range fields {
		allowed[key] = true
	}
	return allowed
}

var expectationFieldsByKind = map[ExpectationKind]map[string]bool{
	ExpectText:               expectationAllowed("text", "value", "message"),
	ExpectTranscript:         expectationAllowed("text", "value", "message"),
	ExpectContains:           expectationAllowed("text", "value", "message"),
	ExpectAudio:              expectationAllowed("corpus_id", "corpusID"),
	ExpectToolCall:           expectationAllowed("tool_call_id", "toolCallID", "tool_name", "toolName", "name"),
	ExpectToolResult:         expectationAllowed("tool_call_id", "toolCallID", "result"),
	ExpectTerminalReason:     expectationAllowed("value", "text", "message"),
	ExpectTerminalProvenance: expectationAllowed("value", "text", "message"),
	ExpectOutputState:        expectationAllowed("value", "text", "message"),
	ExpectClose:              expectationAllowed(),
	ExpectTime:               expectationAllowed("at", "time", "logical_time", "logicalTime"),
	ExpectEvent:              expectationAllowed("event", "value", "message"),
}
