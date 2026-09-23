package browserconversation

// Run is the caller-facing observation boundary for one browser conversation.
// Its mutable collector and validation policy are owned by the private service.
type Run interface {
	Scenario() BrowserConversationScenario
	ObserveCustomerTurn(stepID, observed string) error
	ObserveAssistantTurn(stepID, observed string) error
	ObserveTurn(BrowserConversationTurn) error
	ObserveBrokerCall(BrowserConversationBrokerCall) error
	RecordRecovery([]BrowserConversationRecoveryEvidence) error
	RecordCorrections([]BrowserConversationCorrectionEvidence) error
	ObserveOracleSnapshot(BrowserConversationOracleSnapshot) error
	RecordCancellation(BrowserConversationCancellationEvidence) error
	ObserveInvocationPublication(source string, invocationID, state any, terminal bool) error
	RecordLifecycle(BrowserConversationLifecycleEvidence) error
	RecordMechanicalEvaluation(BrowserConversationMechanicalEvaluation) error
	RecordValidator(BrowserConversationValidatorVerdict) error
	Snapshot() BrowserConversationResult
	Finalize() (BrowserConversationResult, error)
}
