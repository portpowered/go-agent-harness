package main

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/webmcp"
	"github.com/chromedp/chromedp"
)

const (
	chromedpModule = "github.com/chromedp/chromedp"
	cdprotoModule  = "github.com/chromedp/cdproto"
)

// smokeReport is intentionally small and stable enough to paste into the
// decision record. It proves that the selected generated surface can be
// imported and constructed without requiring a browser at compile time.
type smokeReport struct {
	GoVersion       string            `json:"goVersion"`
	GOOS            string            `json:"goos"`
	GOARCH          string            `json:"goarch"`
	ChromedpVersion string            `json:"chromedpVersion"`
	CDProtoVersion  string            `json:"cdprotoVersion"`
	Commands        []string          `json:"commands"`
	EventTypes      []string          `json:"eventTypes"`
	AllocatorURL    string            `json:"allocatorURL"`
	Checks          map[string]string `json:"checks"`
}

// bindingSmoke constructs every WebMCP command and event type required by the
// planned adapter. It deliberately does not issue a command: doing so would
// turn this compatibility check into a browser-availability check.
func bindingSmoke() smokeReport {
	_ = webmcp.Enable()
	_ = webmcp.Disable()
	_ = webmcp.InvokeTool(cdp.FrameID("probe-frame"), "probe.tool", nil)
	_ = webmcp.CancelInvocation("probe-invocation")
	_ = webmcp.EventToolsAdded{}
	_ = webmcp.EventToolsRemoved{}
	_ = webmcp.EventToolInvoked{}
	_ = webmcp.EventToolResponded{}

	const allocatorURL = "http://127.0.0.1:9222"
	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(context.Background(), allocatorURL)
	cancelAllocator()
	_ = allocatorContext

	return smokeReport{
		GoVersion:       runtime.Version(),
		GOOS:            runtime.GOOS,
		GOARCH:          runtime.GOARCH,
		ChromedpVersion: dependencyVersion(chromedpModule),
		CDProtoVersion:  dependencyVersion(cdprotoModule),
		Commands: []string{
			webmcp.CommandEnable,
			webmcp.CommandDisable,
			webmcp.CommandInvokeTool,
			webmcp.CommandCancelInvocation,
		},
		EventTypes: []string{
			"EventToolsAdded",
			"EventToolsRemoved",
			"EventToolInvoked",
			"EventToolResponded",
		},
		AllocatorURL: allocatorURL,
		Checks: map[string]string{
			"generatedWebMCP": "constructed",
			"remoteAllocator": "constructed-and-cancelled",
		},
	}
}

func dependencyVersion(path string) string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unavailable"
	}
	for _, dependency := range info.Deps {
		if dependency.Path == path {
			return dependency.Version
		}
	}
	return "unavailable"
}

func main() {
	report := bindingSmoke()
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		panic(fmt.Errorf("encode smoke report: %w", err))
	}
	fmt.Println(string(encoded))
}
