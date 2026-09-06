package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const baselineVersion = 1

type Baseline struct {
	Version      int              `json:"version"`
	SourceCommit string           `json:"source_commit,omitempty"`
	Entries      []BaselineEntry  `json:"entries"`
	Renames      []BaselineRename `json:"renames,omitempty"`
}

type BaselineEntry struct {
	Rule      string `json:"rule"`
	Module    string `json:"module,omitempty"`
	Package   string `json:"package,omitempty"`
	File      string `json:"file,omitempty"`
	Symbol    string `json:"symbol,omitempty"`
	Value     int    `json:"value,omitempty"`
	Message   string `json:"message,omitempty"`
	Rationale string `json:"rationale"`
	Phase     string `json:"phase"`
}

type BaselineRename struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// baselineForChecks keeps one reviewed file usable by the separate Make
// lanes. A size-only invocation must not report architecture entries as
// stale, and an architecture-only invocation must not consume size debt.
// When both lanes run together the baseline is unchanged.
func baselineForChecks(baseline Baseline, checks map[string]bool) Baseline {
	if checks["architecture"] && checks["size"] {
		return baseline
	}
	entries := make([]BaselineEntry, 0, len(baseline.Entries))
	kept := make(map[string]struct{}, len(baseline.Entries))
	for _, entry := range baseline.Entries {
		isSize := metricRule(entry.Rule)
		if (checks["size"] && isSize) || (checks["architecture"] && !isSize) {
			entries = append(entries, entry)
			kept[baselineIssue(entry).Key()] = struct{}{}
		}
	}
	renamed := make([]BaselineRename, 0, len(baseline.Renames))
	for _, rename := range baseline.Renames {
		if _, ok := kept[rename.To]; ok {
			renamed = append(renamed, rename)
		}
	}
	return Baseline{Version: baseline.Version, SourceCommit: baseline.SourceCommit, Entries: entries, Renames: renamed}
}

func loadBaseline(path, repoRoot string) (Baseline, error) {
	abs, err := resolveRepoPath(path, repoRoot)
	if err != nil {
		return Baseline{}, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return Baseline{}, fmt.Errorf("read baseline %q: %w", path, err)
	}
	var baseline Baseline
	if err := json.Unmarshal(data, &baseline); err != nil {
		return Baseline{}, fmt.Errorf("decode baseline %q: %w", path, err)
	}
	if baseline.Version != baselineVersion {
		return Baseline{}, fmt.Errorf("baseline %q has version %d; expected %d", path, baseline.Version, baselineVersion)
	}
	if err := validateBaseline(baseline); err != nil {
		return Baseline{}, fmt.Errorf("invalid baseline %q: %w", path, err)
	}
	return baseline, nil
}

func validateBaseline(baseline Baseline) error {
	if err := validateBaselineEntries(baseline.Entries); err != nil {
		return err
	}
	return validateBaselineRenames(baseline.Renames)
}

func validateBaselineEntries(entries []BaselineEntry) error {
	seen := make(map[string]struct{}, len(entries))
	previous := ""
	for index, entry := range entries {
		if err := validateBaselineEntry(index, entry); err != nil {
			return err
		}
		key := baselineIssue(entry).Key()
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate entry %q", key)
		}
		if previous != "" && key <= previous {
			return fmt.Errorf("entries must be strictly sorted by issue key: %q follows %q", key, previous)
		}
		previous = key
		seen[key] = struct{}{}
	}
	return nil
}

func validateBaselineEntry(index int, entry BaselineEntry) error {
	if entry.Rule == "" {
		return fmt.Errorf("entry %d has empty rule", index)
	}
	if !metricRule(entry.Rule) && entry.Message == "" {
		return fmt.Errorf("entry %d for %q requires message", index, entry.Rule)
	}
	if entry.Rationale == "" || entry.Phase == "" {
		return fmt.Errorf("entry %d (%s) requires rationale and phase", index, entry.Rule)
	}
	return nil
}

func validateBaselineRenames(renames []BaselineRename) error {
	fromSeen := make(map[string]struct{}, len(renames))
	toSeen := make(map[string]struct{}, len(renames))
	for index, rename := range renames {
		if rename.From == "" || rename.To == "" || rename.From == rename.To {
			return fmt.Errorf("rename %d must have distinct from and to keys", index)
		}
		if _, ok := fromSeen[rename.From]; ok {
			return fmt.Errorf("rename %q appears more than once", rename.From)
		}
		if _, ok := toSeen[rename.To]; ok {
			return fmt.Errorf("rename target %q appears more than once", rename.To)
		}
		fromSeen[rename.From], toSeen[rename.To] = struct{}{}, struct{}{}
	}
	return nil
}

func baselineIssue(entry BaselineEntry) Issue {
	return Issue{Rule: entry.Rule, Module: entry.Module, Package: entry.Package, File: entry.File, Symbol: entry.Symbol, Value: entry.Value, Message: entry.Message}
}

