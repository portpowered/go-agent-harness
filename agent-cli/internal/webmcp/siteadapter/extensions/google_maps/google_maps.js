(() => {
  "use strict";

  const host = location.hostname.toLowerCase();
  if (location.protocol !== "https:" || !((host === "www.google.com" && (location.pathname === "/maps" || location.pathname.startsWith("/maps/"))) || host === "maps.google.com")) return;
  const VERSION = "1.0.0";
  const INSTALL_KEY = "__yuiGoogleMapsWebMCPAdapterV1";
  const MAX_TEXT = 300;
  if (globalThis[INSTALL_KEY]) return;

  const state = { registered: false, controller: new AbortController() };
  Object.defineProperty(globalThis, INSTALL_KEY, { value: state, configurable: false });
  const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const clean = (value, maximum = MAX_TEXT) => String(value || "").replace(/\s+/g, " ").trim().slice(0, maximum);
  const success = (data = {}) => ({ ok: true, adapter_version: VERSION, route: location.pathname, data });
  const failure = (code, message, details = {}) => ({ ok: false, adapter_version: VERSION, route: location.pathname, error: { code, message, details } });
  const placeSnapshot = () => {
    const title = document.querySelector("h1.DUwDvf, h1.fontHeadlineLarge, main h1, [role='main'] h1");
    const address = document.querySelector("button[data-item-id='address'], [data-item-id='address'], button[aria-label^='Address']");
    const rating = document.querySelector("div.F7nice, span[role='img'][aria-label*='star']");
    return { name: clean(title?.textContent, 400), address: clean(address?.textContent || address?.getAttribute("aria-label"), 500), rating: clean(rating?.textContent || rating?.getAttribute("aria-label"), 120), url: location.href };
  };
  const directionsSnapshot = () => {
    const inputs = Array.from(document.querySelectorAll("#directions-searchbox-0 input, #directions-searchbox-1 input, input[aria-label*='Starting point'], input[aria-label*='Destination']"));
    const routeCards = Array.from(document.querySelectorAll("[data-trip-index], .Fk3sm, [role='main'] [aria-label*='via']")).slice(0, 5);
    const routes = routeCards.map((card) => ({ summary: clean(card.textContent, 1000), aria_label: clean(card.getAttribute("aria-label"), 500) })).filter((route) => route.summary || route.aria_label);
    return {
      origin: clean(inputs[0]?.value || inputs[0]?.getAttribute("aria-label"), 400),
      destination: clean(inputs[1]?.value || inputs[1]?.getAttribute("aria-label"), 400),
      routes,
      url: location.href
    };
  };
  const classify = () => {
    const text = clean(document.body?.innerText, 4000).toLowerCase();
    if (text.includes("use your precise location") || text.includes("location permission") || text.includes("can't find your location")) return "location_permission_required";
    if (text.includes("before you continue") || text.includes("accept all")) return "consent_required";
    return "site_changed";
  };
  const register = async () => {
    const modelContext = document.modelContext || navigator.modelContext;
    if (!modelContext || typeof modelContext.registerTool !== "function") return false;
    const emptySchema = { type: "object", properties: {}, additionalProperties: false };
    const tools = [
      {
        name: "google_maps_get_context", title: "Get Google Maps context", description: "Read the current Maps route and adapter capabilities.", inputSchema: emptySchema,
        annotations: { readOnly: true, untrustedContent: true },
        execute: async () => success({ origin: location.origin, path: location.pathname, ready: state.registered, capabilities: ["search_place", "read_place", "directions", "read_route"] })
      },
      {
        name: "google_maps_search_place", title: "Search Google Maps", description: "Open a Google Maps place search. Refresh the catalog and call google_maps_read_place after navigation.",
        inputSchema: { type: "object", properties: { query: { type: "string", minLength: 1, maxLength: MAX_TEXT } }, required: ["query"], additionalProperties: false },
        annotations: { readOnly: false, untrustedContent: true },
        execute: async (input) => {
          const query = clean(input?.query, MAX_TEXT);
          if (!query) return failure("invalid_input", "A non-empty place query is required.");
          const target = `https://www.google.com/maps/search/?api=1&query=${encodeURIComponent(query)}`;
          setTimeout(() => location.assign(target), 0);
          return success({ query, navigation_started: true, next_step: "Refresh the catalog and call google_maps_read_place." });
        }
      },
      {
        name: "google_maps_read_place", title: "Read Google Maps place", description: "Read the selected place's visible name, address, and rating.", inputSchema: emptySchema,
        annotations: { readOnly: true, untrustedContent: true },
        execute: async () => {
          let place = placeSnapshot();
          for (let attempt = 0; attempt < 80 && !place.name; attempt += 1) { await delay(100); place = placeSnapshot(); }
          return place.name ? success({ place }) : failure(classify(), "No selected Google Maps place is ready.");
        }
      },
      {
        name: "google_maps_directions", title: "Get Google Maps directions", description: "Open directions to a destination. Omit origin to use the browser's current location. Refresh the catalog and call google_maps_read_route after navigation.",
        inputSchema: {
          type: "object",
          properties: {
            destination: { type: "string", minLength: 1, maxLength: MAX_TEXT },
            origin: { type: "string", minLength: 1, maxLength: MAX_TEXT, description: "Optional; omit to request current location." },
            travel_mode: { type: "string", enum: ["driving", "walking", "bicycling", "transit"], default: "driving" }
          },
          required: ["destination"], additionalProperties: false
        },
        annotations: { readOnly: false, untrustedContent: true },
        execute: async (input) => {
          const destination = clean(input?.destination, MAX_TEXT);
          const origin = clean(input?.origin, MAX_TEXT);
          const travelMode = ["driving", "walking", "bicycling", "transit"].includes(input?.travel_mode) ? input.travel_mode : "driving";
          if (!destination) return failure("invalid_input", "A non-empty destination is required.");
          const params = new URLSearchParams({ api: "1", destination, travelmode: travelMode });
          if (origin) params.set("origin", origin);
          const target = `https://www.google.com/maps/dir/?${params}`;
          setTimeout(() => location.assign(target), 0);
          return success({ destination, origin: origin || "current_location", travel_mode: travelMode, navigation_started: true, next_step: "Refresh the catalog and call google_maps_read_route." });
        }
      },
      {
        name: "google_maps_read_route", title: "Read Google Maps route", description: "Read the visible origin, destination, and bounded route alternatives from the current directions page.", inputSchema: emptySchema,
        annotations: { readOnly: true, untrustedContent: true },
        execute: async () => {
          let directions = directionsSnapshot();
          for (let attempt = 0; attempt < 100 && (!directions.routes.length || !directions.origin || !directions.destination); attempt += 1) { await delay(100); directions = directionsSnapshot(); }
          if (!directions.routes.length) return failure(classify(), "No Google Maps route alternatives became available.", { origin: directions.origin, destination: directions.destination });
          return success({ directions });
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
