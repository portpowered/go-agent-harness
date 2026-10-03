package service

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	// historyMessages bounds how much session history a task quotes.
	historyMessages = 12
	// historyMessageBytes bounds one quoted history message.
	historyMessageBytes = 1000
	// failureDetailBytes bounds the error text in a failure commentary.
	failureDetailBytes = 300
	// partialResultBytes bounds the partial result quoted when a budget ends a
	// delegation.
	partialResultBytes = 1200
)

// defaultTaskInstructions apply when the host supplies none.
const defaultTaskInstructions = "Use the available tools to do the delegated work. " +
	"Ask for confirmation in your result before any irreversible action the user has not confirmed."

// systemPrompt is the backend prompt prefix suggested by the GPT-Live
// migration guide (gpt-live-provider.md 1.10.1), with the host's task
// instructions and the length rule of open question Q6: the backend
// summarizes, and the provider splits an over-long append as a safety net.
func systemPrompt(instructions string) string {
	if strings.TrimSpace(instructions) == "" {
		instructions = defaultTaskInstructions
	}
	return "## Voice conversation context\n" +
		"You are helping an assistant in a live voice conversation. Transcripts " +
		"can contain mistakes, unfinished phrases, and later corrections. Use " +
		"the latest context and verified records. If a needed detail is still " +
		"unclear, ask for that detail instead of guessing.\n" +
		"## Task instructions\n" + strings.TrimSpace(instructions) + "\n" +
		"## Return the result\n" +
		"Return the relevant facts, the task's current status, and the next step. " +
		"Report an action as complete after the tool or service confirms success. " +
		"If the outcome is unclear, state that and explain what needs to be checked. " +
		"The assistant speaks your result aloud: keep it under 300 words, as plain " +
		"sentences, and summarize long output instead of quoting it."
}

// taskText is the user turn of the nested loop: the provider's task text
// when it sends one, otherwise the transcript window, plus the session's
// recent conversation so short replies such as "yes" stay resolvable.
func taskText(value messages.DelegationCreatedValue, history []messages.Message) string {
	var b strings.Builder
	b.WriteString("## Delegated request\n")
	if task := strings.TrimSpace(value.Task); task != "" {
		b.WriteString(task)
	} else {
		b.WriteString("The voice assistant delegated a request without task text. " +
			"Work out what the user wants from the live transcript below; the latest turns matter most.")
	}
	b.WriteString("\n")
	if len(value.Transcript) > 0 {
		b.WriteString("## Live transcript (oldest first)\n")
		for _, fragment := range value.Transcript {
			fmt.Fprintf(&b, "%s [%d-%d ms]: %s\n", fragment.Speaker, fragment.StartMS, fragment.EndMS, strings.TrimSpace(fragment.Text))
		}
	}
	if earlier := historyText(history); earlier != "" {
		b.WriteString("## Earlier conversation\n")
		b.WriteString(earlier)
	}
	return strings.TrimSpace(b.String())
}

func historyText(history []messages.Message) string {
	var lines []string
	for _, message := range history {
		if message.Role != messages.RoleUser && message.Role != messages.RoleAssistant {
			continue
		}
		text := strings.TrimSpace(message.TextContent())
		if text == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s\n", message.Role, truncate(text, historyMessageBytes)))
	}
	if len(lines) > historyMessages {
		lines = lines[len(lines)-historyMessages:]
	}
	return strings.Join(lines, "")
}

// truncate cuts text to at most limit bytes on a rune boundary.
func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return strings.TrimSpace(text[:cut]) + "..."
}

// failureCommentary is what GPT-Live is told when a delegation fails, so it
// never waits forever for a result (GPT-Live has no delegation timeout).
func failureCommentary(err error) string {
	detail := "unknown error"
	if err != nil {
		detail = truncate(strings.TrimSpace(err.Error()), failureDetailBytes)
	}
	return "The delegated task failed and has no result: " + detail +
		". Tell the user it did not work and offer to try again."
}

// budgetCommentary is the answer to a delegation stopped by its budget.
func budgetCommentary(err error, partial string) string {
	text := "The delegated task stopped before finishing: " + err.Error() + "."
	if partial = strings.TrimSpace(partial); partial != "" {
		text += " What it found so far: " + truncate(partial, partialResultBytes)
	}
	return text + " Tell the user the task is incomplete."
}

// emptyCommentary answers a delegation whose backend finished without text.
const emptyCommentary = "The delegated task finished but returned no result. Tell the user nothing was found."

// progressThinking is the quiet progress note sent before a tool runs.
func progressThinking(call messages.ToolCall) string {
	return "Delegated task in progress: running the " + call.Name + " tool."
}