func compareBaseline(issues []Issue, baseline Baseline) []Issue {
	entries := make(map[string]BaselineEntry, len(baseline.Entries))
	for _, entry := range baseline.Entries {
		entries[baselineIssue(entry).Key()] = entry
	}
	renameByTo := make(map[string]string, len(baseline.Renames))
	for _, rename := range baseline.Renames {
		renameByTo[rename.To] = rename.From
	}
	consumed := make(map[string]struct{}, len(entries))
	result := make([]Issue, 0, len(issues))
	for _, issue := range issues {
		entryKey, entry, ok := baselineEntryForIssue(issue, entries, renameByTo)
		if !ok {
			result = append(result, issue)
			continue
		}
		consumed[entryKey] = struct{}{}
		if drift := baselineDrift(issue, entry); drift != nil {
			result = append(result, *drift)
		}
	}
	result = append(result, staleBaselineIssues(entries, consumed)...)
	return result
}

func baselineEntryForIssue(issue Issue, entries map[string]BaselineEntry, renameByTo map[string]string) (string, BaselineEntry, bool) {
	key := issue.Key()
	entry, ok := entries[key]
	if ok {
		return key, entry, true
	}
	oldKey, renamed := renameByTo[key]
	if !renamed {
		return "", BaselineEntry{}, false
	}
	entry, ok = entries[oldKey]
	return oldKey, entry, ok
}

func baselineDrift(issue Issue, entry BaselineEntry) *Issue {
	old := baselineIssue(entry)
	if metricRule(issue.Rule) {
		if issue.Value == old.Value {
			return nil
		}
		return &Issue{Rule: "baseline-drift", Module: issue.Module, Package: issue.Package, File: issue.File, Symbol: issue.Symbol, Value: issue.Value, Limit: old.Value, Message: fmt.Sprintf("baseline value is %d but current value is %d; lower the baseline after a reduction and investigate increases", old.Value, issue.Value)}
	}
	if issue.Message == old.Message {
		return nil
	}
	return &Issue{Rule: "baseline-drift", Module: issue.Module, Package: issue.Package, File: issue.File, Symbol: issue.Symbol, Message: fmt.Sprintf("baseline message changed from %q to %q", old.Message, issue.Message)}
}

func staleBaselineIssues(entries map[string]BaselineEntry, consumed map[string]struct{}) []Issue {
	result := make([]Issue, 0)
	for key, entry := range entries {
		if _, ok := consumed[key]; ok {
			continue
		}
		result = append(result, Issue{Rule: "baseline-stale", Module: entry.Module, Package: entry.Package, File: entry.File, Symbol: entry.Symbol, Message: fmt.Sprintf("baseline entry %q no longer describes a current violation; delete it", key)})
	}
	return result
}

// compareBaselineHistory guards the other half of a deletion-only baseline:
// a branch cannot add a new exemption or raise an old ceiling and have that
// edit approve itself. If the merge base predates the baseline, the source
// inventory at that merge base is used as a bootstrap ceiling. This makes the
// first baseline review explicit while still rejecting entries for violations
// that did not exist in the reviewed source tree.
func compareBaselineHistory(ctx context.Context, gitBinary, repoRoot, baselinePath, base string, current Baseline, policies ...Policy) []Issue {
	relative, err := filepath.Rel(repoRoot, baselinePath)
	if err != nil || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return []Issue{{Rule: "baseline-history", File: baselinePath, Message: "baseline path must be inside the repository for merge-base comparison"}}
	}
	mergeBase, err := gitOutput(ctx, gitBinary, repoRoot, "merge-base", base, "HEAD")
	if err != nil {
		return []Issue{{Rule: "baseline-history", File: filepath.ToSlash(relative), Message: fmt.Sprintf("cannot resolve merge base %q: %v", base, err)}}
	}
	mergeBaseName := strings.TrimSpace(string(mergeBase))
	if current.SourceCommit != "" && current.SourceCommit != mergeBaseName {
		return []Issue{{Rule: "baseline-history-source", File: filepath.ToSlash(relative), Message: fmt.Sprintf("baseline source_commit %q does not identify merge base %s", current.SourceCommit, mergeBaseName)}}
	}
	oldData, err := gitOutput(ctx, gitBinary, repoRoot, "show", mergeBaseName+":"+filepath.ToSlash(relative))
	if err != nil {
		policy := Policy{Version: policyVersion, Limits: defaultLimits()}
		if len(policies) > 0 {
			policy = policies[0]
		}
		return compareBootstrapBaseline(ctx, gitBinary, repoRoot, relative, mergeBaseName, current, policy)
	}
	previous, err := decodeHistoricalBaseline(oldData)
	if err != nil {
		return []Issue{{Rule: "baseline-history", File: filepath.ToSlash(relative), Message: err.Error()}}
	}
	return compareHistoricalEntries(relative, previous, current)
}

