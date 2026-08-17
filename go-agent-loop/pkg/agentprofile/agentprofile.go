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

// ErrUnknownProfile identifies an unsafe or absent profile name.
var ErrUnknownProfile = errors.New("unknown agent profile")

// ErrMalformedProfile identifies a profile whose instructions or declaration cannot be used safely.
var ErrMalformedProfile = errors.New("malformed agent profile")

// UnknownProfileError reports an unsafe or absent name.
type UnknownProfileError struct{ Name, Reason string }

func (e *UnknownProfileError) Error() string {
	return fmt.Sprintf("unknown agent profile %q: %s", e.Name, e.Reason)
}

func (e *UnknownProfileError) Unwrap() error { return ErrUnknownProfile }

// MalformedProfileError identifies a profile and actionable reason.
type MalformedProfileError struct{ Profile, Reason string }

func (e *MalformedProfileError) Error() string {
	return fmt.Sprintf("malformed agent profile %q: %s", e.Profile, e.Reason)
}

func (e *MalformedProfileError) Unwrap() error { return ErrMalformedProfile }

// Loader reads profiles from an injected filesystem root.
type Loader struct{ root fs.FS }

// NewLoader constructs a loader for root.
func NewLoader(root fs.FS) *Loader { return &Loader{root: root} }

// Load reads one profile from root.
func Load(root fs.FS, name string) (Profile, error) { return NewLoader(root).Load(name) }

// Load reads one profile by canonical directory name.
func (l *Loader) Load(name string) (Profile, error) {
	if !validProfileName(name) {
		return Profile{}, &UnknownProfileError{Name: name, Reason: "name must be a single safe catalog entry"}
	}
	if l == nil || l.root == nil {
		return Profile{}, malformed(name, "profile filesystem is nil")
	}
	info, err := fs.Stat(l.root, name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Profile{}, &UnknownProfileError{Name: name, Reason: "profile is not present in the supplied catalog"}
	case err != nil:
		return Profile{}, malformed(name, fmt.Sprintf("stat profile: %v", err))
	case !info.IsDir():
		return Profile{}, malformed(name, "catalog entry is not a profile directory")
	}
	instructions, err := readFile(l.root, name, InstructionsFileName)
	if err != nil {
		return Profile{}, malformed(name, err.Error())
	}
	if len(instructions) == 0 || !utf8.Valid(instructions) || strings.TrimSpace(string(instructions)) == "" {
		return Profile{}, malformed(name, fmt.Sprintf("%s must contain non-empty UTF-8 instructions", InstructionsFileName))
	}
	declarationName, err := findDeclaration(l.root, name)
	if err != nil {
		return Profile{}, malformed(name, err.Error())
	}
	declaration, err := readFile(l.root, name, declarationName)
	if err != nil {
		return Profile{}, malformed(name, err.Error())
	}
	outcome, err := parseOutcome(declaration)
	if err != nil {
		return Profile{}, malformed(name, err.Error())
	}
	return Profile{Name: name, Instructions: string(instructions), ExpectedOutcome: outcome}, nil
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

func readFile(root fs.FS, profile, name string) ([]byte, error) {
	data, err := fs.ReadFile(root, path.Join(profile, name))
	if err != nil {
		return nil, fmt.Errorf("read %s: %v", name, err)
	}
	return data, nil
}

func validProfileName(name string) bool {
	return name != "" && name == strings.TrimSpace(name) && fs.ValidPath(name) &&
		!strings.ContainsAny(name, `/\`) && strings.IndexFunc(name, func(r rune) bool {
		return !(r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	}) == -1
}

func findDeclaration(root fs.FS, profile string) (string, error) {
	entries, err := fs.ReadDir(root, profile)
	if err != nil {
		return "", fmt.Errorf("read profile directory: %v", err)
	}
	name, count := "", 0
	for _, entry := range entries {
		if !entry.IsDir() && (entry.Name() == ExpectedOutcomeFileName || entry.Name() == "expected.json") {
			name, count = entry.Name(), count+1
		}
	}
	switch count {
	case 0:
		return "", errors.New("missing expected-outcome declaration")
	case 1:
		return name, nil
	default:
		return "", fmt.Errorf("expected exactly one expected-outcome declaration, found %d", count)
	}
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
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return ExpectedOutcome{}, errors.New("expected-outcome declaration contains multiple JSON values")
		}
		return ExpectedOutcome{}, fmt.Errorf("decode trailing expected-outcome data: %v", err)
	}

	kind := OutcomeKind(declaration.Kind)
	switch kind {
	case OutcomeShellCommand, OutcomeFileRead, OutcomeImageDescription, OutcomeOrderedTools, OutcomeNoTools:
	default:
		return ExpectedOutcome{}, fmt.Errorf("invalid outcome kind %q", declaration.Kind)
	}
	if (declaration.Command != nil && kind != OutcomeShellCommand) || (declaration.TargetFile != nil && kind != OutcomeFileRead) ||
		(declaration.ImageRequirement != nil && kind != OutcomeImageDescription) || (declaration.OrderedCalls != nil && kind != OutcomeOrderedTools) ||
		(declaration.FirstResultInformsSecond != nil && kind != OutcomeOrderedTools) || (declaration.CallCount != nil && kind != OutcomeNoTools) {
		return ExpectedOutcome{}, fmt.Errorf("outcome contains fields not valid for outcome kind %q", kind)
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
		for i, call := range *declaration.OrderedCalls {
			if strings.TrimSpace(call) == "" {
				return ExpectedOutcome{}, fmt.Errorf("ordered-multi-tool outcome call %d must be non-empty", i)
			}
		}
		if declaration.FirstResultInformsSecond == nil || !*declaration.FirstResultInformsSecond {
			return ExpectedOutcome{}, errors.New("ordered-multi-tool outcome requires first_result_informs_second=true")
		}
		return ExpectedOutcome{Kind: kind, OrderedCalls: append([]string(nil), (*declaration.OrderedCalls)...), FirstResultInformsSecond: true}, nil
	case OutcomeNoTools:
		if declaration.CallCount == nil || *declaration.CallCount != 0 {
			return ExpectedOutcome{}, errors.New("no-tools outcome requires call_count=0")
		}
		return ExpectedOutcome{Kind: kind, CallCount: 0}, nil
	}
	return ExpectedOutcome{}, fmt.Errorf("invalid outcome kind %q", declaration.Kind)
}

func validTargetFile(name string) bool {
	return name != "" && name != "." && name == strings.TrimSpace(name) && fs.ValidPath(name) && !path.IsAbs(name) && !strings.ContainsAny(name, ":\\")
}

func malformed(profile, reason string) *MalformedProfileError {
	return &MalformedProfileError{Profile: profile, Reason: reason}
}
