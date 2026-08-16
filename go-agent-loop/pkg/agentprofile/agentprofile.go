// Package agentprofile loads deterministic AGENTS.md tool-steering profiles.
package agentprofile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	InstructionsFileName    = "AGENTS.md"
	ExpectedOutcomeFileName = "expected-outcome.json"
)

// OutcomeKind is the closed set of observable profile behaviors.
type OutcomeKind string

const (
	OutcomeShellCommand     OutcomeKind = "shell-command"
	OutcomeFileRead         OutcomeKind = "file-read"
	OutcomeImageDescription OutcomeKind = "image-description"
	OutcomeOrderedTools     OutcomeKind = "ordered-multi-tool"
	OutcomeNoTools          OutcomeKind = "no-tools"
)

// ExpectedOutcome is the typed declaration paired with a profile.
type ExpectedOutcome struct {
	Kind                     OutcomeKind `json:"kind"`
	Command                  string      `json:"command,omitempty"`
	TargetFile               string      `json:"target_file,omitempty"`
	ImageRequirement         string      `json:"image_requirement,omitempty"`
	OrderedCalls             []string    `json:"ordered_calls,omitempty"`
	FirstResultInformsSecond bool        `json:"first_result_informs_second,omitempty"`
	CallCount                int         `json:"call_count,omitempty"`
}

// Profile is the complete result of loading one named profile.
type Profile struct {
	Name            string
	Instructions    string
	ExpectedOutcome ExpectedOutcome
}

var (
	// ErrUnknownProfile identifies an unsafe or absent profile name.
	ErrUnknownProfile = errors.New("unknown agent profile")
	// ErrMalformedProfile identifies a profile whose instructions or
	// declaration cannot be used safely and deterministically.
	ErrMalformedProfile = errors.New("malformed agent profile")
)

// UnknownProfileError reports an unsafe or absent name.
type UnknownProfileError struct {
	Name   string
	Reason string
}

func (e *UnknownProfileError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("unknown agent profile %q: %s", e.Name, e.Reason)
}

func (e *UnknownProfileError) Unwrap() error { return ErrUnknownProfile }

// MalformedProfileError identifies a profile and actionable reason.
type MalformedProfileError struct {
	Profile string
	Reason  string
}

func (e *MalformedProfileError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("malformed agent profile %q: %s", e.Profile, e.Reason)
}

func (e *MalformedProfileError) Unwrap() error { return ErrMalformedProfile }

// Loader reads profiles from an injected filesystem root.
type Loader struct {
	root fs.FS
}

// NewLoader constructs a loader for root.
func NewLoader(root fs.FS) *Loader { return &Loader{root: root} }

// Load reads one profile by canonical directory name.
func (l *Loader) Load(name string) (Profile, error) {
	if !validProfileName(name) {
		return Profile{}, &UnknownProfileError{Name: name, Reason: "name must be a single safe catalog entry"}
	}
	if l == nil || l.root == nil {
		return Profile{}, malformed(name, "profile filesystem is nil")
	}

	info, err := fs.Stat(l.root, name)
	if errors.Is(err, fs.ErrNotExist) {
		return Profile{}, &UnknownProfileError{Name: name, Reason: "profile is not present in the supplied catalog"}
	}
	if err != nil {
		return Profile{}, malformed(name, fmt.Sprintf("stat profile: %v", err))
	}
	if !info.IsDir() {
		return Profile{}, malformed(name, "catalog entry is not a profile directory")
	}

	instructionsData, err := fs.ReadFile(l.root, path.Join(name, InstructionsFileName))
	if err != nil {
		return Profile{}, malformed(name, fmt.Sprintf("read %s: %v", InstructionsFileName, err))
	}
	if !validInstructions(instructionsData) {
		return Profile{}, malformed(name, fmt.Sprintf("%s must contain non-empty UTF-8 instructions", InstructionsFileName))
	}

	declarationName, err := findDeclaration(l.root, name)
	if err != nil {
		return Profile{}, malformed(name, err.Error())
	}
	declarationData, err := fs.ReadFile(l.root, path.Join(name, declarationName))
	if err != nil {
		return Profile{}, malformed(name, fmt.Sprintf("read %s: %v", declarationName, err))
	}
	outcome, err := parseOutcome(declarationData)
	if err != nil {
		return Profile{}, malformed(name, err.Error())
	}

	return Profile{
		Name:            name,
		Instructions:    string(instructionsData),
		ExpectedOutcome: outcome,
	}, nil
}

