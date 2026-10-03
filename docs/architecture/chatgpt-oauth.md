# GPT-Live auth via ChatGPT: `yui auth chatgpt`

---
status: decided (user decisions of 2026-10-02 below); phase 2, the login and token store, is #635
component: go-llm-gateway, go-agent-runtime, agent-cli
extends: docs/architecture/gpt-live-provider.md (#630); settles its 2.3 "Auth" and 2.4 "Delegation mode", which were PENDING this document
sources verified: 2026-10-02
---

This document extends the GPT-Live provider design
([`gpt-live-provider.md`](gpt-live-provider.md), #630). That design left two
sections PENDING this document, and both are now decided:

- 2.3 "Auth": it fixes the shape (ChatGPT auth store by default, API key as
  the fallback, headers injected through a credential provider);
- 2.4 "Delegation mode": it leaves the choice between client and Responses
  delegation open.

This document asks whether a ChatGPT subscription login can replace the
Platform API key, the way Codex CLI and OpenClaw sign in, so that the
end-to-end flow is:

```bash
yui auth chatgpt
yui session --provider openai-live --model <gpt-live model>
```

with no `OPENAI_API_KEY`.

## Summary

1. **Login is well understood and is the same in both references.** It is an
   OAuth 2.0 authorization-code flow with PKCE (S256) against
   `https://auth.openai.com`. Both use the public Codex client id
   `app_EMoamEEZ73f0CkXaXp7hrann`, a loopback redirect on port 1455, rotating
   refresh tokens, and a device-code fallback. Section 1 has the details.
2. **A ChatGPT token cannot open the public `gpt-live-1` model.** Both
   references say so:
   - OpenClaw throws `"GPT-Live API sessions require a Platform API key"` for
     `gpt-live-1`, and its docs say "ChatGPT subscription credentials are not
     a fallback for `gpt-live-1`".
   - Codex's direct-WebSocket realtime path returns `"realtime conversation
     requires API key auth"` for a ChatGPT login.
3. **A ChatGPT token can open a different GPT-Live model, `gpt-live-1-codex`,
   but only over WebRTC.** Call creation goes through the ChatGPT backend
   (`POST https://chatgpt.com/backend-api/codex/realtime/calls?intent=quicksilver&architecture=avas`).
   Control then attaches as a sideband WebSocket at
   `wss://api.openai.com/v1/live/{call_id}`, using the same bearer token plus
   `chatgpt-account-id`. That route speaks the older Codex "quicksilver v2"
   wire dialect, not the public `session.*` protocol in #630. It also
   needs a server-side WebRTC peer with Opus.
4. **Recommendation:**
   - Keep `openai-live` as the provider name.
   - Route by model and credential: `gpt-live-1` with an API key over the
     primary WebSocket (#630 unchanged); `gpt-live-1-codex` with the
     ChatGPT credential over WebRTC plus sideband.
   - Keep #630's credential order (ChatGPT store first, API key as the
     fallback). The model then follows the credential: the ChatGPT store
     selects `gpt-live-1-codex`, and an API key selects `gpt-live-1`.
   - Keep **client delegation**, and back it with Responses over the ChatGPT
     backend (`https://chatgpt.com/backend-api/codex/responses`) using the
     same token. Server-hosted Responses delegation is not offered on the
     subscription route, so it does not simplify anything there.
5. **What it removes:** the need for a Platform key, and its billing, for
   voice and for the delegation backend. One login covers both. **What it
   adds:** a WebRTC peer, Opus, and a second GPT-Live wire dialect.
   Section 3.6 compares the two.
6. **Terms:** neither reference publishes a statement that third-party reuse
   of the Codex client id is permitted. OpenClaw offers a separate
   app-registered flow, "Sign in with ChatGPT (Beta)", as the app-specific
   alternative, and that flow does not cover realtime voice. See section 5.

## Decisions (user, 2026-10-02)

| Topic | Decision | Where |
| --- | --- | --- |
| Voice | Build the Codex WebRTC route. `openai-live` routes by model: `gpt-live-1-codex` runs on the ChatGPT login over WebRTC plus the sideband; `gpt-live-1` runs on an API key over the primary WebSocket. | 3.2, 3.5; `gpt-live-provider.md` 2.3 and PR 9 |
| Client id | Reuse the Codex public client id `app_EMoamEEZ73f0CkXaXp7hrann`. | 1.1, 5 |
| API-key exchange | Not offered. The login never exchanges the id token for a Platform API key. | 3.4 |
| Delegation | Client delegation. The backend runs Responses at `chatgpt.com/backend-api/codex/responses` with the same token. | 2, 3.3; `gpt-live-provider.md` 2.4 |
| Credential order | ChatGPT login first, API key as the fallback. An explicit `--model` narrows the order to the credential that model accepts. | 3.5; `gpt-live-provider.md` 2.3 |
| Scopes | Keep `openid profile email offline_access`. The Codex source shows the `api.connectors.*` scopes are not needed (4.3). | 1.1, 4.3 |

Labels used below: **CONFIRMED** means it is read directly from code or
docs in the cited reference. **INFERRED** means it is reasoned from that
code, and has not been observed against the live service in this work. No
real login or network call was made.

## Sources

| Source | Revision | Paths |
| --- | --- | --- |
| Codex (`openai/codex`, local copy) | `1e6185e522`, 2026-08-23 | `codex-rs/login/src/{server.rs,pkce.rs,device_code_auth.rs,token_data.rs,success_page.rs}`, `codex-rs/login/src/auth/{manager.rs,storage.rs,revoke.rs}`, `codex-rs/core/src/{client.rs,realtime_conversation.rs}`, `codex-rs/codex-api/src/endpoint/{realtime_call.rs,realtime_websocket/methods.rs,realtime_websocket/protocol_frameless_bidi.rs}`, `codex-rs/model-provider-info/src/lib.rs`, `codex-rs/model-provider/src/auth.rs` |
| OpenClaw (`openclaw/openclaw`, shallow clone) | `87af5763`, 2026-10-02 | `extensions/openai/openai-chatgpt-oauth-*.ts`, `openai-chatgpt-device-code.ts`, `realtime-auth.ts`, `realtime-quicksilver*.ts`, `token-sharing*.ts`, `packages/ai/src/providers/openai-chatgpt-responses.ts`, `docs/providers/openai/{authentication,setup,voice-and-speech}.md`, `docs/concepts/oauth.md` |
| #630 (merged) | `acaba43dc` | `docs/architecture/gpt-live-provider.md` |

The local Codex copy predates the public GPT-Live release (2026-09-10). Its
GPT-Live model is the internal `gpt-live-1-boulder-alpha`. OpenClaw is
current and names the subscription model `gpt-live-1-codex`.

---

## 1. The OAuth flow

### 1.1 Constants

| Item | Codex | OpenClaw | Status |
| --- | --- | --- | --- |
| Issuer | `https://auth.openai.com` | same | CONFIRMED |
| Authorize | `GET {issuer}/oauth/authorize` | same | CONFIRMED |
| Token (code exchange and refresh) | `POST {issuer}/oauth/token` | same | CONFIRMED |
| Revoke | `POST {issuer}/oauth/revoke`, JSON `{token, token_type_hint, client_id?}` | not used | CONFIRMED (Codex) |
| Client id | `app_EMoamEEZ73f0CkXaXp7hrann` (env override `CODEX_APP_SERVER_LOGIN_CLIENT_ID`) | same, hard-coded | CONFIRMED |
| Scope | `openid profile email offline_access api.connectors.read api.connectors.invoke` | `openid profile email offline_access` | CONFIRMED |
| Redirect URI | `http://localhost:{port}/auth/callback` | `http://localhost:1455/auth/callback` (host may be `127.0.0.1` or `[::1]`) | CONFIRMED |
| Port | 1455; falls back to 1457 ("Keep in sync with the Codex CLI Hydra redirect URI allow-list") | 1455 only | CONFIRMED |
| Bind address | `127.0.0.1` | the configured loopback host | CONFIRMED |
| PKCE | 64 random bytes, base64url without padding (86 characters); S256 challenge | `generatePKCE()` (S256) | CONFIRMED |
| `state` | 32 random bytes, base64url | 16 random bytes, hex | CONFIRMED |
| Extra authorize parameters | `id_token_add_organizations=true`, `codex_cli_simplified_flow=true`, `originator=codex_cli_rs`, optional `allowed_workspace_id` | `id_token_add_organizations=true`, `codex_cli_simplified_flow=true`, `originator=openclaw` | CONFIRMED |

The redirect allow-list is server side. Ports 1455 and 1457 are the only ones
Codex documents as registered. **INFERRED:** any other port fails at
authorize time.

OpenClaw sends its own `originator` (`openclaw`) and the login still
succeeds, so the originator is not checked against the client id.
**INFERRED** from OpenClaw shipping it; not observed here.

### 1.2 Browser flow

1. Generate the PKCE verifier and challenge, and a random `state`.
2. Bind `127.0.0.1:1455`. If the port is busy, Codex first sends
   `GET /cancel` to the old listener (it is probably a stale Codex login),
   retries for about 2 s, then falls back to 1457. OpenClaw instead offers a
   manual paste of the redirect URL.
3. Open the browser on:

   ```text
   https://auth.openai.com/oauth/authorize?response_type=code
     &client_id=app_EMoamEEZ73f0CkXaXp7hrann
     &redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback
     &scope=openid%20profile%20email%20offline_access
     &code_challenge=<S256(verifier)>&code_challenge_method=S256
     &id_token_add_organizations=true&codex_cli_simplified_flow=true
     &state=<state>&originator=<originator>
   ```

4. The browser returns to `GET /auth/callback?code=...&state=...`, or to
   `?error=...&error_description=...`. A wrong `state` gets a 400 response
   and the server keeps waiting. An `access_denied` whose description contains
   `missing_codex_entitlement` means "Codex is not enabled for your
   workspace".
5. Exchange the code, form-encoded:

   ```http
   POST /oauth/token
   Content-Type: application/x-www-form-urlencoded

   grant_type=authorization_code&code=<code>&redirect_uri=<same uri>
   &client_id=<client id>&code_verifier=<verifier>
   ```

   The response is JSON: `{id_token, access_token, refresh_token}`. OpenClaw
   also reads `expires_in` and requires it.
6. Codex then **optionally** exchanges the id token for a Platform API key
   (section 3.4) and redirects the browser to a local `/success` page.

### 1.3 Device-code (headless) flow

Both references implement the same non-standard device flow (CONFIRMED):

1. `POST {issuer}/api/accounts/deviceauth/usercode` with JSON
   `{"client_id": ...}`. The response is
   `{device_auth_id, user_code | usercode, interval}`, where `interval` is a
   string of seconds. A 404 means device login is not enabled for that server.
2. Show the URL `{issuer}/codex/device` and the code. Both expire after
   15 minutes.
3. Poll `POST {issuer}/api/accounts/deviceauth/token` with JSON
   `{device_auth_id, user_code}` every `interval` seconds:
   - 403 or 404 means pending;
   - 200 returns `{authorization_code, code_challenge, code_verifier}`;
   - any other status is fatal.
4. Exchange `authorization_code` at `/oauth/token` as in 1.2, using
   `redirect_uri={issuer}/deviceauth/callback` and the **server-supplied**
   `code_verifier`.

The OpenClaw docs note that the device-code grant does not include
`api.connectors.invoke`. That does not matter here.

### 1.4 Tokens, claims and expiry

- The access token and the id token are JWTs. Neither reference verifies
  signatures locally; both only decode the payload.
- Claims live under `https://api.openai.com/auth`: `chatgpt_account_id`,
  `chatgpt_plan_type`, `chatgpt_user_id`, `organization_id`, `project_id`,
  `completed_platform_onboarding`, `is_org_owner`.
  - The email is the top-level `email` claim of the id token. OpenClaw reads
    it from `https://api.openai.com/profile.email` in the access token.
  - Codex reads the account id from the **id token**; OpenClaw reads it from
    the **access token**. Both are CONFIRMED to carry it.
- Expiry:
  - OpenClaw uses `expires_in` from the token response.
  - Codex uses the access token's JWT `exp`. It refreshes proactively when
    `exp` is within **5 minutes**, or, when `exp` is unknown, when
    `last_refresh` is older than **8 days**.
  - Neither reference states the actual lifetime. **INFERRED:** it is hours,
    not minutes.
- Refresh:
  - Codex sends JSON `{client_id, grant_type:"refresh_token", refresh_token}`.
  - OpenClaw sends the same fields form-encoded.
  - The response has optional `id_token`, `access_token` and
    `refresh_token`. An omitted refresh token means "keep the old one".
- Refresh tokens **rotate**. Reuse returns `refresh_token_reused`.
  - Permanent failure codes (both references): `refresh_token_expired`,
    `refresh_token_reused`, `refresh_token_invalidated`, plus
    `invalid_grant` on a 400 and any 401. These mean "sign in again".
  - Everything else is transient.
- Concurrency:
  - Codex reloads `auth.json` before refreshing. If another process has
    already refreshed, it uses that result; it also detects an account switch.
  - OpenClaw wraps refresh in a per-profile file lock
    (`oauth-profile-lock.ts`).
  - Because tokens rotate, two processes that refresh with the same token
    would log one of them out. Locking is therefore required.

### 1.5 Storage format (Codex `auth.json`)

```json
{
  "auth_mode": "chatgpt",
  "OPENAI_API_KEY": null,
  "tokens": {
    "id_token": "<jwt>",
    "access_token": "<jwt>",
    "refresh_token": "<opaque>",
    "account_id": "<chatgpt_account_id>"
  },
  "last_refresh": "2026-10-02T12:00:00Z"
}
```

The file is written with mode `0600`, or kept in the OS keyring.
`OPENAI_API_KEY` holds the token-exchange result described in 3.4.

### 1.6 Where Codex and OpenClaw differ

| Topic | Codex | OpenClaw |
| --- | --- | --- |
| Scope | adds `api.connectors.read api.connectors.invoke` | minimal OIDC plus `offline_access` |
| Originator | `codex_cli_rs` | `openclaw` |
| Port fallback | 1457 after cancelling a stale listener | manual paste of the redirect URL |
| API-key token exchange at login | yes, best effort, stored in `OPENAI_API_KEY` | no |
| Refresh encoding | JSON | form |
| Expiry source | JWT `exp`, plus an 8-day staleness fallback | `expires_in` |
| Account id source | id token | access token |
| Storage | `~/.codex/auth.json` or keyring | per-agent auth profile store |
| Refresh concurrency | reload and compare before refreshing | per-profile file lock |

---

## 2. Inference with a ChatGPT token: Responses over the ChatGPT backend

**CONFIRMED** from Codex (`CHATGPT_CODEX_BASE_URL`) and OpenClaw
(`OPENAI_CODEX_RESPONSES_BASE_URL`):

- Base URL: `https://chatgpt.com/backend-api/codex`.
- Inference: `POST {base}/responses`. This is the Responses API; there is no
  Chat Completions endpoint.
- Model list: `GET {base}/models?client_version=<semver>` (Codex). The
  allowed models depend on the account and plan. OpenClaw's catalog uses
  current GPT-5.x/6 names, and its docs list retirements from "the
  ChatGPT-account Codex route". Read this list at runtime; never hard-code it.
- Required headers (OpenClaw's SSE path):
  - `Authorization: Bearer <access_token>`
  - `chatgpt-account-id: <chatgpt_account_id>`
  - `originator: <name>`
  - `OpenAI-Beta: responses=experimental`
  - `accept: text/event-stream`
  - `content-type: application/json`
  - optional `session_id` and `x-client-request-id`
- Codex also sends `version`, `session-id`/`thread-id`,
  `x-codex-installation-id`, `x-codex-turn-state` and optional
  attestation headers.
- Body: a standard Responses request with `stream: true` and
  **`store: false`**. OpenClaw: "ChatGPT Codex Responses rejects `store: true`
  ('Store must be set to false')".
  - `instructions` is sent as a separate field.
  - `include: ["reasoning.encrypted_content"]` keeps reasoning across turns
    without server storage.
  - Function tools work. A WebSocket transport also exists (Codex beta
    `responses_websockets=2026-02-06`).
- Errors: `usage_limit_reached`, `usage_not_included` and `rate_limit_exceeded`
  mean the plan's Codex allowance is exhausted (OpenClaw).

**Consequence for this repo:** the existing `openai` text provider uses
`/chat/completions` (`go-llm-gateway/pkg/providers/openai/provider.go`). A
ChatGPT credential therefore needs a new Responses client. It is a
straightforward client: SSE parsing of `response.*` events, `store:false`,
and the headers above. It is not built here.

---

## 3. Realtime and voice with a ChatGPT token (the primary question)

### 3.1 Routes, by evidence

| # | Route | ChatGPT token? | Evidence |
| --- | --- | --- | --- |
| A | Public GPT-Live `gpt-live-1`, primary WebSocket `wss://api.openai.com/v1/live/sessions` (`gpt-live-provider.md`) | **No** | CONFIRMED (OpenClaw): the direct WebSocket is gated on a Platform API key in `realtime-quicksilver-gateway-bridge.ts:234` (`connectDirect` only for `auth.type === "api-key"`), `realtime-voice-provider-factory.ts:423` and `realtime-voice-session-policy.ts:480`. The docs say "ChatGPT subscription credentials are not a fallback for `gpt-live-1`". |
| B | Public GPT-Live `gpt-live-1` over WebRTC, `POST /v1/live/sessions` | **No** | CONFIRMED (OpenClaw, same branch of code) |
| C | Codex GPT-Live `gpt-live-1-codex`: WebRTC call created on the **ChatGPT backend**, then a sideband on `api.openai.com/v1/live/{call_id}` | **Yes** | CONFIRMED in both references (3.2) |
| D | Codex GPT-Live over a **direct** WebSocket (`wss://api.openai.com/v1/live?model=...`) | **No evidence of support** | CONFIRMED that both references use API keys only here: OpenClaw `connectDirect` takes `auth.type === "api-key"`; Codex `realtime_api_key` fails for ChatGPT auth; its fallback is marked "TODO(aibrahim): Remove this temporary fallback once realtime auth no longer requires API key auth for ChatGPT/SIWC sessions." |
| E | GA Realtime `gpt-realtime-*` over WebSocket `/v1/realtime` | **No** | CONFIRMED (OpenClaw docs: "Gateway-controlled GA relay, ... direct backend sockets ... require Platform auth"; Codex as for D) |
| F | GA Realtime over WebRTC, `POST https://api.openai.com/v1/realtime/calls` (multipart) | **Yes, if the account has access** | CONFIRMED in OpenClaw code and tests (`realtime-quicksilver-ga-oauth.test.ts` sends `Authorization: Bearer <oauth>` plus `chatgpt-account-id`). OpenClaw uses it only for its browser "Talk" fallback. Not exercised live here. |
| G | Ephemeral client secrets (`/v1/realtime/client_secrets`) | **No evidence** | OpenClaw mints them only from a Platform credential. No GPT-Live client-secret flow is documented (`gpt-live-provider.md` 1.3). |
| H | "Sign in with ChatGPT (Beta)" (SIWC) token-sharing grant | **No** | CONFIRMED (OpenClaw authentication table): "Realtime voice: Not supported by this credential". |

### 3.2 Route C in detail (CONFIRMED in both references)

1. **Create the call:**

   ```http
   POST https://chatgpt.com/backend-api/codex/realtime/calls?intent=quicksilver&architecture=avas
   Authorization: Bearer <chatgpt access_token>
   chatgpt-account-id: <chatgpt_account_id>
   OpenAI-Alpha: quicksilver=v2
   originator: <name>
   session-id: <uuid>
   thread-id: <uuid>
   x-session-id: <uuid>
   Content-Type: application/json

   {"sdp": "<local SDP offer>",
    "session": {"model": "gpt-live-1-codex",
                "instructions": "...",
                "audio": {"output": {"voice": "cove"}},
                "delegation": {"type": "client"},
                "initial_items": [...]}}
   ```

   The **request** body is JSON on this backend; the Platform equivalent
   uses multipart, and Codex has a TODO to align the two. The **response**
   body is the raw SDP answer (Codex `realtime_call.rs:162`,
   `decode_sdp_response`). Codex requires a `Location` header such as
   `/v1/live/rtc_<id>` and takes the call id from it; only OpenClaw falls
   back to an `openai-session-id` header when `Location` is missing.
2. **Media:** audio goes over the negotiated WebRTC track (Opus). OpenClaw
   runs the peer server side with `werift` and `libopus-wasm`, at 24 kHz PCM
   at the edges.
3. **Control:** open a sideband WebSocket at
   `wss://api.openai.com/v1/live/rtc_<id>`, with the **same** bearer token,
   `chatgpt-account-id`, `OpenAI-Alpha: quicksilver=v2` and session headers.
   Codex's comment: "ChatGPT-auth sessions send their bearer plus account id;
   transceiver is responsible for accepting that same call-create identity on
   the direct `api.openai.com` sideband path."
4. **Wire dialect** (OpenClaw `realtime-quicksilver-protocol.ts`,
   `-events.ts`; Codex `protocol_frameless_bidi.rs`). This is **not** the
   public vocabulary in `gpt-live-provider.md`.
   - The client sends:
     - `input_audio.append`, which is only needed without WebRTC media;
     - `session.update`, never `session.start`;
     - `session.context.append`, or `delegation.context.append` with
       `delegation_item_id`, plus `channel: "speakable" | "commentary"` and
       `content: [{type:"input_text", text}]`;
     - `session.close`.
   - The server sends `session.started`/`session.updated`,
     `output_audio.delta` (`audio`), `input_transcript.added`,
     `output_transcript.added` (`item.text`), `turn.done`,
     `delegation.created`, `output_audio_buffer.cleared`, `error`.
5. **Voices:** `gpt-live-1-codex` uses `arbor`, `breeze`, `cove` (the
   default), `ember`, `juniper`, `maple`, `sol`, `spruce` and `vale`. These
   differ from the 31 public voices.
6. **Delegation:** only `{"type":"client"}`, optionally with
   `ack_filler:false` when the host controls input. Neither reference sends
   `{"type":"responses"}` on this route.
7. **Billing:** usage counts against the ChatGPT plan's Codex allowance, not
   Platform billing. **INFERRED** from the backend and the OpenClaw docs; no
   per-minute price is published for this route.

### 3.3 Answers to `gpt-live-provider.md` 2.3 and 2.4

2.3 asks whether a ChatGPT OAuth token is accepted on
`wss://api.openai.com/v1/live/sessions`, and whether it can call the
Responses API. 2.4 asks which delegation mode follows from that.

- **The voice socket as designed: no.** The design targets route A (`gpt-live-1`,
  primary WebSocket, `session.start`, `session.*` events). Route A requires a
  Platform key. Two literal UX targets fail with a ChatGPT login:
  - `yui session --provider openai-live --model gpt-live-1` cannot run on it;
  - a ChatGPT-only `yui session --provider openai-live` cannot pick
    `gpt-live-1` either.
- **The voice socket with the Codex model: yes, through route C.** That means
  a WebRTC peer plus a sideband, and a second wire dialect.
- **The Responses API: yes, but only on the ChatGPT backend**
  (`chatgpt.com/backend-api/codex/responses`, section 2), never on
  `api.openai.com/v1/responses`.
- **Responses delegation (2.4): no simplification.**
  - On route A, hosted Responses delegation runs on the Platform project that
    opened the session. A ChatGPT token cannot open that session.
  - On route C, only client delegation is used.
  - So the "one-credential, low-complexity" Responses option that 2.4
    hopes for is unavailable whenever the credential is ChatGPT.
- **The client-delegation backend: yes, and this is where the ChatGPT token
  pays off.** The client-delegation executor (2.5) needs a turn-based backend
  model. It can call `chatgpt.com/backend-api/codex/responses` with the same
  token (section 2). One credential then covers voice and reasoning, and the
  delegation backend needs no API key. Q8 in `gpt-live-provider.md` ("which
  backend model") gets a natural default: the account's default Codex model.
- **Answer for 2.4:** **client delegation**. `gpt-live-provider.md` had left
  the mode PENDING (2.4 and the PR plan in 2.12); the user has now chosen
  client delegation, and 2.4 records it.

### 3.4 Exchanging the id token for an API key

Codex still runs an RFC 8693 token exchange at the end of a browser login
(CONFIRMED, `obtain_api_key`):

```http
POST https://auth.openai.com/oauth/token
Content-Type: application/x-www-form-urlencoded

grant_type=urn:ietf:params:oauth:grant-type:token-exchange
&client_id=<client id>&requested_token=openai-api-key
&subject_token=<id_token>&subject_token_type=urn:ietf:params:oauth:token-type:id_token
```

- The `access_token` it returns is stored as `OPENAI_API_KEY` in `auth.json`.
- Errors are ignored (`.ok()`). The device-code path never calls it.
- Codex itself does **not** use that key for ChatGPT-mode realtime:
  `CodexAuth::api_key()` returns `None` in ChatGPT mode, and realtime falls
  back to the `OPENAI_API_KEY` env var instead.
- **INFERRED:**
  - The exchange only succeeds when the id token carries a Platform
    `organization_id`/`project_id`, that is, when the user has a Platform org.
    The `needs_setup`/`completed_platform_onboarding` handling on the success
    page points to this.
  - The key it returns is billed to that **Platform org**, not to the ChatGPT
    plan.
  - With that key, route A (`gpt-live-1`) works if the org has GPT-Live
    access (Tier 1 or higher).
- It is therefore a convenience, not a subscription path. It still needs a
  funded Platform org, and it removes copy-pasting a key, nothing more.
- **Decision: not offered.** `yui auth chatgpt` never performs this exchange.

### 3.5 Recommendation (adopted)

1. Keep the single provider name `openai-live` (`gpt-live-provider.md`
   2.2). Admit two
   catalog models:
   - `gpt-live-1` uses route A: an API key, the primary WebSocket, the public
     dialect. This is #630 unchanged.
   - `gpt-live-1-codex` uses route C: the ChatGPT credential, WebRTC plus a
     sideband, the quicksilver-v2 dialect.

   The provider name still selects "OpenAI GPT-Live". The model picks the
   route, as it does in OpenClaw.
2. **Credential resolution for `openai-live`** (the order `gpt-live-provider.md`
   2.3 fixes):
   1. The ChatGPT auth store (`yui auth chatgpt`). With no `--model`, this
      selects `gpt-live-1-codex`.
   2. Otherwise, an API key: `--api-key`, `model.openai.api_key` or
      `OPENAI_API_KEY`. With no `--model`, this selects `gpt-live-1`.
   3. Otherwise, fail with "run `yui auth chatgpt` or set `OPENAI_API_KEY`".

   An explicit `--model` narrows the order to the credential class that model
   accepts:
   - `--model gpt-live-1` skips the ChatGPT store and needs an API key;
   - `--model gpt-live-1-codex` prefers the ChatGPT store. OpenClaw's code
     falls back to a Platform key for this model, creating the WebRTC call
     with multipart at `POST https://api.openai.com/v1/live`. That fallback
     is CONFIRMED in OpenClaw's code; it was not observed against the live
     service.

   With only a ChatGPT login, `--model gpt-live-1` fails fast, before any
   dial, with that message.

   **As built (PR 9):** `--model gpt-live-1-codex` takes only the ChatGPT
   store, with no Platform-key fallback, and the CLI reads the key from
   `--api-key`, `model.openai.api_key` or `AGENT_MODEL__OPENAI__API_KEY`
   (not a bare `OPENAI_API_KEY`). See `gpt-live-provider.md` 2.14.

   OpenClaw uses the opposite order ("A configured Platform credential takes
   precedence over ChatGPT sign-in"). The order here follows
   `gpt-live-provider.md` and the request. It matters only when both
   credentials exist and no model is given.
3. **Delegation is client-side** (the answer to `gpt-live-provider.md` 2.4;
   the design is its 2.5). The backend defaults to
   the Responses-over-ChatGPT client when the credential is ChatGPT, and to
   the configured provider otherwise.
4. **Order of work:**
   1. Login and token store (this phase 2 PR).
   2. `gpt-live-provider.md` PRs 1 to 3 (public route, API key only).
   3. The Responses-over-ChatGPT client, the `openai-chatgpt` provider
      (`gpt-live-provider.md` PR 3a), before the delegation executor (PR 4).
      It also gives `yui ask`/`yui chat` a path with no API key.
   4. A route C transport (`gpt-live-provider.md` PR 9): a pion peer, Opus through `go-audio/pkg/codec`,
      call creation, the sideband, and a codec for the quicksilver-v2 dialect
      behind the same `openailive` session state machine.

### 3.6 How much complexity does this remove?

| Removed or simplified | Added |
| --- | --- |
| No Platform key or billing for voice (route C) or for the delegation backend (section 2) | A pion WebRTC peer on the provider side. The repo already has `pion/webrtc/v4` and a pure-Go Opus encoder and decoder (`go-audio/pkg/codec/opus.go`), so it needs no new dependencies. |
| One login serves `ask`, `chat`, `session` and the delegation backend | A second GPT-Live dialect (quicksilver v2) next to the public one |
| `validateSessionCredential` gains one more credential class; no code is deleted | Token refresh and locking (this PR) |
| | A Responses client (the existing text path is Chat Completions) |

The honest answer is that it removes **operator** complexity (keys and
billing), not code. No existing package becomes deletable. The places that
become simpler are the CLI quick start ("export OPENAI_API_KEY" becomes
`yui auth chatgpt`) and the credential check, which no longer hard-fails for
OpenAI hosts when a ChatGPT login exists.

---

## 4. Design for this repo

### 4.1 Package

Put the core in **`go-llm-gateway/pkg/providers/openai/chatgptauth`**:

- It is vendor-specific OpenAI auth, which is why it sits under the `openai`
  provider tree.
- It is reusable by `openai`, `openailive` and the future Responses client.
  It has no CLI imports, as depguard's `reusable-modules` rule requires.

| File | Contents |
| --- | --- |
| `config.go` | `Config{Issuer, ClientID, Originator, Scope, HTTPClient, Now}`, the defaults (section 1.1), and the endpoint URLs. |
| `pkce.go` | `NewPKCE(io.Reader)`: a 64-byte verifier, base64url, S256. |
| `authorize.go` | `AuthorizeURL(redirectURI, PKCE, state)` |
| `callback.go` | A loopback callback server on `127.0.0.1:1455`, with fallback to 1457. It serves `/auth/callback` (state check, error mapping) and `/cancel`, and returns the code exactly once. |
| `client.go` | `ExchangeCode`, `Refresh`, `Revoke`, `RequestDeviceCode`, `PollDeviceCode`, and the classification of permanent and transient errors. |
| `claims.go` | Unverified JWT payload decoding: account id, email, plan and `exp`. |
| `credential.go` | `Credential{AccessToken, RefreshToken, IDToken, AccountID, Email, PlanType, ExpiresAt, LastRefresh}` |
| `store.go` | `FileStore`: atomic writes with mode 0600 and directory mode 0700, and an OS advisory lock (flock on Unix, LockFileEx on Windows) on `<file>.lock`. |
| `manager.go` | `Manager.Token(ctx)`: load, check expiry against the injected clock, lock, reload, refresh, save. |
| `login.go` | `LoginBrowser` and `LoginDevice` orchestration, with an injected browser opener, output writer, clock and sleeper. |

### 4.2 Token storage

- Path: `<config-dir>/auth/chatgpt.json`. The default `--config-dir` is
  `~/.agent-cli`, which is the existing yui config dir. `~/.codex` is never
  read or written.
- Permissions: the directory is `0700` and the file `0600`. A load fails
  closed if the file is group- or world-readable on Unix.
- Writes: write a temporary file in the same directory, fsync, then rename.
- Format (version 1):

  ```json
  {
    "version": 1,
    "issuer": "https://auth.openai.com",
    "client_id": "app_EMoamEEZ73f0CkXaXp7hrann",
    "account_id": "<chatgpt_account_id>",
    "email": "user@example.com",
    "plan_type": "plus",
    "tokens": {"id_token": "<jwt>", "access_token": "<jwt>", "refresh_token": "<opaque>"},
    "expires_at": "2026-10-02T18:00:00Z",
    "last_refresh": "2026-10-02T12:00:00Z"
  }
  ```

- Windows: file modes are not enforced. The store relies on the ACL it
  inherits from the config directory, which is under the user's profile by
  default. Elsewhere, a pre-existing store directory with looser permissions
  is tightened to `0700`.
- Locking: an OS advisory lock on the sibling file `<file>.lock`, taken
  without blocking: `flock(LOCK_EX|LOCK_NB)` on Unix and `LockFileEx`
  (exclusive, fail-immediately) on Windows, through `golang.org/x/sys`.
  - The operating system releases the lock when its holder exits or
    crashes. There is no stale lock to judge or break, and no window in
    which two processes both hold it. (An earlier directory lock with owner
    tokens and stale-lock breaking was replaced in review: reading the
    staleness and the owner at different times, and the rename-away and
    rename-back steps, let two holders exist at once.)
  - The lock belongs to the open file, so two `Lock` calls in one process
    also exclude each other.
  - The lock file is never deleted. Deleting it would let a waiter that
    holds the old file and a caller that creates a new one both hold "the"
    lock.
  - Waiters poll through an injected sleeper. Inside the lock the manager
  **reloads** the file before refreshing. If another process already rotated
  the token and it is fresh, the manager returns it without refreshing, as
  Codex does.
- Refresh policy:
  - Refresh when `expires_at - now <= 5 min`.
  - If `expires_at` is unknown, use the JWT `exp`. If that is unknown too,
    refresh when `last_refresh` is older than 8 days.
  - A permanent failure returns `ErrReauthRequired` and keeps the file, so
    `status` can explain the problem.
  - A transient failure returns the error. A still-valid access token is
    used without refreshing.
  - Unverified JWT decoding is acceptable. The token is only used as a bearer
    toward OpenAI, and the claims are only used for display and the account
    header.

### 4.3 Commands

| Command | Behaviour |
| --- | --- |
| `yui auth chatgpt` | Browser flow: bind the callback, print the URL, try to open the browser (`open`, `xdg-open` or `rundll32`), wait for the callback, exchange, save. Prints the email, plan and store path. Never prints a token. |
| `yui auth chatgpt --device` | Device-code flow: print the URL and code, then poll. For SSH or headless hosts. |
| `yui auth chatgpt --no-browser` | Browser flow without launching a browser. The user opens the URL by hand, or forwards port 1455 over SSH (`ssh -L 1455:127.0.0.1:1455`). |
| `yui auth status [--json]` | Shows signed in or not, email, account id, plan, access-token expiry, last refresh, the store path, and a warning when the file mode is too open. It does not refresh, so it has no side effects. |
| `yui auth logout` | Best-effort `POST /oauth/revoke` of the refresh token, then delete the file under the lock. A revoke failure is reported, but local deletion still happens. |

**Scopes (checked in the Codex source).** Codex requests
`api.connectors.read api.connectors.invoke` only in its browser authorize URL
(`login/src/server.rs:590`). Nothing in `core` or
`codex-api` checks them for `/backend-api/codex/responses` or for realtime
calls. Codex's device-code flow sends no scope at all and still drives both.
OpenClaw's minimal-scope login drives `gpt-live-1-codex` and Codex
Responses. The connectors scopes are for OpenAI-hosted connectors only, so
the login keeps `openid profile email offline_access`.

The originator is `yui`, following OpenClaw's precedent of sending its own
name rather than impersonating `codex_cli_rs`.

### 4.4 How providers pick up the credential

The repo convention is that **the provider name selects the wire protocol**
(`gpt-live-provider.md` 2.2). An auth mode is therefore a credential source,
not a new provider name:

- `openai-live`: credential resolution as in 3.5. The credential provider of
  `gpt-live-provider.md` 2.3, `func(ctx) (headers map[string]string, err error)`,
  is backed by `chatgptauth.Manager.Credential`. It returns
  `Authorization: Bearer <access_token>` plus `chatgpt-account-id`, and the
  route C headers (`OpenAI-Alpha: quicksilver=v2`, `originator`). The provider
  calls it on every dial, including sideband reconnects, so a refreshed token
  is used without restarting the session. `validateSessionCredential`
  accepts the auth store for `gpt-live-1-codex` and an API key for
  `gpt-live-1` (as built in PR 9, the model picks one credential class; the
  `model.openai.auth` setting below is not built).
- Text inference: the Chat Completions `openai` provider cannot use the token
  (section 2). Add a provider name **`openai-chatgpt`**. It is a different
  wire protocol (Responses SSE) and a different host, which follows the same
  convention. `yui ask --provider openai-chatgpt` and the `livedelegation`
  backend use it. It is `gpt-live-provider.md` PR 3a, with its file list in
  that document's 2.10.
- Config: `model.openai.auth: auto | api_key | chatgpt` (default `auto`)
  selects the source for both. `auto` applies the order in 3.5.

### 4.5 Removable or simpler code

See 3.6. Nothing is removable in phase 2. Later:

- the "export `OPENAI_API_KEY`" requirement in the root help and quick start;
- the API-key-only branch of `validateSessionCredential`;
- the need for a separate delegation-backend key (`gpt-live-provider.md` Q8).

---

## 5. Terms and risks

Stated only as the references state them:

- **Codex:** the README says "We recommend signing into your ChatGPT account
  to use Codex as part of your Plus, Pro, Business, Edu, or Enterprise
  plan". The code is Apache-2.0. Neither the repository nor its docs say
  anything about other applications reusing the Codex OAuth client id. The
  redirect allow-list comment ("Keep in sync with the Codex CLI Hydra
  redirect URI allow-list") shows that the client is registered for Codex
  CLI.
- **OpenClaw:**
  - It describes Codex login as "Your ChatGPT account and workspace using the
    Codex product", with usage drawn from "Your Codex allowance".
  - It separately offers **Sign in with ChatGPT (Beta)** for "app-specific
    authorization": a dynamically registered client (`dynamic_agent_client`
    becomes `oaiapp_*`) with the scopes
    `resource.invoke chatgpt.tokens.use.direct`. It calls
    `api.openai.com/v1/responses`, and the docs require that "Your account
    and workspace must have SIWC registration and token sharing enabled by
    OpenAI". SIWC does **not** support realtime voice.
  - For Anthropic, OpenClaw records an explicit vendor statement ("Anthropic
    staff told us this usage is allowed"). It records no equivalent statement
    for OpenAI Codex login.
- **Operational risks:**
  - OpenAI can change the client id, the redirect allow-list, the originator
    handling or the backend (`chatgpt.com/backend-api` is not a documented
    public API).
  - Workspaces can disable Codex (`missing_codex_entitlement`).
  - The model list is account-dependent, and models are retired
    (OpenClaw's GPT-5.4 retirement note).
  - Usage counts against the user's plan allowance.
  - Rotating refresh tokens punish concurrent refreshes; this design locks.
  - The `gpt-live-1-codex` route uses an `OpenAI-Alpha` header, which signals
    a pre-GA contract that may change without notice.

---

## 6. Phase 2 scope (PR `chatgpt-oauth-login`)

Implemented:

- the `chatgptauth` package (sections 4.1 and 4.2);
- `yui auth chatgpt [--device|--no-browser]`, `yui auth status [--json]` and
  `yui auth logout`.

Tests:

- an `httptest` fake issuer for authorize, token, refresh, revoke and the
  device endpoints;
- an injected clock for expiry and refresh, and an injected sleeper for lock polling;
- an injected sleeper for device polling;
- no real network and no real login.

Not in phase 2:

- provider pickup (4.4);
- the Responses client;
- the route C transport.

## 7. Resolved questions

All five questions this document raised were answered on 2026-10-02 (see
"Decisions" at the top):

1. **Model target:** build the `gpt-live-1-codex` WebRTC route for the
   ChatGPT login; `gpt-live-1` stays on an API key.
2. **Credential precedence:** ChatGPT first, API key as the fallback, with
   `--model` narrowing the order.
3. **Client id:** reuse the Codex public client id.
4. **API-key exchange:** not offered.
5. **Scope:** minimal; the connectors scopes are not needed (4.3).
