package hermetic

// OperationDiscover and the list constants are diagnostic-only caller
// operation names. They are deliberately not included in the frozen
// browser-script.v1 operation vocabulary.
const (
	OperationDiscover           OperationType = "discover"
	OperationList               OperationType = "list"
	OperationListTools          OperationType = "list_tools"
	OperationBrowserDiscover    OperationType = "browser_discover"
	OperationBrowserListTargets OperationType = "browser_list_targets"
	OperationBrowserListTools   OperationType = "browser_list_tools"
	OperationDoctor             OperationType = "doctor"
	OperationContext            OperationType = "context"
	OperationBrowsers           OperationType = "browsers"
	OperationTabs               OperationType = "tabs"
	OperationTools              OperationType = "tools"
)

// listTargetsOperation is the scripted runtime's target listing, a read-only
// diagnostic operation. It is untyped so it does not join the fixture
// operation vocabulary that exhaustive switches cover.
const listTargetsOperation = "list_targets"
