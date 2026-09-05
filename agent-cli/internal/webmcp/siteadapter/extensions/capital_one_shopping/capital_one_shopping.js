(() => {
  "use strict";

  const ALLOWED_HOSTS = new Set(["capitaloneshopping.com", "www.capitaloneshopping.com"]);
  if (location.protocol !== "https:" || !ALLOWED_HOSTS.has(location.hostname.toLowerCase())) return;
  const VERSION = "1.0.0";
  const INSTALL_KEY = "__yuiCapitalOneShoppingWebMCPAdapterV1";
  const MAX_PAGES = 20;
  const MAX_RESULTS = 50;
  const MAX_STORED_OFFERS = 1000;
  if (globalThis[INSTALL_KEY]) return;

  const state = {
    registered: false,
    generation: 0,
    scanning: false,
    offers: new Map(),
    matches: [],
    unknownCost: [],
    summary: null,
    controller: new AbortController()
  };
  Object.defineProperty(globalThis, INSTALL_KEY, { value: state, configurable: false });

  const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const clean = (value, maximum = 500) => String(value || "").replace(/\s+/g, " ").trim().slice(0, maximum);
  const number = (value) => Number(String(value || "").replace(/[$,%\s,]/g, ""));
  const success = (data = {}) => ({ ok: true, adapter_version: VERSION, route: location.pathname, data });
  const failure = (code, message, details = {}) => ({ ok: false, adapter_version: VERSION, route: location.pathname, error: { code, message, details } });
  const money = (text) => {
    const match = clean(text, 200).match(/\$\s*([0-9][0-9,]*(?:\.\d{1,2})?)/);
    return match ? number(match[1]) : null;
  };
  const hash = (value) => {
    let result = 2166136261;
    for (let index = 0; index < value.length; index += 1) {
      result ^= value.charCodeAt(index);
      result = Math.imul(result, 16777619);
    }
    return `cos-${(result >>> 0).toString(36)}`;
  };
  const deepAll = (selector) => {
    const result = [];
    const visit = (root) => {
      result.push(...root.querySelectorAll(selector));
      for (const element of root.querySelectorAll("*")) if (element.shadowRoot) visit(element.shadowRoot);
    };
    visit(document);
    return result;
  };
  const rewardFromText = (text) => {
    const normalized = clean(text, 2000);
    const percentMatch = normalized.match(/(?:earn|now|up to|\+)?\s*([0-9]+(?:\.[0-9]+)?)\s*%\s*back\b/i);
    const bonusMatch = normalized.match(/(?:earn|now|up to|get|\+)?\s*\$\s*([0-9][0-9,]*(?:\.\d{1,2})?)\s*back\b/i);
    const capMatch = normalized.match(/\(\s*\$\s*([0-9][0-9,]*(?:\.\d{1,2})?)\s*max\s*\)/i);
    const spendMatch = normalized.match(/\bwhen\s+you\s+spend\s+\$\s*([0-9][0-9,]*(?:\.\d{1,2})?)/i);
    return {
      cashback_percent: percentMatch ? number(percentMatch[1]) : null,
      bonus_usd: bonusMatch ? number(bonusMatch[1]) : null,
      reward_cap_usd: capMatch ? number(capMatch[1]) : null,
      qualifying_spend_usd: spendMatch ? number(spendMatch[1]) : null
    };
  };
  const explicitCost = (card) => {
    const priceElements = card.querySelectorAll("[data-price], [itemprop='price'], [class*='product-price'], [class*='sale-price']");
    for (const element of priceElements) {
      const raw = element.getAttribute("content") || element.getAttribute("data-price") || element.textContent;
      const parsed = money(raw);
      if (Number.isFinite(parsed)) return parsed;
    }
    return null;
  };
  const merchantFromCard = (card, text) => {
    const label = clean(card.getAttribute("aria-label"), 500);
    const labelMatch = label.match(/(?:view|activate)\s+(.+?)\s+offer\b/i);
    if (labelMatch) return clean(labelMatch[1], 160);
    const imageAlt = clean(card.querySelector("img[alt]")?.getAttribute("alt"), 300);
    const imageMatch = imageAlt.match(/merchant image for\s+(.+)/i);
    if (imageMatch) return clean(imageMatch[1], 160);
    const paragraphs = Array.from(card.querySelectorAll("p")).map((element) => clean(element.textContent, 300)).filter(Boolean);
    const rewardIndex = paragraphs.findIndex((value) => /(?:%|\$)\s*(?:back|[^ ]*\s+back)|\bback\b/i.test(value));
    if (rewardIndex > 0) {
      for (let index = rewardIndex - 1; index >= 0; index -= 1) {
        const candidate = paragraphs[index];
        if (/^(limited time event|home page activation bonus|today'?s top offer|exclusive offer)$/i.test(candidate)) continue;
        if (candidate.length > 100 || /(?:[0-9]+(?:\.[0-9]+)?\s*%|\$\s*[0-9][0-9,.]*)\s*off\b|^save\b/i.test(candidate)) continue;
        return candidate;
      }
    }
    const atMatch = text.match(/\bat\s+(.+?)(?:\s+(?:get|save|up to|exclusive|limited)\b|$)/i);
    return atMatch ? clean(atMatch[1], 160) : "Unknown merchant";
  };
  const offerURL = (card) => {
    const anchor = card.matches("a[href]") ? card : card.querySelector("a[href]");
    if (!anchor) return location.href;
    try {
      const parsed = new URL(anchor.href, location.href);
      return ALLOWED_HOSTS.has(parsed.hostname.toLowerCase()) ? parsed.href.slice(0, 1000) : location.href;
    } catch (_) {
      return location.href;
    }
  };
  const parseCard = (card) => {
    const text = clean(card.innerText || card.textContent, 2000);
    const reward = rewardFromText(text);
    if (reward.cashback_percent === null && reward.bonus_usd === null) return null;
    const merchant = merchantFromCard(card, text);
    const description = clean(Array.from(card.querySelectorAll("p")).map((element) => clean(element.textContent, 500)).filter(Boolean).join(" | ") || text, 1200);
    const identity = [merchant.toLowerCase(), reward.cashback_percent, reward.bonus_usd, reward.reward_cap_usd, reward.qualifying_spend_usd, description.toLowerCase()].join("|");
    return {
      offer_id: hash(identity),
      merchant,
      description,
      cashback_percent: reward.cashback_percent,
      bonus_usd: reward.bonus_usd,
      reward_cap_usd: reward.reward_cap_usd,
      qualifying_spend_usd: reward.qualifying_spend_usd,
      cost_usd: explicitCost(card),
      url: offerURL(card)
    };
  };
  const collectOffers = () => {
    const found = new Map();
    for (const card of deepAll("button, article, a[href], [role='button']")) {
      const offer = parseCard(card);
      if (!offer || found.has(offer.offer_id)) continue;
      // Prefer the smallest semantic card when a containing article and its
      // actionable child both describe the same offer.
      found.set(offer.offer_id, offer);
      if (found.size >= MAX_STORED_OFFERS) break;
    }
    return found;
  };
  const validateScanInput = (input) => {
    const maxPages = Number(input?.max_pages);
    if (!Number.isInteger(maxPages) || maxPages < 1 || maxPages > MAX_PAGES) return { error: "max_pages must be an integer from 1 through 20." };
    const optionalNumber = (name, maximum) => {
      if (input?.[name] === undefined || input?.[name] === null) return null;
      const value = Number(input[name]);
      if (!Number.isFinite(value) || value < 0 || (maximum !== undefined && value > maximum)) return NaN;
      return value;
    };
    const maxCost = optionalNumber("max_cost_usd");
    const minimumPercent = optionalNumber("min_cashback_percent", 100);
    const minimumBonus = optionalNumber("min_bonus_usd");
    if (Number.isNaN(maxCost)) return { error: "max_cost_usd must be a non-negative number." };
    if (Number.isNaN(minimumPercent)) return { error: "min_cashback_percent must be between 0 and 100." };
    if (Number.isNaN(minimumBonus)) return { error: "min_bonus_usd must be a non-negative number." };
    const rewardMatch = input?.reward_match === undefined ? "any" : input.reward_match;
    if (!['any', 'all'].includes(rewardMatch)) return { error: "reward_match must be any or all." };
    const unknownCostPolicy = input?.unknown_cost_policy === undefined ? "separate" : input.unknown_cost_policy;
    if (!['separate', 'exclude'].includes(unknownCostPolicy)) return { error: "unknown_cost_policy must be separate or exclude." };
    return { maxPages, maxCost, minimumPercent, minimumBonus, rewardMatch, unknownCostPolicy };
  };
  const rewardMatches = (offer, config) => {
    const checks = [];
    if (config.minimumPercent !== null) checks.push(offer.cashback_percent !== null && offer.cashback_percent >= config.minimumPercent);
    if (config.minimumBonus !== null) checks.push(offer.bonus_usd !== null && offer.bonus_usd >= config.minimumBonus);
    if (!checks.length) return true;
    return config.rewardMatch === "all" ? checks.every(Boolean) : checks.some(Boolean);
  };
  const waitForGrowth = async (beforeHeight, beforeCount) => {
    const deadline = Date.now() + 4000;
    while (Date.now() < deadline) {
      await delay(100);
      const height = Math.max(document.body?.scrollHeight || 0, document.documentElement?.scrollHeight || 0);
      const count = collectOffers().size;
      if (height > beforeHeight || count > beforeCount) return true;
    }
    return false;
  };
  const scan = async (config) => {
    state.scanning = true;
    state.generation += 1;
    state.offers = new Map();
    state.matches = [];
    state.unknownCost = [];
    let pagesScanned = 0;
    let loadCycles = 0;
    let noGrowthCycles = 0;
    let duplicateCount = 0;
    let stopReason = "max_pages";
    try {
      while (pagesScanned < config.maxPages) {
        const visible = collectOffers();
        for (const [id, offer] of visible) {
          if (state.offers.has(id)) duplicateCount += 1;
          else if (state.offers.size < MAX_STORED_OFFERS) state.offers.set(id, offer);
        }
        pagesScanned += 1;
        if (pagesScanned >= config.maxPages) break;
        const beforeHeight = Math.max(document.body?.scrollHeight || 0, document.documentElement?.scrollHeight || 0);
        const beforeCount = visible.size;
        window.scrollTo({ top: beforeHeight, behavior: "instant" });
        loadCycles += 1;
        const grew = await waitForGrowth(beforeHeight, beforeCount);
        const after = collectOffers();
        const newOffers = Array.from(after.keys()).filter((id) => !state.offers.has(id)).length;
        if (!grew && newOffers === 0) noGrowthCycles += 1;
        else noGrowthCycles = 0;
        if (noGrowthCycles >= 2) {
          stopReason = "no_growth";
          break;
        }
      }
      for (const offer of state.offers.values()) {
        if (!rewardMatches(offer, config)) continue;
        if (config.maxCost !== null && offer.cost_usd === null) {
          if (config.unknownCostPolicy === "separate") state.unknownCost.push(offer);
          continue;
        }
        if (config.maxCost !== null && offer.cost_usd > config.maxCost) continue;
        state.matches.push(offer);
      }
      state.matches.sort((left, right) => (right.cashback_percent || 0) - (left.cashback_percent || 0) || (right.bonus_usd || 0) - (left.bonus_usd || 0) || left.merchant.localeCompare(right.merchant));
      state.unknownCost.sort((left, right) => left.merchant.localeCompare(right.merchant));
      state.summary = {
        scan_generation: state.generation,
        pages_requested: config.maxPages,
        pages_scanned: pagesScanned,
        load_cycles: loadCycles,
        stop_reason: stopReason,
        offers_observed: state.offers.size,
        duplicate_observations: duplicateCount,
        match_count: state.matches.length,
        unknown_cost_match_count: state.unknownCost.length,
        result_limit: MAX_RESULTS
      };
      return success({
        ...state.summary,
        matches: state.matches.slice(0, MAX_RESULTS),
        unknown_cost_matches: state.unknownCost.slice(0, MAX_RESULTS),
        next_step: state.matches.length > MAX_RESULTS || state.unknownCost.length > MAX_RESULTS ? "Call capital_one_shopping_list_matches for additional bounded results." : "Report the grounded matches and scan summary."
      });
    } finally {
      state.scanning = false;
    }
  };

  const register = async () => {
    const modelContext = document.modelContext || navigator.modelContext;
    if (!modelContext || typeof modelContext.registerTool !== "function") return false;
    const emptySchema = { type: "object", properties: {}, additionalProperties: false };
    const tools = [
      {
        name: "capital_one_shopping_get_context", title: "Get Capital One Shopping context", description: "Read the current Capital One Shopping route and scan state.", inputSchema: emptySchema,
        annotations: { readOnly: true, untrustedContent: true },
        execute: async () => success({ origin: location.origin, path: location.pathname, ready: state.registered, scanning: state.scanning, scan_generation: state.generation, summary: state.summary, capabilities: ["scan_offers", "list_matches", "reset_scan"] })
      },
      {
        name: "capital_one_shopping_scan_offers", title: "Scan Capital One Shopping offers", description: "Scan one through twenty lazy-loaded offer batches, deduplicate virtualized cards, and apply typed cost, cashback-percent, and fixed-bonus thresholds. This only reads and scrolls; it never activates an offer or makes a purchase.",
        inputSchema: {
          type: "object",
          properties: {
            max_pages: { type: "integer", minimum: 1, maximum: MAX_PAGES },
            max_cost_usd: { type: "number", minimum: 0 },
            min_cashback_percent: { type: "number", minimum: 0, maximum: 100 },
            min_bonus_usd: { type: "number", minimum: 0 },
            reward_match: { type: "string", enum: ["any", "all"], default: "any" },
            unknown_cost_policy: { type: "string", enum: ["separate", "exclude"], default: "separate" }
          },
          required: ["max_pages"],
          additionalProperties: false
        },
        annotations: { readOnly: true, untrustedContent: true },
        execute: async (input) => {
          if (state.scanning) return failure("scan_in_progress", "A Capital One Shopping scan is already running.");
          const config = validateScanInput(input);
          if (config.error) return failure("invalid_input", config.error);
          return scan(config);
        }
      },
      {
        name: "capital_one_shopping_list_matches", title: "List Capital One Shopping matches", description: "Read a bounded page of matches from the latest completed scan.",
        inputSchema: { type: "object", properties: { kind: { type: "string", enum: ["matched", "unknown_cost"], default: "matched" }, offset: { type: "integer", minimum: 0, default: 0 }, limit: { type: "integer", minimum: 1, maximum: MAX_RESULTS, default: MAX_RESULTS }, scan_generation: { type: "integer", minimum: 1 } }, required: ["scan_generation"], additionalProperties: false },
        annotations: { readOnly: true, untrustedContent: true },
        execute: async (input) => {
          if (!state.summary || input?.scan_generation !== state.generation) return failure("stale_result", "Use the scan generation returned by the latest completed scan.");
          const offset = input?.offset === undefined ? 0 : Number(input.offset);
          const limit = input?.limit === undefined ? MAX_RESULTS : Number(input.limit);
          const kind = input?.kind === undefined ? "matched" : input.kind;
          if (!Number.isInteger(offset) || offset < 0 || !Number.isInteger(limit) || limit < 1 || limit > MAX_RESULTS || !["matched", "unknown_cost"].includes(kind)) return failure("invalid_input", "kind, offset, or limit is invalid.");
          const source = kind === "unknown_cost" ? state.unknownCost : state.matches;
          return success({ scan_generation: state.generation, kind, offset, limit, total: source.length, offers: source.slice(offset, offset + limit) });
        }
      },
      {
        name: "capital_one_shopping_reset_scan", title: "Reset Capital One Shopping scan", description: "Clear locally collected scan results without changing the shopping account or activating any offer.", inputSchema: emptySchema,
        annotations: { readOnly: true, untrustedContent: true },
        execute: async () => {
          if (state.scanning) return failure("scan_in_progress", "The active scan must finish before reset.");
          state.generation += 1;
          state.offers = new Map(); state.matches = []; state.unknownCost = []; state.summary = null;
          return success({ scan_generation: state.generation, reset: true });
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
