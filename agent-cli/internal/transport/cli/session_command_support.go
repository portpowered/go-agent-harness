package cli

import (
	"fmt"
	"io"
	"strings"

	gwproviders "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

const sessionCommandExample = "  yui session\n" +
	"  yui session --voice marin --model gpt-realtime-2.1\n" +
	"  yui session --record session.json\n" +
	"  yui session --browser-tools webmcp --browser-open https://example.com/"

// SetFeedbackWarningWriter exposes feedback-classification text only to
// diagnostics and tests. Production CLI sessions leave it nil and remain
// silent; feedback suppression itself is unaffected.
func (c *SessionCommand) SetFeedbackWarningWriter(writer io.Writer) {
	if c == nil {
		return
	}
	c.feedbackWarningWriter = writer
}

func decorateSessionCommandError(err error) error {
	if err == nil || strings.Contains(err.Error(), "classification=") {
		return err
	}
	classification := gwproviders.SessionErrorClassification("", "", err.Error())
	if classification != gwproviders.ErrorClassRateLimited {
		return err
	}
	return fmt.Errorf("[classification=%s]: %w", classification, err)
}
