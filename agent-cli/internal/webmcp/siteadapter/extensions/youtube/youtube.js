(() => {
  "use strict";

	const ALLOWED_PROTOCOL = "https:";
	const ALLOWED_HOSTS = new Set(["youtube.com", "www.youtube.com", "m.youtube.com"]);
	if (location.protocol !== ALLOWED_PROTOCOL || !ALLOWED_HOSTS.has(location.hostname)) return;

  const VERSION = "1.0.0";
  const INSTALL_KEY = "__yuiYouTubeWebMCPAdapterV1";
	const MAX_QUERY = 200;
	const MAX_RESULTS = 10;
	const WAIT_MS = 12000;
	const SEARCH_WAIT_MS = 25000;

  if (globalThis[INSTALL_KEY]) return;

  const state = {
    version: VERSION,
    registered: false,
    registrationError: "",
    searchGeneration: 0,
    query: "",
    results: new Map(),
    controller: new AbortController()
  };
  Object.defineProperty(globalThis, INSTALL_KEY, { value: state, configurable: false });

  const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const clean = (value, maximum = 300) => String(value || "").replace(/\s+/g, " ").trim().slice(0, maximum);
  const route = () => {
    if (location.pathname === "/results") return "results";
    if (location.pathname === "/watch") return "watch";
    if (location.pathname.startsWith("/shorts/")) return "shorts";
    if (location.pathname === "/" || location.pathname === "") return "home";
    return "other";
  };
  const success = (data = {}) => ({ ok: true, adapter_version: VERSION, route: route(), data });
  const failure = (code, message, details = {}) => ({
    ok: false,
    adapter_version: VERSION,
    route: route(),
    error: { code, message, details }
  });
  const waitFor = async (predicate, timeout = WAIT_MS) => {
    const deadline = performance.now() + timeout;
    while (performance.now() < deadline) {
      if (state.controller.signal.aborted) throw new DOMException("adapter canceled", "AbortError");
      const value = predicate();
      if (value) return value;
      await delay(100);
    }
    return null;
  };
  const videoIDFromURL = (raw) => {
    try {
      const parsed = new URL(raw, location.href);
      if (parsed.origin !== location.origin || parsed.pathname !== "/watch") return "";
      const id = parsed.searchParams.get("v") || "";
      return /^[A-Za-z0-9_-]{6,20}$/.test(id) ? id : "";
    } catch (_) {
      return "";
    }
  };
  const currentVideoID = () => videoIDFromURL(location.href);
  const isVisible = (element) => {
    if (!(element instanceof Element)) return false;
    const style = getComputedStyle(element);
    const rect = element.getBoundingClientRect();
    return style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0;
  };
	  const searchResultsRoot = () => document.querySelector("ytd-search, yt-search-page");
	  const collectResults = (root = document) => {
	  const selectors = [
		"ytd-video-renderer a#video-title[href*='/watch?v=']",
		"ytd-rich-item-renderer a#video-title-link[href*='/watch?v=']",
		"a#video-title[href*='/watch?v=']",
		"a[href*='/watch?v=']"
	  ];
    const found = new Map();
    for (const selector of selectors) {
		for (const anchor of root.querySelectorAll(selector)) {
		if (!isVisible(anchor) || anchor.closest("ytd-ad-slot-renderer, ytd-promoted-video-renderer, ytd-display-ad-renderer, ad-slot-renderer, ad-button-view-model, lockup-attachments-view-model")) continue;
		const id = videoIDFromURL(anchor.href);
		if (!id || found.has(id)) continue;
		const container = anchor.closest("ytd-video-renderer, ytd-rich-item-renderer") || anchor.parentElement;
		const titleAnchor = container && container.querySelector("a#video-title[href*='/watch?v='], a#video-title-link[href*='/watch?v='], a[aria-label][href*='/watch?v=']");
		const channel = container && container.querySelector("#channel-name a, ytd-channel-name a");
		const duration = container && container.querySelector("ytd-thumbnail-overlay-time-status-renderer #text, .ytd-thumbnail-overlay-time-status-renderer");
		found.set(id, {
		  video_id: id,
		  title: clean((titleAnchor || anchor).getAttribute("title") || (titleAnchor || anchor).getAttribute("aria-label") || (titleAnchor || anchor).textContent, 300),
          channel: clean(channel && channel.textContent, 200),
          duration: clean(duration && duration.textContent, 40),
          watch_url: `${location.origin}/watch?v=${encodeURIComponent(id)}`
        });
        if (found.size >= MAX_RESULTS) break;
      }
      if (found.size >= MAX_RESULTS) break;
    }
    return found;
  };
  const activeVideo = () => {
    const videos = Array.from(document.querySelectorAll("video"));
    return videos.find((video) => isVisible(video)) || videos[0] || null;
  };
  const playerSnapshot = () => {
    const video = activeVideo();
    if (!video) return null;
    const caption = document.querySelector(".ytp-subtitles-button");
    return {
      video_id: currentVideoID(),
      title: clean(document.querySelector("h1.ytd-watch-metadata yt-formatted-string, h1.title")?.textContent || document.title.replace(/\s*-\s*YouTube\s*$/, ""), 300),
      state: video.ended ? "ended" : (video.paused ? "paused" : "playing"),
      paused: Boolean(video.paused),
      ended: Boolean(video.ended),
      ready_state: Number(video.readyState),
      current_time: Number.isFinite(video.currentTime) ? video.currentTime : 0,
      duration: Number.isFinite(video.duration) ? video.duration : null,
      muted: Boolean(video.muted),
      volume: Math.round(video.volume * 100),
      captions_enabled: caption ? caption.getAttribute("aria-pressed") === "true" : null
    };
  };
  const contextData = () => ({
    origin: location.origin,
    path: location.pathname,
    route: route(),
    ready: state.registered,
    search_generation: state.searchGeneration,
    query: state.query,
    result_count: state.results.size,
    capabilities: ["search", "list_results", "play", "state", "pause", "resume", "seek", "volume", "captions"]
  });
  const classifyPage = () => {
    const text = clean(document.body?.innerText, 3000).toLowerCase();
    if (text.includes("before you continue to youtube") || text.includes("accept all")) return "consent_required";
    if (text.includes("sign in to confirm") || text.includes("sign in to youtube")) return "signin_required";
    if (text.includes("age-restricted") || text.includes("age restricted")) return "age_restricted";
    if (text.includes("video unavailable")) return "video_unavailable";
    return "site_changed";
  };
  const playAndVerify = async (expectedID) => {
    const video = await waitFor(activeVideo);
    if (!video) return failure("player_not_ready", "The YouTube player did not become ready.");
    try {
      await video.play();
    } catch (error) {
      if (error && error.name === "NotAllowedError") {
        return failure("user_gesture_required", "Chrome requires one customer interaction before audible playback.", {
          user_activation_active: Boolean(navigator.userActivation && navigator.userActivation.isActive),
          user_activation_ever: Boolean(navigator.userActivation && navigator.userActivation.hasBeenActive)
        });
      }
      return failure("player_not_ready", "YouTube rejected the playback request.", { name: clean(error && error.name, 80) });
    }
    const first = playerSnapshot();
    await delay(1200);
    const second = playerSnapshot();
    if (!first || !second || second.video_id !== expectedID || second.paused || second.ready_state < 2 || second.current_time <= first.current_time) {
      return failure("player_not_ready", "Playback could not be verified.", { first, second });
    }
    return success({
      player: second,
      verified_advance_seconds: second.current_time - first.current_time,
      user_activation_active: Boolean(navigator.userActivation && navigator.userActivation.isActive),
      user_activation_ever: Boolean(navigator.userActivation && navigator.userActivation.hasBeenActive)
    });
  };
  const register = async () => {
    const modelContext = document.modelContext || navigator.modelContext;
    if (!modelContext || typeof modelContext.registerTool !== "function") return false;
    const emptySchema = { type: "object", properties: {}, additionalProperties: false };
    const tools = [
      {
        name: "youtube_get_context",
        title: "Get YouTube adapter context",
        description: "Read the selected YouTube tab route, adapter readiness, and supported capabilities.",
        inputSchema: emptySchema,
        annotations: { readOnly: true, untrustedContent: true },
        execute: async () => success(contextData())
      },
      {
        name: "youtube_search",
        title: "Search YouTube",
        description: "Search YouTube using its visible search control and return a bounded structured result list.",
        inputSchema: {
          type: "object",
          properties: { query: { type: "string", minLength: 1, maxLength: MAX_QUERY, description: "The customer's YouTube search query." } },
          required: ["query"],
          additionalProperties: false
        },
        annotations: { readOnly: false, untrustedContent: true },
        execute: async (input) => {
          const query = clean(input && input.query, MAX_QUERY);
          if (!query) return failure("invalid_input", "A non-empty search query is required.");
          const search = document.querySelector("input#search, input[name='search_query'], textarea[name='search_query'], ytd-searchbox input, .ytSearchboxComponentInput");
          const button = document.querySelector("button#search-icon-legacy, ytd-searchbox button#search-icon-legacy, button[aria-label='Search']");
          if (!(search instanceof HTMLInputElement || search instanceof HTMLTextAreaElement) || !(button instanceof HTMLElement)) {
			return failure(classifyPage(), "The visible YouTube search controls are unavailable.");
		  }
		  const valuePrototype = search instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
		  const setter = Object.getOwnPropertyDescriptor(valuePrototype, "value")?.set;
          if (!setter) return failure("site_changed", "The YouTube search input could not be updated.");
          setter.call(search, query);
          search.dispatchEvent(new Event("input", { bubbles: true, composed: true }));
          search.dispatchEvent(new Event("change", { bubbles: true, composed: true }));
          button.click();
		  const results = await waitFor(() => {
			if (route() !== "results") return null;
			const root = searchResultsRoot();
			return root && collectResults(root);
		  }, SEARCH_WAIT_MS);
          if (!results || results.size === 0) return failure("results_not_ready", "No ordinary video results became available.");
          state.searchGeneration += 1;
          state.query = query;
          state.results = results;
          return success({ query, search_generation: state.searchGeneration, results: Array.from(results.values()) });
        }
      },
      {
        name: "youtube_list_results",
        title: "List YouTube search results",
        description: "Return ordinary visible videos from the current YouTube result page.",
        inputSchema: {
          type: "object",
          properties: { limit: { type: "integer", minimum: 1, maximum: MAX_RESULTS, default: MAX_RESULTS } },
          additionalProperties: false
        },
        annotations: { readOnly: true, untrustedContent: true },
        execute: async (input) => {
          const requested = input && input.limit === undefined ? MAX_RESULTS : Number(input && input.limit);
          if (!Number.isInteger(requested) || requested < 1 || requested > MAX_RESULTS) return failure("invalid_input", "limit must be an integer from 1 through 10.");
          if (route() !== "results") return failure("unsupported_route", "Search results are available only on the YouTube results page.");
		  const root = searchResultsRoot();
		  const observed = root ? collectResults(root) : new Map();
          if (observed.size) state.results = observed;
          return success({ query: state.query, search_generation: state.searchGeneration, results: Array.from(state.results.values()).slice(0, requested) });
        }
      },
      {
        name: "youtube_play_video",
        title: "Play a YouTube search result",
		description: "Open a video returned by the current structured YouTube search results. After navigation, refresh the catalog and call youtube_get_player_state; call youtube_resume if it is paused.",
        inputSchema: {
          type: "object",
          properties: {
            video_id: { type: "string", pattern: "^[A-Za-z0-9_-]{6,20}$" },
            search_generation: { type: "integer", minimum: 1 }
          },
          required: ["video_id"],
          additionalProperties: false
        },
        annotations: { readOnly: false, untrustedContent: true },
        execute: async (input) => {
          const id = clean(input && input.video_id, 24);
          const generation = input && input.search_generation;
          if (!state.results.has(id) || (generation !== undefined && generation !== state.searchGeneration)) {
            return failure("stale_result", "The video must come from the current structured search results.");
          }
          const anchor = Array.from(document.querySelectorAll("a[href*='/watch?v=']")).find((candidate) => videoIDFromURL(candidate.href) === id && isVisible(candidate));
          if (!(anchor instanceof HTMLElement)) return failure("stale_result", "The selected result is no longer visible.");
		  const watchURL = anchor.href;
		  setTimeout(() => anchor.click(), 0);
		  return success({
			video_id: id,
			watch_url: watchURL,
			navigation_started: true,
			verification_required: "Refresh the page-tool catalog and call youtube_get_player_state after navigation. Call youtube_resume if paused."
		  });
        }
      },
      {
        name: "youtube_get_player_state",
        title: "Get YouTube player state",
        description: "Read and verify the selected YouTube watch page's visible media state.",
        inputSchema: emptySchema,
        annotations: { readOnly: true, untrustedContent: true },
		execute: async () => {
		  const first = playerSnapshot();
		  if (!first) return failure("player_not_ready", "No YouTube media player is ready.");
		  if (first.paused || first.ended) return success({ player: first, verified_advance_seconds: 0 });
		  await delay(1200);
		  const second = playerSnapshot();
		  if (!second || second.paused || second.ended || second.current_time <= first.current_time) {
			return failure("player_not_ready", "Playback did not advance during verification.", { first, second });
		  }
		  return success({ player: second, verified_advance_seconds: second.current_time - first.current_time });
		}
      },
      {
        name: "youtube_pause",
        title: "Pause YouTube",
        description: "Pause the active YouTube player and verify the result.",
        inputSchema: emptySchema,
        annotations: { readOnly: false },
        execute: async () => {
          const video = activeVideo();
          if (!video) return failure("player_not_ready", "No YouTube media player is ready.");
          video.pause();
          await delay(50);
          const player = playerSnapshot();
          return player && player.paused ? success({ player }) : failure("player_not_ready", "Pause could not be verified.");
        }
      },
      {
        name: "youtube_resume",
        title: "Resume YouTube",
        description: "Resume the active YouTube player and verify advancing playback.",
        inputSchema: emptySchema,
        annotations: { readOnly: false },
        execute: async () => {
          const id = currentVideoID();
          return id ? playAndVerify(id) : failure("unsupported_route", "Open a YouTube watch page before resuming playback.");
        }
      },
      {
        name: "youtube_seek",
        title: "Seek YouTube",
        description: "Seek the active YouTube video to a non-negative time in seconds and verify the result.",
        inputSchema: {
          type: "object",
          properties: { seconds: { type: "number", minimum: 0 } },
          required: ["seconds"],
          additionalProperties: false
        },
        annotations: { readOnly: false },
        execute: async (input) => {
          const seconds = Number(input && input.seconds);
          const video = activeVideo();
          if (!Number.isFinite(seconds) || seconds < 0) return failure("invalid_input", "seconds must be a finite non-negative number.");
          if (!video) return failure("player_not_ready", "No YouTube media player is ready.");
          const target = Number.isFinite(video.duration) ? Math.min(seconds, video.duration) : seconds;
          video.currentTime = target;
          await delay(150);
          const player = playerSnapshot();
          return player && Math.abs(player.current_time - target) <= 2 ? success({ requested_seconds: seconds, observed_seconds: player.current_time, player }) : failure("player_not_ready", "Seek could not be verified.");
        }
      },
      {
        name: "youtube_set_volume",
        title: "Set YouTube volume",
        description: "Set the active YouTube player's volume from 0 through 100 and verify the result.",
        inputSchema: {
          type: "object",
          properties: { volume: { type: "integer", minimum: 0, maximum: 100 } },
          required: ["volume"],
          additionalProperties: false
        },
        annotations: { readOnly: false },
        execute: async (input) => {
          const volume = Number(input && input.volume);
          const video = activeVideo();
          if (!Number.isInteger(volume) || volume < 0 || volume > 100) return failure("invalid_input", "volume must be an integer from 0 through 100.");
          if (!video) return failure("player_not_ready", "No YouTube media player is ready.");
          video.volume = volume / 100;
          if (volume > 0) video.muted = false;
          await delay(50);
          const player = playerSnapshot();
          return player && Math.abs(player.volume - volume) <= 1 ? success({ player }) : failure("player_not_ready", "Volume could not be verified.");
        }
      },
      {
        name: "youtube_set_captions",
        title: "Set YouTube captions",
        description: "Enable or disable captions through the visible YouTube player control when available.",
        inputSchema: {
          type: "object",
          properties: { enabled: { type: "boolean" } },
          required: ["enabled"],
          additionalProperties: false
        },
        annotations: { readOnly: false },
        execute: async (input) => {
          if (!input || typeof input.enabled !== "boolean") return failure("invalid_input", "enabled must be a boolean.");
          const button = document.querySelector(".ytp-subtitles-button");
          if (!(button instanceof HTMLElement) || button.getAttribute("aria-disabled") === "true") return failure("unsupported", "Captions are unavailable for this video.");
          const before = button.getAttribute("aria-pressed") === "true";
          if (before !== input.enabled) button.click();
          await delay(100);
          const observed = button.getAttribute("aria-pressed") === "true";
          return observed === input.enabled ? success({ enabled: observed, player: playerSnapshot() }) : failure("unsupported", "Caption state could not be verified.");
        }
      }
    ];
    await Promise.all(tools.map((tool) => modelContext.registerTool(tool, { signal: state.controller.signal })));
    state.registered = true;
    return true;
  };

  (async () => {
    for (let attempt = 0; attempt < 100 && !state.controller.signal.aborted; attempt += 1) {
      try {
        if (await register()) return;
      } catch (error) {
        state.registrationError = clean(error && (error.stack || error.message || error), 1000);
        return;
      }
      await delay(50);
    }
    state.registrationError = "native WebMCP registration is unavailable";
  })();
})();
