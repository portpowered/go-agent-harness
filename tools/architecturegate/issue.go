package main

import "fmt"

// Issue is one architecture finding. Its key identifies the rule and the
// location (module, package, file, symbol) it applies to.
type Issue struct {
	Rule    string `json:"rule"`
	Module  string `json:"module,omitempty"`
	Package string `json:"package,omitempty"`
	File    string `json:"file,omitempty"`
	Symbol  string `json:"symbol,omitempty"`
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
	return fmt.Sprintf("%s %s: %s", i.Rule, location, i.Message)
}

func (i Issue) Key() string {
	return i.Rule + "\x00" + i.Module + "\x00" + i.Package + "\x00" + i.File + "\x00" + i.Symbol
}

func (i Issue) Less(other Issue) bool {
	if i.Key() != other.Key() {
		return i.Key() < other.Key()
	}
	return i.Message < other.Message
}
