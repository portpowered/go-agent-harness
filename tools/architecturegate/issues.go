package main

import "fmt"

type Issue struct {
	Rule    string `json:"rule"`
	Module  string `json:"module,omitempty"`
	Package string `json:"package,omitempty"`
	File    string `json:"file,omitempty"`
	Symbol  string `json:"symbol,omitempty"`
	Value   int    `json:"value,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Message string `json:"message"`
}

func (i Issue) String() string {
	location := i.Package
	if i.File != "" {
		location = i.File
	}
	if i.Symbol != "" {
		location += ":" + i.Symbol
	}
	if location == "" {
		location = i.Module
	}
	if i.Value != 0 || i.Limit != 0 {
		return fmt.Sprintf("%s %s (%d > %d): %s", i.Rule, location, i.Value, i.Limit, i.Message)
	}
	return fmt.Sprintf("%s %s: %s", i.Rule, location, i.Message)
}

func (i Issue) Key() string {
	return i.Rule + "\x00" + i.Module + "\x00" + i.Package + "\x00" + i.File + "\x00" + i.Symbol
}

func (i Issue) Less(other Issue) bool {
	if i.Key() != other.Key() {
		return i.Key() < other.Key()
	}
	if i.Value != other.Value {
		return i.Value < other.Value
	}
	return i.Message < other.Message
}

func metricRule(rule string) bool {
	switch rule {
	case "package-files", "file-lines", "function-lines", "function-statements", "cognitive-complexity", "cyclomatic-complexity":
		return true
	default:
		return false
	}
}
