package browserscenario

// BrowserConversationReportVersion identifies the stable, sanitized report
// envelope suitable for review or validator input.
const BrowserConversationReportVersion = "webmcp.conversational-report.v1"

// BrowserConversationValidatorInputVersion identifies the fixed validator
// input envelope.
const BrowserConversationValidatorInputVersion = "webmcp.conversational-validator-input.v1"

// BrowserConversationReportMetadata contains reproducibility facts safe to
// publish. It intentionally has no credential field.
type BrowserConversationReportMetadata struct {
	Command            string   `json:"command,omitempty"`
	Configuration      string   `json:"configuration,omitempty"`
	DependencyBaseline []string `json:"dependency_baseline,omitempty"`
	Provider           string   `json:"provider,omitempty"`
	Model              string   `json:"model,omitempty"`
	BrowserChannel     string   `json:"browser_channel,omitempty"`
	BrowserVersion     string   `json:"browser_version,omitempty"`
	BrowserRevision    string   `json:"browser_revision,omitempty"`
	PR269Status        string   `json:"pr_269_status,omitempty"`
	LaneIBranch        string   `json:"lane_i_branch,omitempty"`
	LaneIPullRequest   string   `json:"lane_i_pull_request,omitempty"`
}

// BrowserConversationReport is the complete sanitized evidence envelope.
type BrowserConversationReport struct {
	Version  string                            `json:"version"`
	Metadata BrowserConversationReportMetadata `json:"metadata"`
	Rubric   []string                          `json:"validator_rubric"`
	Evidence BrowserConversationResult         `json:"evidence"`
}

// BrowserConversationValidatorInput is the fixed sanitized payload consumed
// by an external validator agent.
type BrowserConversationValidatorInput struct {
	Version  string                    `json:"version"`
	Rubric   []string                  `json:"rubric"`
	Evidence BrowserConversationResult `json:"evidence"`
}

// BrowserConversationValidatorRubric is a value object that returns a fresh
// copy of the required validator checks.
type BrowserConversationValidatorRubric struct{}

func (BrowserConversationValidatorRubric) Values() []string {
	return []string{
		"claim_grounding", "terminal_statuses", "page_state_changes",
		"stale_reference_recovery", "input_json_validity", "correction_grounding",
		"interruption_and_cancel", "detach_survival",
	}
}

// BrowserConversationTrace provides pure validity measurement over an ordered
// broker trace without exposing a package-level function.
type BrowserConversationTrace []BrowserConversationBrokerCall

func (calls BrowserConversationTrace) InputJSONValidity() BrowserConversationInputJSONValidity {
	return computeBrowserConversationInputJSONValidity(calls)
}
