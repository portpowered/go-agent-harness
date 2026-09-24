package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

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

func parseProfile(reader io.Reader, name string, blocks map[string]profileBlock) (string, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, profileScanInitialBuffer), profileScanMaxLine)
	lineNumber := 0
	mode := ""
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var detail string
		if mode == "" {
			mode, detail = parseProfileMode(line)
		} else {
			detail = addProfileLine(blocks, line)
		}
		if detail != "" {
			return "", profileError(name, lineNumber, detail)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("%w: read %q: %w", ErrProfileInvalid, name, err)
	}
	if mode == "" {
		return "", profileError(name, lineNumber+1, "profile does not declare a coverage mode")
	}
	return mode, nil
}

// Coverage profile lines are short; the scanner starts small and caps any
// single line at 1 MiB.
const (
	profileScanInitialBuffer = 1 << 10
	profileScanMaxLine       = 1 << 20
)

// profileBlockFields is the number of fields in a coverage block line:
// file range, statement count, and execution count.
const profileBlockFields = 3

// parseProfileBlock parses one "file:range statements count" coverage line.
// A non-empty detail explains why the line is invalid.
func parseProfileBlock(line string) (string, profileBlock, string) {
	fields := strings.Fields(line)
	if len(fields) != profileBlockFields {
		return "", profileBlock{}, "coverage block must contain file range, statements, and count"
	}
	fileRange := fields[0]
	colon := strings.LastIndexByte(fileRange, ':')
	if colon <= 0 || !strings.Contains(fileRange[colon+1:], ",") {
		return "", profileBlock{}, "coverage block has an invalid file range"
	}
	packagePath := fileRange[:colon]
	slash := strings.LastIndexByte(packagePath, '/')
	if slash <= 0 || slash == len(packagePath)-1 {
		return "", profileBlock{}, "coverage block has an invalid import path"
	}
	statements, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || statements < 0 {
		return "", profileBlock{}, "statement count is invalid"
	}
	count, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || count < 0 {
		return "", profileBlock{}, "execution count is invalid"
	}
	return fileRange, profileBlock{packagePath: packagePath[:slash], statements: statements, covered: count > 0}, ""
}

// parseProfileMode parses the leading "mode: <mode>" line. A non-empty
// detail explains why the line is invalid.
func parseProfileMode(line string) (string, string) {
	if !strings.HasPrefix(line, "mode: ") {
		return "", "first non-empty line must declare mode"
	}
	mode := strings.TrimSpace(strings.TrimPrefix(line, "mode: "))
	if mode == "" {
		return "", "coverage mode is empty"
	}
	return mode, ""
}

// addProfileLine merges one coverage block line into blocks. A non-empty
// detail explains why the line is invalid.
func addProfileLine(blocks map[string]profileBlock, line string) string {
	fileRange, block, detail := parseProfileBlock(line)
	if detail != "" {
		return detail
	}
	if block.statements == 0 {
		return ""
	}
	if err := mergeProfileBlock(blocks, fileRange, block); err != nil {
		return err.Error()
	}
	return ""
}
