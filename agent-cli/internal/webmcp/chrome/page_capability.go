package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/chromedp/cdproto/runtime"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// pageToolCatalogProbeExpression intentionally reads only the page's public
// WebMCP producer. It does not retain page data or execute a page tool.
const pageToolCatalogProbeExpression = `(async () => {
  const readyState = (typeof document !== "undefined" &&
    typeof document.readyState === "string") ? document.readyState : "";
  const readiness = {
    document_ready_state: readyState,
    document_loading: readyState === "loading",
    document_loading_known: readyState === "loading" ||
      readyState === "interactive" || readyState === "complete"
  };
  const context = (typeof document !== "undefined" && document.modelContext) ||
    (typeof navigator !== "undefined" && navigator.modelContext);
  const policy = (typeof document !== "undefined" &&
    (document.permissionsPolicy || document.featurePolicy));
  if (policy && typeof policy.allowsFeature === "function" &&
      !policy.allowsFeature("tools")) {
    return Object.assign({producer_present: false, catalog_ready: false, tool_count: 0}, readiness);
  }
  if (!context || typeof context.getTools !== "function") {
    return Object.assign({producer_present: false, catalog_ready: false, tool_count: 0}, readiness);
  }
	try {
	    const tools = await context.getTools();
	    if (!Array.isArray(tools)) {
	      return Object.assign({producer_present: true, catalog_ready: false, tool_count: 0}, readiness);
	    }
	    return Object.assign({
	      producer_present: true,
	      // An empty loading document has not yet provided affirmative catalog
	      // evidence. Once loading completes, the same empty list is explicit
	      // empty-catalog evidence.
	      catalog_ready: tools.length > 0 || readyState !== "loading",
	      tool_count: tools.length
	    }, readiness);
  } catch (_) {
    return Object.assign({producer_present: true, catalog_ready: false, tool_count: 0}, readiness);
  }
})()`

type pageToolCatalogProbe struct {
	ProducerPresent      bool   `json:"producer_present"`
	CatalogReady         bool   `json:"catalog_ready"`
	ToolCount            int    `json:"tool_count"`
	DocumentReadyState   string `json:"document_ready_state"`
	DocumentLoading      bool   `json:"document_loading"`
	DocumentLoadingKnown bool   `json:"document_loading_known"`
}

func evaluatePageToolCatalog(ctx context.Context) (pageToolCatalogProbe, error) {
	var probe pageToolCatalogProbe
	result, exception, err := runtime.Evaluate(pageToolCatalogProbeExpression).
		WithReturnByValue(true).
		WithAwaitPromise(true).
		Do(ctx)
	if err != nil {
		return probe, err
	}
	if exception != nil {
		return probe, errors.New("page WebMCP catalog probe raised an exception")
	}
	if result == nil || len(result.Value) == 0 {
		return probe, errors.New("page WebMCP catalog probe returned no value")
	}
	if err := json.Unmarshal(result.Value, &probe); err != nil {
		return probe, errors.New("page WebMCP catalog probe returned invalid data")
	}
	probe = normalizePageToolCatalogProbe(probe)
	return probe, nil
}

func normalizePageToolCatalogProbe(probe pageToolCatalogProbe) pageToolCatalogProbe {
	probe.DocumentReadyState = strings.ToLower(strings.TrimSpace(probe.DocumentReadyState))
	switch probe.DocumentReadyState {
	case webmcp.DocumentReadyStateLoading:
		if probe.ToolCount == 0 {
			probe.CatalogReady = false
		}
		probe.DocumentLoading = true
		probe.DocumentLoadingKnown = true
	case webmcp.DocumentReadyStateInteractive, webmcp.DocumentReadyStateComplete:
		probe.DocumentLoading = false
		probe.DocumentLoadingKnown = true
	default:
		probe.DocumentReadyState = webmcp.DocumentReadyStateUnknown
		probe.DocumentLoading = false
		probe.DocumentLoadingKnown = false
	}
	return probe
}
