package services

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// maxSuggestions is the maximum number of suggestions shown at a time.
const maxSuggestions = 10

// Suggestion represents a single autocomplete suggestion with a label and optional description.
type Suggestion struct {
	Label       string
	Description string
}

// styleSelected is the highlight style for the currently selected suggestion.
func styleSelected() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("12"))
}

// styleDescription renders the description text in a dimmer color.
func styleDescription() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
}

// Autocomplete is a reusable Bubble Tea sub-model that renders a list of
// suggestions below the input line and supports keyboard navigation.
//
// The parent model should:
//  1. Call SetSuggestions() to provide the full suggestion list.
//  2. Call SetFilter() with the current prefix to filter suggestions.
//  3. Delegate key events to Update() when IsActive() is true.
//  4. Append View() output below the input line.
type Autocomplete struct {
	suggestions []Suggestion // full unfiltered list
	filtered    []Suggestion // suggestions matching the current filter
	filter      string       // current filter prefix
	selected    int          // index into filtered (0-based)
	active      bool         // whether the popup is visible
	offset      int          // scroll offset for long lists
}

// NewAutocomplete creates an empty, inactive Autocomplete model.
func NewAutocomplete() Autocomplete {
	return Autocomplete{}
}

// SetSuggestions replaces the full suggestion list and reapplies the current filter.
func (a *Autocomplete) SetSuggestions(items []Suggestion) {
	a.suggestions = items
	a.applyFilter()
}

// SetFilter updates the filter prefix, refilters suggestions, and activates
// the popup if there are matches (deactivates if none).
func (a *Autocomplete) SetFilter(prefix string) {
	a.filter = prefix
	a.applyFilter()
}

// IsActive reports whether the autocomplete popup is visible (has filtered matches).
func (a *Autocomplete) IsActive() bool {
	return a.active
}

// Selected returns the label of the currently selected suggestion, or empty
// string if nothing is selected or the popup is inactive.
func (a *Autocomplete) Selected() string {
	if !a.active || len(a.filtered) == 0 {
		return ""
	}
	if a.selected < 0 || a.selected >= len(a.filtered) {
		return ""
	}
	return a.filtered[a.selected].Label
}

// FilteredCount returns the number of suggestions matching the current filter.
func (a *Autocomplete) FilteredCount() int {
	return len(a.filtered)
}

// Reset deactivates the autocomplete popup and clears the filter.
func (a *Autocomplete) Reset() {
	a.active = false
	a.filter = ""
	a.filtered = nil
	a.selected = 0
	a.offset = 0
}

// Update handles key events for the autocomplete popup. It consumes Up, Down,
// Tab, and Escape when active. Returns the updated model and an optional Cmd.
// The parent should check IsActive() before delegating.
func (a *Autocomplete) Update(msg tea.Msg) (Autocomplete, tea.Cmd) {
	next := *a
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || !next.active {
		return next, nil
	}

	// Only Up, Down, and Esc change popup state (tea.KeyType has ~85 members,
	// so an if-chain states that directly). Tab completes the selected
	// suggestion: the parent reads Selected() and decides whether to dismiss.
	if keyType := keyMsg.Type; keyType == tea.KeyUp {
		if next.selected > 0 {
			next.selected--
			// Scroll up if needed.
			if next.selected < next.offset {
				next.offset = next.selected
			}
		}
	} else if keyType == tea.KeyDown {
		if next.selected < len(next.filtered)-1 {
			next.selected++
			// Scroll down if needed.
			if next.selected >= next.offset+maxSuggestions {
				next.offset = next.selected - maxSuggestions + 1
			}
		}
	} else if keyType == tea.KeyEsc {
		next.active = false
		next.selected = 0
		next.offset = 0
	}

	return next, nil
}

// View renders the autocomplete popup as a string. Returns empty string when
// inactive or no matches. The output is meant to be appended below the input line.
func (a *Autocomplete) View() string {
	if !a.active || len(a.filtered) == 0 {
		return ""
	}

	var b strings.Builder
	end := a.offset + maxSuggestions
	if end > len(a.filtered) {
		end = len(a.filtered)
	}

	for i := a.offset; i < end; i++ {
		s := a.filtered[i]
		line := s.Label
		if s.Description != "" {
			line += "  " + styleDescription().Render(s.Description)
		}

		if i == a.selected {
			// Re-render the whole line with selection style (label part only for highlight).
			if s.Description != "" {
				line = styleSelected().Render(s.Label) + "  " + styleDescription().Render(s.Description)
			} else {
				line = styleSelected().Render(s.Label)
			}
		}

		b.WriteString(line)
		b.WriteByte('\n')
	}

	return b.String()
}

// applyFilter filters suggestions by the current prefix (case-sensitive) and
// updates the active state. Resets selection when the filter changes.
func (a *Autocomplete) applyFilter() {
	a.filtered = nil
	a.selected = 0
	a.offset = 0

	for _, s := range a.suggestions {
		if a.filter == "" || strings.HasPrefix(s.Label, a.filter) {
			a.filtered = append(a.filtered, s)
		}
	}

	a.active = len(a.filtered) > 0
}
