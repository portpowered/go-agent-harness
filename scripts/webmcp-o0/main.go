package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
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
		cancelProbeTarget(browserContext)
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

// probeCommand describes one subcommand: the operands it requires after its
// name and the probe it runs. A nil report prints nothing.
type probeCommand struct {
	operands []string
	run      func(operands []string) (any, error)
}

const endpointOperand = "<browser-websocket-endpoint>"

func probeCommands() map[string]probeCommand {
	return map[string]probeCommand{
		"cdp-version": {operands: []string{endpointOperand}, run: func(operands []string) (any, error) {
			return readCDPVersion(operands[0])
		}},
		"webmcp-matrix": {operands: []string{endpointOperand}, run: func(operands []string) (any, error) {
			return runWebMCPMatrix(operands[0])
		}},
		"detach-probe": {operands: []string{endpointOperand, "<target-id>", "<initial|reattach>"}, run: func(operands []string) (any, error) {
			return runDetachProbe(operands[0], operands[1], operands[2])
		}},
		"serve-detach-fixture": {run: func([]string) (any, error) {
			return nil, serveDetachFixture()
		}},
		"hermetic": {operands: []string{endpointOperand}, run: func(operands []string) (any, error) {
			return runHermeticProbe(operands[0])
		}},
		"watch-cross-process": {operands: []string{endpointOperand, "<agent-cli-binary>"}, run: func(operands []string) (any, error) {
			return runWatchCrossProcessProbe(operands[0], operands[1])
		}},
	}
}

const (
	exitProbeFailed = 1
	exitUsage       = 2
)

// runProbeCommand runs the subcommand named by args[1] and returns the
// process exit status.
func runProbeCommand(args []string) int {
	command, ok := probeCommands()[args[1]]
	if !ok {
		fmt.Fprintln(os.Stderr, "usage: go run . [cdp-version <browser-websocket-endpoint> | webmcp-matrix <browser-websocket-endpoint> | detach-probe <browser-websocket-endpoint> <target-id> <initial|reattach> | serve-detach-fixture | hermetic <browser-websocket-endpoint> | watch-cross-process <browser-websocket-endpoint> <agent-cli-binary>]")
		return exitUsage
	}
	operands := args[2:]
	if len(operands) != len(command.operands) {
		fmt.Fprintln(os.Stderr, "usage: go run . "+strings.Join(append([]string{args[1]}, command.operands...), " "))
		return exitUsage
	}
	report, err := command.run(operands)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", args[1], err)
		return exitProbeFailed
	}
	if report != nil {
		printJSON(report)
	}
	return 0
}

func main() {
	if len(os.Args) > 1 {
		if status := runProbeCommand(os.Args); status != 0 {
			os.Exit(status)
		}
		return
	}

	printJSON(bindingSmoke())
}
