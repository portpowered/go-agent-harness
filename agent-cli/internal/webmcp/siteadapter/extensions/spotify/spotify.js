(() => {
  "use strict";

  if (location.protocol !== "https:" || location.hostname.toLowerCase() !== "open.spotify.com") return;
  const VERSION = "1.0.0";
  const INSTALL_KEY = "__yuiSpotifyWebMCPAdapterV1";
  const MAX_QUERY = 200;
  const MAX_RESULTS = 10;
  if (globalThis[INSTALL_KEY]) return;

  const state = { registered: false, generation: 0, query: "", results: new Map(), controller: new AbortController() };
  Object.defineProperty(globalThis, INSTALL_KEY, { value: state, configurable: false });
  const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const clean = (value, maximum = 400) => String(value || "").replace(/\s+/g, " ").trim().slice(0, maximum);
  const success = (data = {}) => ({ ok: true, adapter_version: VERSION, route: location.pathname, data });
  const failure = (code, message, details = {}) => ({ ok: false, adapter_version: VERSION, route: location.pathname, error: { code, message, details } });
  const waitFor = async (predicate, timeout = 12000) => {
    const deadline = performance.now() + timeout;
    while (performance.now() < deadline) {
      const value = predicate();
      if (value) return value;
      await delay(100);
    }
    return null;
  };
  const trackID = (raw) => {
    try {
      const parsed = new URL(raw, location.href);
      if (parsed.origin !== location.origin) return "";
      const match = parsed.pathname.match(/^\/(?:embed\/)?track\/([A-Za-z0-9]{10,40})/);
      return match ? match[1] : "";
    } catch (_) { return ""; }
  };
  const collectTracks = () => {
    const found = new Map();
    for (const anchor of document.querySelectorAll("a[href*='/track/']")) {
      const id = trackID(anchor.href);
      if (!id || found.has(id)) continue;
      const row = anchor.closest("[data-testid='tracklist-row'], [role='row'], li") || anchor.parentElement;
      const text = clean(row?.textContent, 800);
      found.set(id, {
        track_id: id,
        title: clean(anchor.textContent || anchor.getAttribute("aria-label") || anchor.getAttribute("title"), 300),
        details: text,
        url: new URL(anchor.href, location.href).href
      });
      if (found.size >= MAX_RESULTS) break;
    }
    return found;
  };
  const playPauseButton = () => document.querySelector("button[data-testid='control-button-playpause'], button[data-testid='play-pause-button'], button[aria-label='Play'], button[aria-label='Pause']");
  const parseClock = (value) => {
    const normalized = clean(value, 40);
    if (!normalized) return null;
    const parts = normalized.split(":").map(Number);
    if (!parts.length || parts.some((part) => !Number.isFinite(part))) return null;
    return parts.reduce((total, part) => total * 60 + part, 0);
  };
  const playerSnapshot = () => {
    const button = playPauseButton();
    const media = document.querySelector("audio, video");
    const label = clean(button?.getAttribute("aria-label"), 80).toLowerCase();
    const position = parseClock(document.querySelector("[data-testid='playback-position']")?.textContent);
    const duration = parseClock(document.querySelector("[data-testid='playback-duration']")?.textContent);
    const now = document.querySelector("[data-testid='context-item-info-title'] a, [data-testid='now-playing-widget'] a[href*='/track/'], [data-testid='track-name'], h1");
    return {
      track_id: trackID(now?.href || location.href),
      title: clean(now?.textContent, 300),
      state: label.includes("pause") || media && !media.paused ? "playing" : "paused",
      paused: !(label.includes("pause") || media && !media.paused),
      current_time: Number.isFinite(media?.currentTime) ? media.currentTime : position,
      duration: Number.isFinite(media?.duration) ? media.duration : duration,
      muted: media ? Boolean(media.muted) : null,
      volume: media && Number.isFinite(media.volume) ? Math.round(media.volume * 100) : null
    };
  };
  const pageBlocker = () => {
    const text = clean(document.body?.innerText, 4000).toLowerCase();
    if (text.includes("log in") && (text.includes("sign up") || text.includes("continue with"))) return "signin_required";
    if (text.includes("something went wrong")) return "site_changed";
    return "player_not_ready";
  };
  const verifyPlaying = async () => {
    const first = playerSnapshot();
    await delay(1400);
    const second = playerSnapshot();
    const advanced = first.current_time !== null && second.current_time !== null ? second.current_time - first.current_time : 0;
    if (!second.paused && advanced > 0) return success({ player: second, verified_advance_seconds: advanced, verification_mode: "media_time" });
    const previewStream = /^\/embed\/track\//.test(location.pathname) && performance.getEntriesByType("resource").some((entry) => /\/mp3-preview\//.test(entry.name));
    if (!second.paused && previewStream) return success({ player: second, verified_advance_seconds: null, verification_mode: "preview_stream" });
    return failure(pageBlocker(), "Spotify playback did not advance.", { first, second });
  };
  const openPreview = (id) => {
    setTimeout(() => location.assign(`${location.origin}/embed/track/${id}`), 0);
    return success({ track_id: id, navigation_started: true, preview: true, next_step: "Refresh the catalog and call spotify_resume to start the public preview." });
  };
  const register = async () => {
    const modelContext = document.modelContext || navigator.modelContext;
    if (!modelContext || typeof modelContext.registerTool !== "function") return false;
    const emptySchema = { type: "object", properties: {}, additionalProperties: false };
    const tools = [
      {
        name: "spotify_get_context", title: "Get Spotify context", description: "Read Spotify Web Player route and supported controls.", inputSchema: emptySchema,
        annotations: { readOnly: true, untrustedContent: true },
        execute: async () => success({ origin: location.origin, path: location.pathname, ready: state.registered, query: state.query, search_generation: state.generation, result_count: state.results.size, capabilities: ["search_tracks", "list_tracks", "play", "state", "pause", "resume", "volume"] })
      },
      {
        name: "spotify_search_tracks", title: "Search Spotify tracks", description: "Open Spotify track-search results. Refresh the catalog and call spotify_list_tracks after navigation.",
        inputSchema: { type: "object", properties: { query: { type: "string", minLength: 1, maxLength: MAX_QUERY } }, required: ["query"], additionalProperties: false },
        annotations: { readOnly: false, untrustedContent: true },
        execute: async (input) => {
          const query = clean(input?.query, MAX_QUERY);
          if (!query) return failure("invalid_input", "A non-empty track query is required.");
          const target = `${location.origin}/search/${encodeURIComponent(query)}/tracks`;
          setTimeout(() => location.assign(target), 0);
          return success({ query, navigation_started: true, next_step: "Refresh the catalog and call spotify_list_tracks." });
        }
      },
      {
        name: "spotify_list_tracks", title: "List Spotify tracks", description: "Return a bounded structured list of tracks from the current Spotify search page.",
        inputSchema: { type: "object", properties: { limit: { type: "integer", minimum: 1, maximum: MAX_RESULTS, default: MAX_RESULTS } }, additionalProperties: false },
        annotations: { readOnly: true, untrustedContent: true },
        execute: async (input) => {
          const limit = input && input.limit === undefined ? MAX_RESULTS : Number(input?.limit);
          if (!Number.isInteger(limit) || limit < 1 || limit > MAX_RESULTS) return failure("invalid_input", "limit must be an integer from 1 through 10.");
          const found = await waitFor(() => { const tracks = collectTracks(); return tracks.size ? tracks : null; });
          if (!found) return failure(pageBlocker(), "No Spotify track results became available.");
          state.generation += 1;
          state.query = decodeURIComponent(location.pathname.split("/")[2] || "");
          state.results = found;
          return success({ query: state.query, search_generation: state.generation, tracks: Array.from(found.values()).slice(0, limit) });
        }
      },
      {
        name: "spotify_play_track", title: "Play a Spotify track", description: "Play one track from the latest structured Spotify search results and verify advancement.",
        inputSchema: { type: "object", properties: { track_id: { type: "string", pattern: "^[A-Za-z0-9]{10,40}$" }, search_generation: { type: "integer", minimum: 1 } }, required: ["track_id", "search_generation"], additionalProperties: false },
        annotations: { readOnly: false, untrustedContent: true },
        execute: async (input) => {
          const id = clean(input?.track_id, 40);
          if (input?.search_generation !== state.generation || !state.results.has(id)) return failure("stale_result", "The track must come from the latest structured search results.");
          const anchor = Array.from(document.querySelectorAll("a[href*='/track/']")).find((item) => trackID(item.href) === id);
          const row = anchor?.closest("[data-testid='tracklist-row'], [role='row'], li") || anchor?.parentElement;
          const button = row?.querySelector("button[data-testid='play-button'], button[aria-label^='Play']");
          if (!(button instanceof HTMLElement)) return failure(pageBlocker(), "The selected track's play control is unavailable.");
          button.click();
          const played = await verifyPlaying();
          return played.ok ? played : openPreview(id);
        }
      },
      {
        name: "spotify_get_player_state", title: "Get Spotify player state", description: "Read Spotify playback state and verify advancement when playing.", inputSchema: emptySchema,
        annotations: { readOnly: true, untrustedContent: true },
        execute: async () => {
          const stateNow = playerSnapshot();
          if (stateNow.paused) return success({ player: stateNow, verified_advance_seconds: 0 });
          return await verifyPlaying();
        }
      },
      {
        name: "spotify_pause", title: "Pause Spotify", description: "Pause Spotify and verify the player state.", inputSchema: emptySchema,
        annotations: { readOnly: false },
        execute: async () => {
          const button = await waitFor(() => playPauseButton(), 8000);
          if (!(button instanceof HTMLElement) || !clean(button.getAttribute("aria-label")).toLowerCase().includes("pause")) return failure(pageBlocker(), "Spotify is not currently playing.");
          button.click(); await delay(150);
          const player = playerSnapshot();
          return player.paused ? success({ player }) : failure("player_not_ready", "Pause could not be verified.");
        }
      },
      {
        name: "spotify_resume", title: "Resume Spotify", description: "Resume Spotify and verify advancing playback.", inputSchema: emptySchema,
        annotations: { readOnly: false },
        execute: async () => {
          const button = await waitFor(() => playPauseButton(), 8000);
          if (!(button instanceof HTMLElement)) return failure(pageBlocker(), "Spotify's play control is unavailable.");
          if (!clean(button.getAttribute("aria-label")).toLowerCase().includes("pause")) button.click();
          return await verifyPlaying();
        }
      },
      {
        name: "spotify_set_volume", title: "Set Spotify volume", description: "Set Spotify Web Player volume from 0 through 100 and verify the control value.",
        inputSchema: { type: "object", properties: { volume: { type: "integer", minimum: 0, maximum: 100 } }, required: ["volume"], additionalProperties: false },
        annotations: { readOnly: false },
        execute: async (input) => {
          const volume = Number(input?.volume);
          if (!Number.isInteger(volume) || volume < 0 || volume > 100) return failure("invalid_input", "volume must be an integer from 0 through 100.");
          const slider = document.querySelector("[data-testid='volume-bar'] input[type='range'], input[type='range'][aria-label*='Volume']");
          if (!(slider instanceof HTMLInputElement)) return failure("unsupported", "Spotify's volume slider is unavailable.");
          const minimum = Number(slider.min || 0), maximum = Number(slider.max || 1);
          const value = minimum + (maximum - minimum) * volume / 100;
          Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(slider, String(value));
          slider.dispatchEvent(new Event("input", { bubbles: true })); slider.dispatchEvent(new Event("change", { bubbles: true }));
          await delay(100);
          const observed = Math.round((Number(slider.value) - minimum) * 100 / (maximum - minimum));
          return Math.abs(observed - volume) <= 1 ? success({ requested_volume: volume, observed_volume: observed, player: playerSnapshot() }) : failure("player_not_ready", "Volume could not be verified.");
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
