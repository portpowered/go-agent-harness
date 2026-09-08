(() => {
  "use strict";

  const ALLOWED_HOSTS = new Set(["x.com", "www.x.com", "twitter.com", "www.twitter.com"]);
  if (location.protocol !== "https:" || !ALLOWED_HOSTS.has(location.hostname.toLowerCase())) return;
  const VERSION = "1.1.0";
  const INSTALL_KEY = "__yuiXWebMCPAdapterV1";
  const MAX_POST_LENGTH = 280;
  if (globalThis[INSTALL_KEY]) return;

  const state = {
    registered: false,
    generation: 0,
    draftToken: "",
    draftText: "",
    draftAccount: "",
    draftMedia: "",
    draftAllowsMedia: false,
    draftTarget: null,
    mediaGuard: null,
    mediaChanged: false,
    upload: null,
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
  // Bound selectors to the composer, never a timeline video or another dialog.
  const composerRoot = (target = composer()) => {
    for (let root = target?.parentElement; root; root = root.parentElement) {
      if (root.querySelector('[data-testid="tweetButtonInline"], [data-testid="tweetButton"]')) return root;
    }
    return null;
  };
  const mediaSignature = (root) => JSON.stringify(Array.from(root?.querySelectorAll('video, [data-testid="attachments"] img') || [], element => [element.tagName, element.currentSrc || element.src || ""]));
  const mediaProcessing = (root) => Array.from(root?.querySelectorAll('[role="progressbar"]') || []).some(element => !element.closest('[data-testid="countdown-circle"]'));
  const hasMedia = (root) => Boolean(root?.querySelector('video, [data-testid="attachments"], [data-testid="removeMedia"]')) || mediaProcessing(root) || Array.from(root?.querySelectorAll('input[type="file"]') || []).some(element => element.files?.length);
  const postButton = (root) => Array.from(root?.querySelectorAll('[data-testid="tweetButtonInline"], [data-testid="tweetButton"]') || []).find(element => !element.disabled && element.getAttribute("aria-disabled") !== "true");
  const activeUpload = (token) => {
    const upload = state.upload;
    if (!upload || upload.token !== token || upload.expires < Date.now() || upload.account !== accountHandle()) {
      if (upload?.expires < Date.now()) state.upload = null;
      return null;
    }
    return upload;
  };
  const clearPreparedState = () => {
    state.draftToken = "";
    state.draftText = "";
    state.draftAccount = "";
    state.draftMedia = "";
    state.draftAllowsMedia = false;
    state.draftTarget = null;
    state.mediaGuard?.abort();
    state.mediaGuard = null;
    state.mediaChanged = false;
  };

  const register = async () => {
    const modelContext = document.modelContext || navigator.modelContext;
    if (!modelContext || typeof modelContext.registerTool !== "function") return false;
    const emptySchema = { type: "object", properties: {}, additionalProperties: false };
    const tools = [
      {
        name: "x_get_context", title: "Get X context", description: "Read sign-in, account, route, and prepared-draft state without changing X.", inputSchema: emptySchema,
        annotations: { readOnly: true, untrustedContent: true },
        execute: async () => success({ origin: location.origin, path: location.pathname, ready: state.registered, signed_in: isSignedIn(), account_handle: accountHandle(), draft_generation: state.generation, draft_prepared: Boolean(state.draftToken), capabilities: ["prepare_post", "prepare_video_post", "publish_post", "clear_draft"], max_video_bytes: 67108864, max_chunk_bytes: 32768 })
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
          if (hasMedia(composerRoot(target))) return failure("existing_media", "The composer already has media; text-only preparation cannot authorize it.");
          if (composerText(target) && composerText(target) !== text) return failure("existing_draft", "The composer contains another draft. Preserve or clear it first.");
          setComposerText(target, text);
          await delay(100);
          const observed = composerText(target);
          if (hasMedia(composerRoot(target))) return failure("existing_media", "Media was added during text-only preparation; it was not authorized.");
          if (observed !== text) return failure("site_changed", "X did not retain the exact requested draft text.", { requested_text: text, observed_text: observed });
          state.generation += 1;
          state.draftText = text;
          state.draftAccount = accountHandle();
          state.draftMedia = mediaSignature(composerRoot(target));
          state.draftAllowsMedia = false;
          state.draftTarget = target;
          state.draftToken = draftToken();
          return success({ draft_generation: state.generation, draft_token: state.draftToken, text, character_count: text.length, published: false, next_step: "Review the exact text, then call x_publish_post with this draft_token, the same text, and confirm=true." });
        }
      },
      {
        name: "x_begin_video_upload", title: "Begin X video transfer", description: "Stage one MP4 of at most 64 MiB in page memory. Does not publish. Requires the expected signed-in account and SHA-256.",
        inputSchema: { type: "object", properties: { filename: { type: "string", pattern: "^[^/\\\\]{1,120}\\.mp4$" }, size: { type: "integer", minimum: 12, maximum: 67108864 }, sha256: { type: "string", pattern: "^[a-f0-9]{64}$" }, account: { type: "string", pattern: "^@[A-Za-z0-9_]{1,15}$" } }, required: ["filename", "size", "sha256", "account"], additionalProperties: false },
        annotations: { readOnly: false, untrustedContent: false },
        execute: async (input) => {
          if (!isSignedIn() || !accountHandle() || input?.account !== accountHandle()) return failure("account_mismatch", "The expected X account is not signed in.");
          if (!Number.isInteger(input.size) || input.size < 12 || input.size > 67108864 || !/^[a-f0-9]{64}$/.test(input.sha256) || !/^[^/\\]{1,120}\.mp4$/.test(input.filename)) return failure("invalid_input", "Expected a bounded MP4 filename, size, and SHA-256.");
          if (state.upload && state.upload.expires > Date.now()) return failure("upload_in_progress", "Finish or cancel the existing video transfer first.");
          if (state.draftToken || composerText() || hasMedia(composerRoot())) return failure("existing_draft", "Preserve or clear the existing composer before uploading a video.");
          state.upload = { token: crypto.randomUUID(), account: input.account, filename: input.filename, size: input.size, sha256: input.sha256, received: 0, chunks: [], expires: Date.now() + 600000, attached: false, busy: false };
          const started = state.upload;
          setTimeout(() => { if (state.upload === started) { started.chunks = []; state.upload = null; state.mediaGuard?.abort(); } }, 600000);
          return success({ upload_token: state.upload.token, max_chunk_bytes: 32768, expires_in_seconds: 600 });
        }
      },
      {
        name: "x_append_video_chunk", title: "Transfer X video chunk", description: "Append an ordered base64 chunk, at most 32 KiB decoded, to the staged video. No publishing.",
        inputSchema: { type: "object", properties: { upload_token: { type: "string", maxLength: 160 }, offset: { type: "integer", minimum: 0 }, data_base64: { type: "string", minLength: 4, maxLength: 43692 } }, required: ["upload_token", "offset", "data_base64"], additionalProperties: false },
        annotations: { readOnly: false, untrustedContent: false },
        execute: async (input) => {
          const upload = activeUpload(input?.upload_token);
          if (!upload) return failure("stale_upload", "The upload expired, changed account, or does not exist.");
          if (upload.busy || upload.attached || input.offset !== upload.received) return failure("chunk_order", "The chunk offset must equal the current received byte count.", { received_bytes: upload.received });
          let bytes;
          try {
            if (typeof input.data_base64 !== "string" || input.data_base64.length > 43692 || !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(input.data_base64)) throw Error("base64");
            bytes = Uint8Array.from(atob(input.data_base64), char => char.charCodeAt(0));
          } catch { return failure("invalid_chunk", "Invalid bounded base64 chunk."); }
          if (!bytes.length || bytes.length > 32768 || upload.received + bytes.length > upload.size) return failure("invalid_chunk", "Chunk exceeds the declared video size or chunk limit.");
          upload.chunks.push(bytes);
          upload.received += bytes.length;
          return success({ received_bytes: upload.received, total_bytes: upload.size });
        }
      },
      {
        name: "x_prepare_video_post", title: "Prepare X video post", description: "Verify the staged MP4 hash and attach it to X. Repeat with identical inputs while video_processing; never publishes. Returns a one-use token only when the video preview and Post control are ready.",
        inputSchema: { type: "object", properties: { upload_token: { type: "string", maxLength: 160 }, text: { type: "string", minLength: 1, maxLength: MAX_POST_LENGTH } }, required: ["upload_token", "text"], additionalProperties: false },
        annotations: { readOnly: false, untrustedContent: false },
        execute: async (input) => {
          const upload = activeUpload(input?.upload_token);
          const text = clean(input?.text, MAX_POST_LENGTH + 1);
          if (!upload) return failure("stale_upload", "The upload expired, changed account, or does not exist.");
          if (!text || text.length > MAX_POST_LENGTH) return failure("invalid_input", "Expected 1 through 280 characters.");
          if (upload.busy) return failure("upload_busy", "Another preparation is in progress.");
          if (upload.received !== upload.size) return failure("incomplete_upload", "Transfer all video bytes before preparing.");
          if (upload.attached && upload.text !== text) return failure("text_mismatch", "A processing video's caption cannot change.");
          upload.busy = true;
          try {
            if (!upload.attached) {
              const file = new File(upload.chunks, upload.filename, { type: "video/mp4" });
              const buffer = await file.arrayBuffer();
              const hash = Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", buffer)), byte => byte.toString(16).padStart(2, "0")).join("");
              if (hash !== upload.sha256) return failure("hash_mismatch", "Video bytes do not match the declared SHA-256.");
              if (String.fromCharCode(...new Uint8Array(buffer, 4, 4)) !== "ftyp") return failure("invalid_video", "Expected an MP4 ftyp header.");
              const target = await openComposer();
              const root = composerRoot(target);
              if (!target || !root) return failure("composer_not_ready", "The X composer is unavailable.");
              if (composerText(target) || hasMedia(root)) return failure("existing_draft", "The composer changed during transfer; nothing was overwritten.");
              const fileInput = root.querySelector('input[type="file"][data-testid="fileInput"]');
              if (!fileInput) return failure("site_changed", "The composer video file input is unavailable.");
              if (activeUpload(input.upload_token) !== upload) return failure("stale_upload", "Upload state changed before attachment.");
              setComposerText(target, text);
              await delay(100);
              if (composerText(target) !== text) return failure("site_changed", "X did not retain the exact caption.");
              const transfer = new DataTransfer();
              transfer.items.add(file);
              fileInput.files = transfer.files;
              state.mediaGuard?.abort();
              state.mediaGuard = new AbortController();
              state.mediaChanged = false;
              let ownChange = true;
              root.addEventListener("change", event => {
                if (event.target?.type !== "file") return;
                if (ownChange && event.target === fileInput && fileInput.files[0] === file) { ownChange = false; return; }
                state.mediaChanged = true;
              }, { capture: true, signal: state.mediaGuard.signal });
              root.addEventListener("click", event => {
                if (event.target.closest?.('[data-testid="removeMedia"], [aria-label="Edit video"], [aria-label="Edit media"]')) state.mediaChanged = true;
              }, { capture: true, signal: state.mediaGuard.signal });
              upload.attached = true;
              upload.text = text;
              upload.root = root;
              upload.target = target;
              upload.chunks = [];
              fileInput.dispatchEvent(new Event("change", { bubbles: true }));
            }
            await delay(250);
            const root = upload.root;
            if (state.mediaChanged || !root.isConnected || composerText(upload.target) !== text || accountHandle() !== upload.account) return failure("draft_changed", "The prepared video composer or account changed.");
            // The caption character counter is also a progressbar. It is not
            // an upload indicator and must not hold a ready video forever.
            if (!root.querySelector("video") || mediaProcessing(root) || !postButton(root)) return success({ video_processing: true, published: false, upload_token: upload.token });
            state.generation += 1;
            state.draftText = text;
            state.draftAccount = upload.account;
            state.draftMedia = mediaSignature(root);
            state.draftAllowsMedia = true;
            state.draftTarget = upload.target;
            state.draftToken = draftToken();
            state.upload = null;
            return success({ draft_token: state.draftToken, text, account_handle: state.draftAccount, video_sha256: upload.sha256, video_bytes: upload.size, filename: upload.filename, published: false });
          } finally { upload.busy = false; }
        }
      },
      {
        name: "x_cancel_video_upload", title: "Cancel staged X video transfer", description: "Release staged video bytes. If already attached, preserves the visible composer for manual inspection; does not publish or remove it.",
        inputSchema: { type: "object", properties: { upload_token: { type: "string", maxLength: 160 } }, required: ["upload_token"], additionalProperties: false },
        annotations: { readOnly: false, untrustedContent: false },
        execute: async (input) => {
          const upload = activeUpload(input?.upload_token);
          if (!upload) return failure("stale_upload", "No matching active upload.");
          if (upload.busy) return failure("upload_busy", "Preparation is in progress.");
          state.upload = null;
          state.mediaGuard?.abort();
          return success({ canceled: true, composer_preserved: upload.attached, published: false });
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
          if (!target || target !== state.draftTarget || composerText(target) !== state.draftText) return failure("draft_changed", "The visible X composer no longer exactly matches the prepared draft.");
          const root = composerRoot(target);
          if (!state.draftAccount || accountHandle() !== state.draftAccount) return failure("account_mismatch", "The signed-in account changed after preparation.");
          if (!state.draftAllowsMedia && hasMedia(root)) return failure("media_changed", "Text-only preparation did not authorize attached or pending media.");
          if (state.mediaChanged || mediaSignature(root) !== state.draftMedia) return failure("media_changed", "The attached media changed after preparation.");
          const button = postButton(root);
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
          if (!target || composerText(target) !== state.draftText || accountHandle() !== state.draftAccount || mediaSignature(composerRoot(target)) !== state.draftMedia) return failure("draft_changed", "The prepared draft changed; nothing was cleared.");
          if (hasMedia(composerRoot(target))) return failure("manual_clear_required", "Video drafts must be removed in the X composer; nothing was cleared.");
          setComposerText(target, "");
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
