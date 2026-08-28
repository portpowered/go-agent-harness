package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/chromedp/cdproto/browser"
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

type cdpVersionReport struct {
	Endpoint        string `json:"endpoint"`
	GoVersion       string `json:"goVersion"`
	ProtocolVersion string `json:"protocolVersion"`
	Product         string `json:"product"`
	Revision        string `json:"revision"`
	UserAgent       string `json:"userAgent"`
	JSVersion       string `json:"jsVersion"`
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

func readCDPVersion(endpoint string) (cdpVersionReport, error) {
	if endpoint == "" {
		return cdpVersionReport{}, fmt.Errorf("CDP endpoint is empty")
	}

	rootContext, cancelRoot := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelRoot()

	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(rootContext, endpoint, chromedp.NoModifyURL)
	defer cancelAllocator()

	browserContext, cancelBrowser := chromedp.NewContext(allocatorContext)
	defer func() {
		// NewRemoteAllocator never owns the browser process. Cancel the remote
		// client context so only the temporary tab created by this probe is
		// detached and closed; the shell launcher owns browser termination.
		_ = chromedp.Cancel(browserContext)
		cancelBrowser()
	}()

	report := cdpVersionReport{
		Endpoint:  endpoint,
		GoVersion: runtime.Version(),
	}
	if err := chromedp.Run(browserContext, chromedp.ActionFunc(func(actionContext context.Context) error {
		c := chromedp.FromContext(actionContext)
		if c == nil || c.Browser == nil {
			return fmt.Errorf("remote browser was not initialized")
		}

		var err error
		report.ProtocolVersion, report.Product, report.Revision, report.UserAgent, report.JSVersion, err = browser.GetVersion().Do(cdp.WithExecutor(actionContext, c.Browser))
		return err
	})); err != nil {
		return cdpVersionReport{}, fmt.Errorf("Browser.getVersion: %w", err)
	}

	return report, nil
}

func printJSON(value any) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "encode JSON: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(encoded))
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "cdp-version":
			if len(os.Args) != 3 {
				fmt.Fprintln(os.Stderr, "usage: go run . cdp-version <browser-websocket-endpoint>")
				os.Exit(2)
			}
			report, err := readCDPVersion(os.Args[2])
			if err != nil {
				fmt.Fprintf(os.Stderr, "cdp-version: %v\n", err)
				os.Exit(1)
			}
			printJSON(report)
			return
		case "webmcp-matrix":
			if len(os.Args) != 3 {
				fmt.Fprintln(os.Stderr, "usage: go run . webmcp-matrix <browser-websocket-endpoint>")
				os.Exit(2)
			}
			report, err := runWebMCPMatrix(os.Args[2])
			if err != nil {
				fmt.Fprintf(os.Stderr, "webmcp-matrix: %v\n", err)
				os.Exit(1)
			}
			printJSON(report)
			return
		case "detach-probe":
			if len(os.Args) != 5 {
				fmt.Fprintln(os.Stderr, "usage: go run . detach-probe <browser-websocket-endpoint> <target-id> <initial|reattach>")
				os.Exit(2)
			}
			report, err := runDetachProbe(os.Args[2], os.Args[3], os.Args[4])
			if err != nil {
				fmt.Fprintf(os.Stderr, "detach-probe: %v\n", err)
				os.Exit(1)
			}
			printJSON(report)
			return
		case "serve-detach-fixture":
			if len(os.Args) != 2 {
				fmt.Fprintln(os.Stderr, "usage: go run . serve-detach-fixture")
				os.Exit(2)
			}
			if err := serveDetachFixture(); err != nil {
				fmt.Fprintf(os.Stderr, "serve-detach-fixture: %v\n", err)
				os.Exit(1)
			}
			return
		case "hermetic":
			if len(os.Args) != 3 {
				fmt.Fprintln(os.Stderr, "usage: go run . hermetic <browser-websocket-endpoint>")
				os.Exit(2)
			}
			report, err := runHermeticProbe(os.Args[2])
			if err != nil {
				fmt.Fprintf(os.Stderr, "hermetic: %v\n", err)
				os.Exit(1)
			}
			printJSON(report)
			return
		default:
			fmt.Fprintln(os.Stderr, "usage: go run . [cdp-version <browser-websocket-endpoint> | webmcp-matrix <browser-websocket-endpoint> | detach-probe <browser-websocket-endpoint> <target-id> <initial|reattach> | serve-detach-fixture | hermetic <browser-websocket-endpoint>]")
			os.Exit(2)
		}
	}

	printJSON(bindingSmoke())
}
