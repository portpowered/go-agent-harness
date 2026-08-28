package chrome

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/chromedp/cdproto/runtime"
)

// pageToolCatalogProbeExpression intentionally reads only the page's public
// WebMCP producer. It does not retain page data or execute a page tool.
const pageToolCatalogProbeExpression = `(() => {
  const context = (typeof document !== "undefined" && document.modelContext) ||
    (typeof navigator !== "undefined" && navigator.modelContext);
  if (!context || typeof context.getTools !== "function") {
    return {producer_present: false, catalog_ready: false, tool_count: 0};
  }
  try {
    const tools = context.getTools();
    if (!Array.isArray(tools)) {
      return {producer_present: true, catalog_ready: false, tool_count: 0};
    }
    return {producer_present: true, catalog_ready: true, tool_count: tools.length};
  } catch (_) {
    return {producer_present: true, catalog_ready: false, tool_count: 0};
  }
})()`

type pageToolCatalogProbe struct {
	ProducerPresent bool `json:"producer_present"`
	CatalogReady    bool `json:"catalog_ready"`
	ToolCount       int  `json:"tool_count"`
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
	return probe, nil
}