// Load reads one profile from root.
func Load(root fs.FS, name string) (Profile, error) {
	return NewLoader(root).Load(name)
}

// Names returns sorted profile directory names and rejects catalog-only files.
func (l *Loader) Names() ([]string, error) {
	if l == nil || l.root == nil {
		return nil, malformed("<catalog>", "profile filesystem is nil")
	}
	entries, err := fs.ReadDir(l.root, ".")
	if err != nil {
		return nil, malformed("<catalog>", fmt.Sprintf("read profile root: %v", err))
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			return nil, malformed("<catalog>", fmt.Sprintf("entry %q is not a profile directory", entry.Name()))
		}
		if !validProfileName(entry.Name()) {
			return nil, malformed(entry.Name(), "profile directory name is not a safe canonical name")
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

// Catalog loads and validates every profile in root.
func (l *Loader) Catalog() ([]Profile, error) {
	names, err := l.Names()
	if err != nil {
		return nil, err
	}
	profiles := make([]Profile, 0, len(names))
	for _, name := range names {
		profile, err := l.Load(name)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

func validProfileName(name string) bool {
	if name == "" || name != strings.TrimSpace(name) || !fs.ValidPath(name) {
		return false
	}
	if strings.ContainsAny(name, `/\\`) || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		if !(r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func validInstructions(data []byte) bool {
	return len(data) > 0 && utf8.Valid(data) && strings.TrimSpace(string(data)) != ""
}

func isDeclarationName(name string) bool {
	return name == ExpectedOutcomeFileName || name == "expected.json"
}

func findDeclaration(root fs.FS, profileName string) (string, error) {
	entries, err := fs.ReadDir(root, profileName)
	if err != nil {
		return "", fmt.Errorf("read profile directory: %v", err)
	}
	declarations := make([]string, 0, 1)
	for _, entry := range entries {
		if !entry.IsDir() && isDeclarationName(entry.Name()) {
			declarations = append(declarations, entry.Name())
		}
	}
	if len(declarations) == 0 {
		return "", errors.New("missing expected-outcome declaration")
	}
	if len(declarations) != 1 {
		return "", fmt.Errorf("expected exactly one expected-outcome declaration, found %d", len(declarations))
	}
	return declarations[0], nil
}

type outcomeDeclaration struct {
	Kind                     string    `json:"kind"`
	Command                  *string   `json:"command"`
	TargetFile               *string   `json:"target_file"`
	ImageRequirement         *string   `json:"image_requirement"`
	OrderedCalls             *[]string `json:"ordered_calls"`
	FirstResultInformsSecond *bool     `json:"first_result_informs_second"`
	CallCount                *int      `json:"call_count"`
}

func parseOutcome(data []byte) (ExpectedOutcome, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var declaration outcomeDeclaration
	if err := decoder.Decode(&declaration); err != nil {
		return ExpectedOutcome{}, fmt.Errorf("decode expected-outcome declaration: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return ExpectedOutcome{}, errors.New("expected-outcome declaration contains multiple JSON values")
		}
		return ExpectedOutcome{}, fmt.Errorf("decode trailing expected-outcome data: %v", err)
	}

	kind := OutcomeKind(declaration.Kind)
	if !isKnownOutcome(kind) {
		return ExpectedOutcome{}, fmt.Errorf("invalid outcome kind %q", declaration.Kind)
	}
	if err := rejectIrrelevantFields(declaration, kind); err != nil {
		return ExpectedOutcome{}, err
	}

	switch kind {
	case OutcomeShellCommand:
		if declaration.Command == nil || strings.TrimSpace(*declaration.Command) == "" {
			return ExpectedOutcome{}, errors.New("shell-command outcome requires a non-empty command")
		}
		return ExpectedOutcome{Kind: kind, Command: *declaration.Command}, nil
	case OutcomeFileRead:
		if declaration.TargetFile == nil || !validTargetFile(*declaration.TargetFile) {
			return ExpectedOutcome{}, errors.New("file-read outcome requires a safe relative target_file")
		}
		return ExpectedOutcome{Kind: kind, TargetFile: *declaration.TargetFile}, nil
	case OutcomeImageDescription:
		if declaration.ImageRequirement == nil || strings.TrimSpace(*declaration.ImageRequirement) == "" {
			return ExpectedOutcome{}, errors.New("image-description outcome requires a non-empty image_requirement")
		}
		return ExpectedOutcome{Kind: kind, ImageRequirement: *declaration.ImageRequirement}, nil
	case OutcomeOrderedTools:
		if declaration.OrderedCalls == nil || len(*declaration.OrderedCalls) != 2 {
			return ExpectedOutcome{}, errors.New("ordered-multi-tool outcome requires exactly two ordered_calls")
		}
		for index, call := range *declaration.OrderedCalls {
			if strings.TrimSpace(call) == "" {
				return ExpectedOutcome{}, fmt.Errorf("ordered-multi-tool outcome call %d must be non-empty", index)
			}
		}
		if declaration.FirstResultInformsSecond == nil || !*declaration.FirstResultInformsSecond {
			return ExpectedOutcome{}, errors.New("ordered-multi-tool outcome requires first_result_informs_second=true")
		}
		return ExpectedOutcome{
			Kind: kind, OrderedCalls: append([]string(nil), (*declaration.OrderedCalls)...),
			FirstResultInformsSecond: true,
		}, nil
	case OutcomeNoTools:
		if declaration.CallCount == nil || *declaration.CallCount != 0 {
			return ExpectedOutcome{}, errors.New("no-tools outcome requires call_count=0")
		}
		return ExpectedOutcome{Kind: kind, CallCount: 0}, nil
	default:
		return ExpectedOutcome{}, fmt.Errorf("invalid outcome kind %q", declaration.Kind)
	}
}

func rejectIrrelevantFields(declaration outcomeDeclaration, kind OutcomeKind) error {
	fields := []struct {
		name string
		set  bool
		want OutcomeKind
	}{
		{"command", declaration.Command != nil, OutcomeShellCommand},
		{"target_file", declaration.TargetFile != nil, OutcomeFileRead},
		{"image_requirement", declaration.ImageRequirement != nil, OutcomeImageDescription},
		{"ordered_calls", declaration.OrderedCalls != nil, OutcomeOrderedTools},
		{"first_result_informs_second", declaration.FirstResultInformsSecond != nil, OutcomeOrderedTools},
		{"call_count", declaration.CallCount != nil, OutcomeNoTools},
	}
	for _, field := range fields {
		if field.set && field.want != kind {
			return fmt.Errorf("field %s is not valid for outcome kind %q", field.name, kind)
		}
	}
	return nil
}

func validTargetFile(name string) bool {
	if name == "" || name != strings.TrimSpace(name) || !fs.ValidPath(name) || name == "." {
		return false
	}
	return !path.IsAbs(name) && !strings.ContainsAny(name, ":\\")
}

func isKnownOutcome(kind OutcomeKind) bool {
	switch kind {
	case OutcomeShellCommand, OutcomeFileRead, OutcomeImageDescription, OutcomeOrderedTools, OutcomeNoTools:
		return true
	default:
		return false
	}
}

func malformed(profile, reason string) *MalformedProfileError {
	return &MalformedProfileError{Profile: profile, Reason: reason}
}
