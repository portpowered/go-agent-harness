package sysinfo

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

// Info holds runtime system information injected into the system prompt.
type Info struct {
	OS       string
	Arch     string
	Cwd      string
	Model    string
	Provider string
	Time     time.Time
}

// Collect gathers current system information. cwd is the working directory
// resolved by the host boundary; an empty value is omitted from the formatted
// section. model and provider describe the active model configuration and are
// included when non-empty.
func Collect(cwd, model, provider string) Info {
	return Info{
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Cwd:      cwd,
		Model:    model,
		Provider: provider,
		Time:     time.Now(),
	}
}

// Format renders the system info as a markdown section for prepending to a system prompt.
func (i Info) Format() string {
	var sb strings.Builder
	fmt.Fprintln(&sb, "## System Information")
	fmt.Fprintln(&sb)
	fmt.Fprintf(&sb, "- **Date/Time**: %s\n", i.Time.Format(time.RFC3339))
	fmt.Fprintf(&sb, "- **OS**: %s\n", i.OS)
	fmt.Fprintf(&sb, "- **Architecture**: %s\n", i.Arch)
	if i.Cwd != "" {
		fmt.Fprintf(&sb, "- **Working Directory**: `%s`\n", i.Cwd)
	}
	if i.Provider != "" {
		fmt.Fprintf(&sb, "- **Provider**: %s\n", i.Provider)
	}
	if i.Model != "" {
		fmt.Fprintf(&sb, "- **Model**: %s\n", i.Model)
	}
	fmt.Fprintln(&sb)
	return sb.String()
}
