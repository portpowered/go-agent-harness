(() => {
  "use strict";

  const ALLOWED_HOSTS = new Set(["x.com", "www.x.com", "twitter.com", "www.twitter.com"]);
  if (location.protocol !== "https:" || !ALLOWED_HOSTS.has(location.hostname.toLowerCase())) return;
  const VERSION = "1.0.0";
  const INSTALL_KEY = "__yuiXWebMCPAdapterV1";
  const MAX_POST_LENGTH = 280;
  if (globalThis[INSTALL_KEY]) return;

  const state = {
    registered: false,
    generation: 0,
    draftToken: "",
    draftText: "",
    consumedTokens: new Set(),
    controller: new AbortController()
  };
  Object.defineProperty(globalThis, INSTALL_KEY, { value: state, configurable: false });

  const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const clean = (value, maximum = 500) => String(value ?? "").replace(/\r\n?/g, "\n").trim().slice(0, maximum);
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
  const firstEnabled = (selector) => deepAll(selector).find((element) => !element.disabled && element.getAttribute("aria-disabled") !== "true") || null;
  const composer = () => firstEnabled('[data-testid="tweetTextarea_0"][contenteditable="true"], [role="textbox"][contenteditable="true"][data-testid*="tweetTextarea"]');
  const composerText = (element = composer()) => clean(element?.innerText || element?.textContent, MAX_POST_LENGTH + 1);
  const accountHandle = () => {
    const profile = firstEnabled('[data-testid="AppTabBar_Profile_Link"], a[aria-label="Profile"], [data-testid="SideNav_AccountSwitcher_Button"] a[href^="/"]');
    const href = profile?.getAttribute("href") || "";
    const match = href.match(/^\/([A-Za-z0-9_]{1,15})(?:\/|$)/);
    return match ? `@${match[1]}` : null;
  };
  const isSignedIn = () => Boolean(
    firstEnabled('[data-testid="SideNav_NewTweet_Button"], a[href="/compose/post"], [data-testid="SideNav_AccountSwitcher_Button"]') || composer()
  );
  const setComposerText = (element, text) => {
    element.focus();
    // X's controlled contenteditable applies execCommand asynchronously and
    // can report false even when it succeeds. A second DOM fallback races its
    // state update and duplicates text, so use one native editing sequence.
    document.execCommand("selectAll", false, null);
    document.execCommand("delete", false, null);
    if (text) document.execCommand("insertText", false, text);
  };
  const waitForComposer = async () => {
    for (let attempt = 0; attempt < 80; attempt += 1) {
      const found = composer();
      if (found) return found;
      await delay(100);
    }
    return null;
  };
  const openComposer = async () => {
    const existing = composer();
    if (existing) return existing;
    const trigger = firstEnabled('[data-testid="SideNav_NewTweet_Button"], a[href="/compose/post"], a[href="/compose/tweet"]');
    if (!trigger) return null;
    trigger.click();
    return waitForComposer();
  };
  const draftToken = () => {
    const random = globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random().toString(36).slice(2)}`;
    return `x-draft-${state.generation}-${random}`;
  };
  const clearPreparedState = () => {
    state.draftToken = "";
    state.draftText = "";
  };

  const register = async () => {
    const modelContext = document.modelContext || navigator.modelContext;
    if (!modelContext || typeof modelContext.registerTool !== "function") return false;
    const emptySchema = { type: "object", properties: {}, additionalProperties: false };
    const tools = [
      {
        name: "x_get_context", title: "Get X context", description: "Read sign-in, account, route, and prepared-draft state without changing X.", inputSchema: emptySchema,
        annotations: { readOnly: true, untrustedContent: true },
        execute: async () => success({ origin: location.origin, path: location.pathname, ready: state.registered, signed_in: isSignedIn(), account_handle: accountHandle(), draft_generation: state.generation, draft_prepared: Boolean(state.draftToken), capabilities: ["prepare_post", "publish_post", "clear_draft"] })
      },
      {
        name: "x_prepare_post", title: "Prepare an X post", description: "Put exact text into the signed-in X composer without publishing it. Returns a one-use draft token required by x_publish_post.",
        inputSchema: { type: "object", properties: { text: { type: "string", minLength: 1, maxLength: MAX_POST_LENGTH } }, required: ["text"], additionalProperties: false },
        annotations: { readOnly: false, untrustedContent: false },
        execute: async (input) => {
          const text = clean(input?.text, MAX_POST_LENGTH + 1);
          if (!text || text.length > MAX_POST_LENGTH) return failure("invalid_input", "Post text must contain 1 through 280 characters after trimming.");
          if (!isSignedIn()) return failure("signin_required", "Sign in to X in this browser profile before preparing a post.");
          const target = await openComposer();
          if (!target) return failure("composer_not_ready", "The X post composer did not become available.");
          setComposerText(target, text);
          await delay(100);
          const observed = composerText(target);
          if (observed !== text) return failure("site_changed", "X did not retain the exact requested draft text.", { requested_text: text, observed_text: observed });
          state.generation += 1;
          state.draftText = text;
          state.draftToken = draftToken();
          return success({ draft_generation: state.generation, draft_token: state.draftToken, text, character_count: text.length, published: false, next_step: "Review the exact text, then call x_publish_post with this draft_token, the same text, and confirm=true." });
        }
      },
      {
        name: "x_publish_post", title: "Publish an X post", description: "Publish the exact prepared X draft once. This creates an externally visible post and requires its one-use token, identical text, and confirm=true.",
        inputSchema: { type: "object", properties: { draft_token: { type: "string", minLength: 1, maxLength: 160 }, text: { type: "string", minLength: 1, maxLength: MAX_POST_LENGTH }, confirm: { type: "boolean", const: true } }, required: ["draft_token", "text", "confirm"], additionalProperties: false },
        annotations: { readOnly: false, untrustedContent: false },
        execute: async (input) => {
          const token = clean(input?.draft_token, 160);
          const text = clean(input?.text, MAX_POST_LENGTH + 1);
          if (state.consumedTokens.has(token)) return failure("already_published", "This one-use draft token was already submitted and cannot be retried.");
          if (!input?.confirm) return failure("confirmation_required", "Set confirm=true only after reviewing the exact prepared text.");
          if (!token || token !== state.draftToken || !state.draftText) return failure("stale_draft", "Prepare the post again and use the latest one-use draft token.");
          if (text !== state.draftText) return failure("text_mismatch", "The publish text must exactly match the prepared draft.", { prepared_text: state.draftText, publish_text: text });
          const target = composer();
          if (!target || composerText(target) !== state.draftText) return failure("draft_changed", "The visible X composer no longer exactly matches the prepared draft.");
          const button = firstEnabled('[data-testid="tweetButtonInline"], [data-testid="tweetButton"]');
          if (!button) return failure("publish_not_ready", "The enabled X Post button is not available.");

          // Consume before clicking. If the site accepts the click but its UI
          // never confirms, retrying the same token must not create a duplicate.
          state.consumedTokens.add(token);
          if (state.consumedTokens.size > 20) state.consumedTokens.delete(state.consumedTokens.values().next().value);
          button.click();
          for (let attempt = 0; attempt < 100; attempt += 1) {
            await delay(100);
            const current = composer();
            if (!current || composerText(current) === "") {
              clearPreparedState();
              return success({ published: true, text, account_handle: accountHandle(), verification: current ? "composer_cleared" : "composer_closed", duplicate_retry_blocked: true });
            }
          }
          clearPreparedState();
          return failure("publish_status_unknown", "The Post control was activated, but X did not expose a completion signal. Do not retry automatically; inspect the account first.", { text, duplicate_retry_blocked: true });
        }
      },
      {
        name: "x_clear_draft", title: "Clear prepared X draft", description: "Clear the exact currently prepared draft without publishing it.",
        inputSchema: { type: "object", properties: { draft_token: { type: "string", minLength: 1, maxLength: 160 } }, required: ["draft_token"], additionalProperties: false },
        annotations: { readOnly: false, untrustedContent: false },
        execute: async (input) => {
          const token = clean(input?.draft_token, 160);
          if (!token || token !== state.draftToken || !state.draftText) return failure("stale_draft", "Only the latest prepared draft can be cleared.");
          const target = composer();
          if (target && composerText(target) === state.draftText) setComposerText(target, "");
          clearPreparedState();
          return success({ cleared: true, published: false });
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