func decodeHistoricalBaseline(data []byte) (Baseline, error) {
	var previous Baseline
	if err := json.Unmarshal(data, &previous); err != nil {
		return Baseline{}, fmt.Errorf("merge-base baseline is invalid: %w", err)
	}
	if err := validateBaseline(previous); err != nil {
		return Baseline{}, fmt.Errorf("merge-base baseline is invalid: %w", err)
	}
	return previous, nil
}

func compareHistoricalEntries(relative string, previous, current Baseline) []Issue {
	oldEntries := baselineEntries(previous.Entries)
	newEntries := baselineEntries(current.Entries)
	result := make([]Issue, 0)
	if previous.SourceCommit != "" && current.SourceCommit != "" && previous.SourceCommit != current.SourceCommit {
		result = append(result, Issue{Rule: "baseline-history-source", File: filepath.ToSlash(relative), Message: "baseline source_commit changed relative to merge base"})
	}
	result = append(result, historyCeilingIssues(oldEntries, current)...)
	result = append(result, historyAddedIssues(oldEntries, current)...)
	result = append(result, historyRenameIssues(relative, oldEntries, newEntries, current.Renames)...)
	return result
}

func baselineEntries(entries []BaselineEntry) map[string]BaselineEntry {
	result := make(map[string]BaselineEntry, len(entries))
	for _, entry := range entries {
		result[baselineIssue(entry).Key()] = entry
	}
	return result
}

func historyCeilingIssues(oldEntries map[string]BaselineEntry, current Baseline) []Issue {
	result := make([]Issue, 0)
	for key, entry := range currentEntriesSorted(current.Entries) {
		oldKey := key
		if source, renamed := renameSourceFor(current.Renames, key); renamed {
			oldKey = source
		}
		previousEntry, ok := oldEntries[oldKey]
		if !ok {
			continue
		}
		if metricRule(entry.Rule) && entry.Value > previousEntry.Value {
			result = append(result, Issue{Rule: "baseline-history-increase", Module: entry.Module, Package: entry.Package, File: entry.File, Symbol: entry.Symbol, Value: entry.Value, Limit: previousEntry.Value, Message: "baseline ceiling increased relative to merge base"})
		}
		if !metricRule(entry.Rule) && entry.Message != previousEntry.Message {
			result = append(result, Issue{Rule: "baseline-history-increase", Module: entry.Module, Package: entry.Package, File: entry.File, Symbol: entry.Symbol, Message: "baseline message changed relative to merge base"})
		}
	}
	return result
}

func historyAddedIssues(oldEntries map[string]BaselineEntry, current Baseline) []Issue {
	result := make([]Issue, 0)
	for key, entry := range currentEntriesSorted(current.Entries) {
		if _, ok := oldEntries[key]; ok {
			continue
		}
		oldKey, mapped := renameSourceFor(current.Renames, key)
		if mapped {
			if _, exists := oldEntries[oldKey]; exists {
				continue
			}
		}
		result = append(result, Issue{Rule: "baseline-history-add", Module: entry.Module, Package: entry.Package, File: entry.File, Symbol: entry.Symbol, Message: "new baseline exemption requires an explicit reviewed migration"})
	}
	return result
}

func historyRenameIssues(relative string, oldEntries, newEntries map[string]BaselineEntry, renames []BaselineRename) []Issue {
	result := make([]Issue, 0)
	for _, rename := range renames {
		if _, oldExists := oldEntries[rename.From]; !oldExists {
			continue
		}
		if _, newExists := newEntries[rename.To]; !newExists {
			result = append(result, Issue{Rule: "baseline-history-rename", File: filepath.ToSlash(relative), Message: fmt.Sprintf("baseline rename target %q is missing", rename.To)})
		}
	}
	return result
}

func currentEntriesSorted(entries []BaselineEntry) map[string]BaselineEntry {
	result := make(map[string]BaselineEntry, len(entries))
	for _, entry := range entries {
		result[baselineIssue(entry).Key()] = entry
	}
	return result
}

func renameSourceFor(renames []BaselineRename, target string) (string, bool) {
	for _, rename := range renames {
		if rename.To == target {
			return rename.From, true
		}
	}
	return "", false
}

func gitOutput(ctx context.Context, gitBinary, dir string, args ...string) ([]byte, error) {
	if strings.TrimSpace(gitBinary) == "" {
		gitBinary = "git"
	}
	command := exec.CommandContext(ctx, gitBinary, args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func baselineJSON(baseline Baseline) ([]byte, error) {
	sort.Slice(baseline.Entries, func(i, j int) bool {
		return baselineIssue(baseline.Entries[i]).Key() < baselineIssue(baseline.Entries[j]).Key()
	})
	encoder := strings.Builder{}
	jsonEncoder := json.NewEncoder(&encoder)
	jsonEncoder.SetIndent("", "  ")
	if err := jsonEncoder.Encode(baseline); err != nil {
		return nil, err
	}
	return []byte(encoder.String()), nil
}
