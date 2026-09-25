package objective

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

// OrderedCheck passes when expected appears as a subsequence of observed.
func OrderedCheck(observed []ObservedOperation, expected []string) Check {
	check := Check{
		Expected:         StringList(expected),
		Actual:           StringList(observedNames(observed)),
		EvidenceArtifact: transcript.BrowserArtifactDefaultPath,
	}
	if len(expected) == 0 {
		check.Passed = true
		return check
	}
	observedIndex := 0
	for _, wanted := range expected {
		for observedIndex < len(observed) && observed[observedIndex].Name != wanted {
			observedIndex++
		}
		if observedIndex == len(observed) {
			if len(observed) > 0 {
				last := observed[len(observed)-1]
				check.OperationPosition = last.OperationPosition
				check.EventPosition = last.EventPosition
			}
			return check
		}
		observedIndex++
	}
	check.Passed = true
	return check
}

// AllowedCheck passes when every observed name is in allowed.
func AllowedCheck(observed []ObservedOperation, allowed []string) Check {
	check := Check{
		Expected:         StringList(allowed),
		Actual:           StringList(observedNames(observed)),
		EvidenceArtifact: transcript.BrowserArtifactDefaultPath,
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		allowedSet[value] = struct{}{}
	}
	for _, item := range observed {
		if _, ok := allowedSet[item.Name]; ok {
			continue
		}
		check.OperationPosition = item.OperationPosition
		check.EventPosition = item.EventPosition
		return check
	}
	check.Passed = true
	return check
}

func observedNames(observed []ObservedOperation) []string {
	result := make([]string, 0, len(observed))
	for _, item := range observed {
		result = append(result, item.Name)
	}
	return result
}
