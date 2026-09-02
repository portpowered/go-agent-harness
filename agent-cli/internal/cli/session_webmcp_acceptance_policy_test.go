package cli

import (
	"strings"
	"testing"
)

// sessionWebMCPWorkspaceBaseline is intentionally page-agnostic. The live
// acceptance test writes this exact policy to a workspace AGENTS.md and then
// asks the production agent to operate previously unknown WebMCP sites.
const sessionWebMCPWorkspaceBaseline = `# Browser workspace

When the customer identifies an already-open website by its purpose, use the WebMCP tab-listing tool, match the request against the safe title and origin returned by that tool, and select the one matching tab with its exact browser and target IDs. If there is exactly one clear match, select it without asking the customer to repeat the URL. Ask only when multiple tabs genuinely match. Listing tabs is discovery, not completion: do not stop, narrate, or return to the customer after finding a clear match; select it immediately and continue the requested page work in the same turn.

After selecting a page, discover its currently advertised page tools. Prefer structured read tools over screenshots. Read the current page state before changing it, carry out the customer's requested edits with the page's advertised tools, preserve customer-supplied text exactly, and read the resulting state back before claiming success. When the customer moves to a different page, list and select tabs again; never reuse a page tool from the previously selected tab.
`

func TestSessionWebMCPWorkspaceBaselineIsPageAgnosticAndVerifiable(t *testing.T) {
	for _, want := range []string{
		"already-open website by its purpose",
		"safe title and origin",
		"exact browser and target IDs",
		"exactly one clear match",
		"Listing tabs is discovery, not completion",
		"continue the requested page work in the same turn",
		"discover its currently advertised page tools",
		"Read the current page state before changing it",
		"preserve customer-supplied text exactly",
		"read the resulting state back",
		"never reuse a page tool from the previously selected tab",
	} {
		if !strings.Contains(sessionWebMCPWorkspaceBaseline, want) {
			t.Errorf("baseline AGENTS.md omits %q", want)
		}
	}
	for _, forbidden := range []string{
		"paperie", "margin", "greeting card", "document editor",
		"get_card_state", "set_card_message", "create_document", "update_document",
		"openai.chatgpt.site",
	} {
		if strings.Contains(strings.ToLower(sessionWebMCPWorkspaceBaseline), forbidden) {
			t.Errorf("baseline AGENTS.md leaks scenario-specific hint %q", forbidden)
		}
	}
}
