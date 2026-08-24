package services

import (
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func visibleAutocompleteLines(view string) []string {
	if view == "" {
		return nil
	}
	view = ansiEscape.ReplaceAllString(view, "")
	return strings.Split(strings.TrimSuffix(view, "\n"), "\n")
}

func TestAutocomplete_S4FilterShapes(t *testing.T) {
	tests := []struct {
		name         string
		items        []Suggestion
		filter       string
		wantActive   bool
		wantCount    int
		wantSelected string
		wantLines    []string
	}{
		{
			name:         "empty input shows all candidates",
			items:        []Suggestion{{Label: "alpha"}, {Label: "beta"}},
			filter:       "",
			wantActive:   true,
			wantCount:    2,
			wantSelected: "alpha",
			wantLines:    []string{"alpha", "beta"},
		},
		{
			name:         "no matches deactivates popup",
			items:        []Suggestion{{Label: "alpha"}, {Label: "beta"}},
			filter:       "z",
			wantActive:   false,
			wantCount:    0,
			wantSelected: "",
			wantLines:    nil,
		},
		{
			name:         "exactly one match selects its content",
			items:        []Suggestion{{Label: "alpha"}, {Label: "beta"}},
			filter:       "al",
			wantActive:   true,
			wantCount:    1,
			wantSelected: "alpha",
			wantLines:    []string{"alpha"},
		},
		{
			name: "many matches preserve candidate order",
			items: []Suggestion{
				{Label: "main.go"},
				{Label: "main_test.go"},
				{Label: "map.go"},
				{Label: "README.md"},
			},
			filter:       "ma",
			wantActive:   true,
			wantCount:    3,
			wantSelected: "main.go",
			wantLines:    []string{"main.go", "main_test.go", "map.go"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ac := NewAutocomplete()
			ac.SetSuggestions(test.items)
			ac.SetFilter(test.filter)

			if got := ac.IsActive(); got != test.wantActive {
				t.Errorf("IsActive() = %t, want %t", got, test.wantActive)
			}
			if got := ac.FilteredCount(); got != test.wantCount {
				t.Errorf("FilteredCount() = %d, want %d", got, test.wantCount)
			}
			if got := ac.Selected(); got != test.wantSelected {
				t.Errorf("Selected() = %q, want %q", got, test.wantSelected)
			}
			if got := visibleAutocompleteLines(ac.View()); !sameStrings(got, test.wantLines) {
				t.Errorf("View() lines = %#v, want %#v", got, test.wantLines)
			}
		})
	}
}

func TestAutocomplete_KeyboardNavigationAndStateTransitions(t *testing.T) {
	items := make([]Suggestion, 11)
	for i := range items {
		items[i] = Suggestion{Label: "item-" + string(rune('a'+i))}
	}

	ac := NewAutocomplete()
	ac.SetSuggestions(items)
	ac.SetFilter("")
	if !ac.IsActive() || ac.Selected() != "item-a" {
		t.Fatalf("initial autocomplete state = active %t, selected %q", ac.IsActive(), ac.Selected())
	}

	updated, cmd := ac.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	if cmd != nil || updated.Selected() != "item-a" || !updated.IsActive() {
		t.Fatalf("non-key update = selected %q, active %t, cmd %v", updated.Selected(), updated.IsActive(), cmd)
	}
	ac = updated

	updated, _ = ac.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if updated.Selected() != "item-a" {
		t.Fatalf("unhandled key changed selection to %q", updated.Selected())
	}
	ac = updated

	updated, _ = ac.Update(tea.KeyMsg{Type: tea.KeyUp})
	if updated.Selected() != "item-a" {
		t.Fatalf("up at first candidate selected %q", updated.Selected())
	}
	ac = updated

	for i := 0; i < 10; i++ {
		ac, _ = ac.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if ac.Selected() != "item-k" {
		t.Fatalf("selection after moving down = %q, want item-k", ac.Selected())
	}
	if got, want := visibleAutocompleteLines(ac.View()), []string{"item-b", "item-c", "item-d", "item-e", "item-f", "item-g", "item-h", "item-i", "item-j", "item-k"}; !sameStrings(got, want) {
		t.Fatalf("scrolled View() = %#v, want %#v", got, want)
	}

	ac, _ = ac.Update(tea.KeyMsg{Type: tea.KeyDown})
	if ac.Selected() != "item-k" {
		t.Fatalf("down at last candidate selected %q", ac.Selected())
	}
	for i := 0; i < 10; i++ {
		ac, _ = ac.Update(tea.KeyMsg{Type: tea.KeyUp})
	}
	if ac.Selected() != "item-a" {
		t.Fatalf("selection after moving up = %q, want item-a", ac.Selected())
	}

	ac, _ = ac.Update(tea.KeyMsg{Type: tea.KeyTab})
	if !ac.IsActive() || ac.Selected() != "item-a" {
		t.Fatalf("tab changed state to active %t, selected %q", ac.IsActive(), ac.Selected())
	}

	ac, _ = ac.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if ac.IsActive() || ac.Selected() != "" || ac.View() != "" {
		t.Fatalf("escape state = active %t, selected %q, view %q", ac.IsActive(), ac.Selected(), ac.View())
	}
	ac, _ = ac.Update(tea.KeyMsg{Type: tea.KeyDown})
	if ac.IsActive() || ac.Selected() != "" {
		t.Fatalf("inactive update changed state to active %t, selected %q", ac.IsActive(), ac.Selected())
	}
}

func TestAutocomplete_ViewRendersDescriptionsAndSelection(t *testing.T) {
	ac := NewAutocomplete()
	ac.SetSuggestions([]Suggestion{
		{Label: "alpha", Description: "first option"},
		{Label: "beta"},
	})
	ac.SetFilter("")

	if got, want := visibleAutocompleteLines(ac.View()), []string{"alpha  first option", "beta"}; !sameStrings(got, want) {
		t.Fatalf("initial rendered lines = %#v, want %#v", got, want)
	}

	ac, _ = ac.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got, want := visibleAutocompleteLines(ac.View()), []string{"alpha  first option", "beta"}; !sameStrings(got, want) {
		t.Fatalf("rendered lines after selecting no-description item = %#v, want %#v", got, want)
	}
	if ac.Selected() != "beta" {
		t.Errorf("Selected() after navigation = %q, want beta", ac.Selected())
	}
}

func TestAutocomplete_ResetClearsFilterAndRenderedState(t *testing.T) {
	ac := NewAutocomplete()
	ac.SetSuggestions([]Suggestion{{Label: "prefix-one"}, {Label: "other"}})
	ac.SetFilter("prefix")
	if !ac.IsActive() || ac.Selected() != "prefix-one" {
		t.Fatalf("pre-reset state = active %t, selected %q", ac.IsActive(), ac.Selected())
	}

	ac.Reset()
	if ac.IsActive() || ac.FilteredCount() != 0 || ac.Selected() != "" || ac.View() != "" {
		t.Fatalf("reset state = active %t, count %d, selected %q, view %q", ac.IsActive(), ac.FilteredCount(), ac.Selected(), ac.View())
	}

	ac.SetSuggestions([]Suggestion{{Label: "other"}})
	if !ac.IsActive() || ac.Selected() != "other" {
		t.Fatalf("state after reusing reset model = active %t, selected %q", ac.IsActive(), ac.Selected())
	}
}

func TestAutocomplete_SelectedRejectsInvalidSelection(t *testing.T) {
	ac := NewAutocomplete()
	ac.active = true
	ac.filtered = []Suggestion{{Label: "valid"}}
	for _, selected := range []int{-1, len(ac.filtered)} {
		ac.selected = selected
		if got := ac.Selected(); got != "" {
			t.Errorf("Selected() with index %d = %q, want empty", selected, got)
		}
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
