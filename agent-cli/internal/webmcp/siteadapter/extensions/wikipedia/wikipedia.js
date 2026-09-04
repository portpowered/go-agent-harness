(() => {
  "use strict";

  const host = location.hostname.toLowerCase();
  if (location.protocol !== "https:" || !(host === "wikipedia.org" || host.endsWith(".wikipedia.org"))) return;

  const VERSION = "1.0.0";
  const INSTALL_KEY = "__yuiWikipediaWebMCPAdapterV1";
  const MAX_QUERY = 200;
  const MAX_RESULTS = 10;
  if (globalThis[INSTALL_KEY]) return;

  const state = { registered: false, generation: 0, query: "", results: new Map(), controller: new AbortController() };
  Object.defineProperty(globalThis, INSTALL_KEY, { value: state, configurable: false });
  const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const clean = (value, maximum = 400) => String(value || "").replace(/\s+/g, " ").trim().slice(0, maximum);
  const success = (data = {}) => ({ ok: true, adapter_version: VERSION, route: location.pathname, data });
  const failure = (code, message, details = {}) => ({ ok: false, adapter_version: VERSION, route: location.pathname, error: { code, message, details } });
  const safeArticle = (raw) => {
    try {
      const parsed = new URL(raw, location.href);
      if (parsed.origin !== location.origin || !parsed.pathname.startsWith("/wiki/")) return null;
      const title = decodeURIComponent(parsed.pathname.slice(6));
      if (!title || title.includes(":")) return null;
      return parsed;
    } catch (_) { return null; }
  };
  const collectResults = () => {
    const found = new Map();
    const selectors = [".mw-search-result-heading a", ".mw-search-result a", "main a[href^='/wiki/']"];
    for (const selector of selectors) {
      for (const anchor of document.querySelectorAll(selector)) {
        const parsed = safeArticle(anchor.href);
        if (!parsed) continue;
        const key = parsed.pathname;
        if (found.has(key)) continue;
        const item = anchor.closest(".mw-search-result") || anchor.parentElement;
        found.set(key, {
          page_key: key,
          title: clean(anchor.textContent || anchor.getAttribute("title"), 300),
          snippet: clean(item && item.querySelector(".searchresult")?.textContent, 600),
          url: parsed.href
        });
        if (found.size >= MAX_RESULTS) return found;
      }
      if (found.size) return found;
    }
    return found;
  };
  const articleData = () => ({
    title: clean(document.querySelector("h1#firstHeading, h1.firstHeading, h1")?.textContent, 300),
    url: location.href,
    intro: clean(Array.from(document.querySelectorAll(".mw-parser-output p, #mw-content-text p, main p")).map((node) => node.textContent).find((text) => clean(text).length > 40), 1800),
    headings: Array.from(document.querySelectorAll(".mw-parser-output h2, .mw-parser-output h3")).slice(0, 20).map((node) => clean(node.textContent, 200)).filter(Boolean)
  });
  const register = async () => {
    const modelContext = document.modelContext || navigator.modelContext;
    if (!modelContext || typeof modelContext.registerTool !== "function") return false;
    const emptySchema = { type: "object", properties: {}, additionalProperties: false };
    const tools = [
      {
        name: "wikipedia_get_context",
        title: "Get Wikipedia context",
        description: "Read the selected Wikipedia page and adapter capabilities.",
        inputSchema: emptySchema,
        annotations: { readOnly: true, untrustedContent: true },
        execute: async () => success({ origin: location.origin, path: location.pathname, ready: state.registered, query: state.query, search_generation: state.generation, result_count: state.results.size, capabilities: ["search", "list_results", "open_result", "read_article"] })
      },
      {
        name: "wikipedia_search",
        title: "Search Wikipedia",
        description: "Open Wikipedia's search-results page for a bounded topic query. Refresh the catalog and call wikipedia_list_results after navigation.",
        inputSchema: { type: "object", properties: { query: { type: "string", minLength: 1, maxLength: MAX_QUERY } }, required: ["query"], additionalProperties: false },
        annotations: { readOnly: false, untrustedContent: true },
        execute: async (input) => {
          const query = clean(input && input.query, MAX_QUERY);
          if (!query) return failure("invalid_input", "A non-empty search query is required.");
          const target = `${location.origin}/w/index.php?title=Special%3ASearch&fulltext=1&ns0=1&search=${encodeURIComponent(query)}`;
          setTimeout(() => location.assign(target), 0);
          return success({ query, navigation_started: true, next_step: "Refresh the catalog and call wikipedia_list_results." });
        }
      },
      {
        name: "wikipedia_list_results",
        title: "List Wikipedia results",
        description: "Return a bounded list of ordinary Wikipedia article results from the current search page.",
        inputSchema: { type: "object", properties: { limit: { type: "integer", minimum: 1, maximum: MAX_RESULTS, default: MAX_RESULTS } }, additionalProperties: false },
        annotations: { readOnly: true, untrustedContent: true },
        execute: async (input) => {
          const limit = input && input.limit === undefined ? MAX_RESULTS : Number(input && input.limit);
          if (!Number.isInteger(limit) || limit < 1 || limit > MAX_RESULTS) return failure("invalid_input", "limit must be an integer from 1 through 10.");
          if (!(location.pathname === "/w/index.php" || location.pathname.includes("Special:Search"))) return failure("unsupported_route", "Wikipedia results are available only on a search-results page.");
          const found = collectResults();
          if (!found.size) return failure("results_not_ready", "No ordinary Wikipedia article results are visible.");
          state.generation += 1;
          state.query = clean(new URL(location.href).searchParams.get("search"), MAX_QUERY);
          state.results = found;
          return success({ query: state.query, search_generation: state.generation, results: Array.from(found.values()).slice(0, limit) });
        }
      },
      {
        name: "wikipedia_open_result",
        title: "Open a Wikipedia result",
        description: "Open one article from the latest structured Wikipedia result list.",
        inputSchema: { type: "object", properties: { page_key: { type: "string", minLength: 7, maxLength: 500 }, search_generation: { type: "integer", minimum: 1 } }, required: ["page_key", "search_generation"], additionalProperties: false },
        annotations: { readOnly: false, untrustedContent: true },
        execute: async (input) => {
          const key = clean(input && input.page_key, 500);
          if (input?.search_generation !== state.generation || !state.results.has(key)) return failure("stale_result", "The article must come from the latest structured search results.");
          const result = state.results.get(key);
          setTimeout(() => location.assign(result.url), 0);
          return success({ title: result.title, url: result.url, navigation_started: true, next_step: "Refresh the catalog and call wikipedia_read_article." });
        }
      },
      {
        name: "wikipedia_read_article",
        title: "Read Wikipedia article",
        description: "Read the title, introductory text, and headings from the current ordinary Wikipedia article.",
        inputSchema: emptySchema,
        annotations: { readOnly: true, untrustedContent: true },
        execute: async () => {
          if (!safeArticle(location.href)) return failure("unsupported_route", "Open an ordinary Wikipedia article first.");
          const article = articleData();
          return article.title ? success({ article }) : failure("article_not_ready", "The Wikipedia article is not ready.");
        }
      }
    ];
    await Promise.all(tools.map((tool) => modelContext.registerTool(tool, { signal: state.controller.signal })));
    state.registered = true;
    return true;
  };
  (async () => {
    for (let attempt = 0; attempt < 100 && !state.controller.signal.aborted; attempt += 1) {
      try { if (await register()) return; } catch (error) { state.error = clean(error?.message || error, 1000); return; }
      await delay(50);
    }
  })();
})();
