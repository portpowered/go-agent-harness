(() => {
  "use strict";

  const ALLOWED_HOSTS = new Set(["reddit.com", "www.reddit.com", "old.reddit.com"]);
  if (location.protocol !== "https:" || !ALLOWED_HOSTS.has(location.hostname.toLowerCase())) return;
  const VERSION = "1.0.0";
  const INSTALL_KEY = "__yuiRedditWebMCPAdapterV1";
  const MAX_QUERY = 200;
  const MAX_RESULTS = 10;
  if (globalThis[INSTALL_KEY]) return;

  const state = { registered: false, generation: 0, query: "", results: new Map(), controller: new AbortController() };
  Object.defineProperty(globalThis, INSTALL_KEY, { value: state, configurable: false });
  const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const clean = (value, maximum = 500) => String(value || "").replace(/\s+/g, " ").trim().slice(0, maximum);
  const success = (data = {}) => ({ ok: true, adapter_version: VERSION, route: location.pathname, data });
  const failure = (code, message, details = {}) => ({ ok: false, adapter_version: VERSION, route: location.pathname, error: { code, message, details } });
  const deepAll = (selector) => {
    const result = [];
    const visit = (root) => {
      result.push(...root.querySelectorAll(selector));
      for (const element of root.querySelectorAll("*")) if (element.shadowRoot) visit(element.shadowRoot);
    };
    visit(document);
    return result;
  };
  const postIdentity = (raw) => {
    try {
      const parsed = new URL(raw, location.href);
      if (!ALLOWED_HOSTS.has(parsed.hostname.toLowerCase())) return null;
      const match = parsed.pathname.match(/\/comments\/([A-Za-z0-9]+)(?:\/|$)/);
      if (!match) return null;
      const subreddit = parsed.pathname.match(/^\/r\/([^/]+)\//i)?.[1] || "";
      return { id: match[1], subreddit: subreddit ? `r/${subreddit}` : "", url: `${location.origin}${parsed.pathname}` };
    } catch (_) { return null; }
  };
  const collectPosts = () => {
    const found = new Map();
    for (const anchor of deepAll("a[href*='/comments/']")) {
      const identity = postIdentity(anchor.href);
      if (!identity || found.has(identity.id)) continue;
      const card = anchor.closest("shreddit-post, [data-testid='post-container'], .search-result, article") || anchor.parentElement;
      const cardText = clean(card?.textContent, 1200);
      if (card?.matches("[promoted]") || card?.querySelector("shreddit-ad-post, [data-testid='promoted-post']") || /\bpromoted\b/i.test(cardText)) continue;
      const title = clean(anchor.getAttribute("slot") === "title" ? anchor.textContent : card?.querySelector("[slot='title'], h1, h2, h3")?.textContent || anchor.textContent, 400);
      if (!title) continue;
      const subreddit = clean(card?.getAttribute("subreddit-prefixed-name") || cardText.match(/r\/[A-Za-z0-9_]+/)?.[0] || identity.subreddit, 100);
      found.set(identity.id, { post_id: identity.id, title, subreddit, url: identity.url, summary: cardText });
      if (found.size >= MAX_RESULTS) break;
    }
    return found;
  };
  const pageBlocker = () => {
    const text = clean(document.body?.innerText, 3000).toLowerCase();
    if (text.includes("you've been blocked") || text.includes("whoa there")) return "blocked";
    if (text.includes("log in") && text.includes("continue")) return "signin_required";
    return "site_changed";
  };
  const register = async () => {
    const modelContext = document.modelContext || navigator.modelContext;
    if (!modelContext || typeof modelContext.registerTool !== "function") return false;
    const emptySchema = { type: "object", properties: {}, additionalProperties: false };
    const tools = [
      {
        name: "reddit_get_context", title: "Get Reddit context", description: "Read the Reddit route and supported controls.", inputSchema: emptySchema,
        annotations: { readOnly: true, untrustedContent: true },
        execute: async () => success({ origin: location.origin, path: location.pathname, ready: state.registered, query: state.query, search_generation: state.generation, result_count: state.results.size, capabilities: ["search", "list_posts", "open_post", "read_post"] })
      },
      {
        name: "reddit_search", title: "Search Reddit", description: "Open Reddit post-search results. Refresh the catalog and call reddit_list_posts after navigation.",
        inputSchema: { type: "object", properties: { query: { type: "string", minLength: 1, maxLength: MAX_QUERY }, sort: { type: "string", enum: ["relevance", "hot", "top", "new", "comments"], default: "relevance" } }, required: ["query"], additionalProperties: false },
        annotations: { readOnly: false, untrustedContent: true },
        execute: async (input) => {
          const query = clean(input?.query, MAX_QUERY);
          const sort = ["relevance", "hot", "top", "new", "comments"].includes(input?.sort) ? input.sort : "relevance";
          if (!query) return failure("invalid_input", "A non-empty Reddit search query is required.");
          const target = `${location.origin}/search/?q=${encodeURIComponent(query)}&type=link&sort=${encodeURIComponent(sort)}`;
          setTimeout(() => location.assign(target), 0);
          return success({ query, sort, navigation_started: true, next_step: "Refresh the catalog and call reddit_list_posts." });
        }
      },
      {
        name: "reddit_list_posts", title: "List Reddit posts", description: "Return bounded, non-promoted Reddit posts from the current search page.",
        inputSchema: { type: "object", properties: { limit: { type: "integer", minimum: 1, maximum: MAX_RESULTS, default: MAX_RESULTS } }, additionalProperties: false },
        annotations: { readOnly: true, untrustedContent: true },
        execute: async (input) => {
          const limit = input && input.limit === undefined ? MAX_RESULTS : Number(input?.limit);
          if (!Number.isInteger(limit) || limit < 1 || limit > MAX_RESULTS) return failure("invalid_input", "limit must be an integer from 1 through 10.");
          let found = collectPosts();
          for (let attempt = 0; attempt < 80 && !found.size; attempt += 1) { await delay(100); found = collectPosts(); }
          if (!found.size) return failure(pageBlocker(), "No ordinary Reddit post results became available.");
          state.generation += 1;
          state.query = clean(new URL(location.href).searchParams.get("q"), MAX_QUERY);
          state.results = found;
          return success({ query: state.query, search_generation: state.generation, posts: Array.from(found.values()).slice(0, limit) });
        }
      },
      {
        name: "reddit_open_post", title: "Open a Reddit post", description: "Open one post from the latest structured Reddit results.",
        inputSchema: { type: "object", properties: { post_id: { type: "string", pattern: "^[A-Za-z0-9]+$" }, search_generation: { type: "integer", minimum: 1 } }, required: ["post_id", "search_generation"], additionalProperties: false },
        annotations: { readOnly: false, untrustedContent: true },
        execute: async (input) => {
          const id = clean(input?.post_id, 30);
          if (input?.search_generation !== state.generation || !state.results.has(id)) return failure("stale_result", "The post must come from the latest structured search results.");
          const result = state.results.get(id);
          setTimeout(() => location.assign(result.url), 0);
          return success({ post_id: id, title: result.title, url: result.url, navigation_started: true, next_step: "Refresh the catalog and call reddit_read_post." });
        }
      },
      {
        name: "reddit_read_post", title: "Read Reddit post", description: "Read the title and bounded body text of the current Reddit post.", inputSchema: emptySchema,
        annotations: { readOnly: true, untrustedContent: true },
        execute: async () => {
          const identity = postIdentity(location.href);
          if (!identity) return failure("unsupported_route", "Open an ordinary Reddit post first.");
          const post = deepAll("shreddit-post, [data-testid='post-container'], article")[0] || document.querySelector("main");
          const title = clean(post?.querySelector?.("[slot='title'], h1")?.textContent || document.querySelector("h1")?.textContent, 500);
          const body = clean(post?.querySelector?.("[slot='text-body'], [data-post-click-location='text-body'], .md")?.textContent, 2500);
          return title ? success({ post: { post_id: identity.id, title, body, url: location.href } }) : failure("post_not_ready", "The Reddit post is not ready.");
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
