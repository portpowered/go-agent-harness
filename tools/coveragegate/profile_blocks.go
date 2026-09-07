package main

import "fmt"

// A package can appear in many test binaries under -coverpkg. Repeated blocks
// are one statement set, covered if any binary executed it; not new statements.
type profileBlock struct {
	packagePath string
	statements  int64
	covered     bool
}

func mergeProfileBlock(blocks map[string]profileBlock, location string, next profileBlock) error {
	if previous, ok := blocks[location]; ok {
		if previous.statements != next.statements {
			return fmt.Errorf("inconsistent statement count for %s", location)
		}
		next.covered = next.covered || previous.covered
	}
	blocks[location] = next
	return nil
}
