# X WebMCP adapter

## Scope and safety

The adapter is active only on exact HTTPS pages at `x.com`, `www.x.com`,
`twitter.com`, and `www.twitter.com`. It uses the account already signed in to
the selected Chrome profile; it never receives or stores an X password or API
token.

Publishing is deliberately split into two calls. `x_prepare_post` writes the
exact text into X's visible composer without submitting it. `x_publish_post`
then requires the returned one-use draft token, the identical text, and
`confirm=true`. A changed composer, changed text, stale token, missing
confirmation, or second use fails closed. The token is consumed before the
Post control is clicked so an uncertain site response cannot be retried into a
duplicate post.

Text posts are bounded to 280 characters. The experimental video path stages
one MP4 of at most 64 MiB (a harness limit, not X's account limit). It does not
create threads, quote posts, reply, repost, follow accounts, or send direct
messages. See [selfiepostx acceptance status](../../selfiepostx/README.md):
fixture tests and live X video preparation/preview pass, but actual publishing
has not yet passed acceptance with an approved clip.

## Tools

### `x_get_context`

Reads the current route, whether the signed-in controls are present, the
observed account handle when available, and whether a draft is prepared.

### `x_prepare_post`

Accepts `{ "text": "..." }`. It opens the X composer if necessary, refuses
unrelated existing text or media, writes the exact normalized text, verifies that X retained it,
and returns `draft_generation`, `draft_token`, `text`, `character_count`, and
`published: false`.

### `x_publish_post`

Accepts:

```json
{
  "draft_token": "the token returned by x_prepare_post",
  "text": "the identical prepared text",
  "confirm": true
}
```

This call creates an externally visible post. Success returns
`published: true`, the posted text, the account handle when observable, and a
UI verification signal. `publish_status_unknown` means the Post control was
activated but X did not expose completion; the caller must inspect the account
and must not automatically retry.

### `x_clear_draft`

Accepts the latest `draft_token`, clears that exact visible draft, and returns
`published: false`. It cannot clear an unrelated or subsequently edited draft.

## Direct CLI invocation

### Experimental video preparation

After selecting the intended X tab, use PowerShell:

```powershell
& $Yui --config-dir $ConfigDir webmcp x-prepare-video `
  --file ./post.mp4 --text 'Reviewed caption. AI-generated video and voice.' `
  --account '@yourhandle' --cdp-url http://127.0.0.1:9222 `
  --auto-select persisted --allowed-origin https://x.com --json `
  > prepare.json 2> transfer-receipt.json
```

Quote the `@handle` in PowerShell. The command reads a bounded local snapshot,
checks an MP4 header, hashes it, and streams 32-KiB chunks through the selected
broker. It does not upload through an undocumented X HTTP endpoint. The page
reconstructs a File, verifies SHA-256, and sends it to the normal composer input.
X still owns format validation and processing; an MP4 header alone is not a
codec or safety validation. The default total command deadline is five minutes.
The CLI holds a bounded, target-scoped Chrome focus override during preparation:
otherwise occluded/background pages may defer media metadata loading indefinitely.
Normal focus behavior is restored on completion or failure. Simply activating
a tab is insufficient when Chrome still reports it as hidden. Low-level callers
using the chunk tools directly must arrange a genuinely visible page or equivalent
bounded focus management themselves.

The underlying tools are `x_begin_video_upload` (filename, size, SHA-256,
expected account), `x_append_video_chunk` (token, byte offset, base64),
`x_prepare_video_post` (token, caption), and `x_cancel_video_upload` (token).
Transfers expire after ten minutes. Repeating prepare while `video_processing`
is true checks the same attachment; it does not upload again. Stale, reordered,
oversized, wrong-hash, changed-account, and changed-draft inputs fail closed.

The transfer receipt on stderr allows inspection/cancellation after an error.
Cancellation frees transfer state but preserves already attached media. Video
draft removal is manual; `x_clear_draft` returns `manual_clear_required` rather
than claiming to remove a video. Do not automatically retry uncertain uploads
or publishes. A publish token is returned only after a video preview and enabled
Post control exist with no upload indicator. Publishing still requires the
separate `x_publish_post` call below; its token binds the composer, text, account,
and observed media. Reloading the page invalidates tokens.

### Text preparation and explicit publication

Build `yui`, start Chrome with a dedicated DevTools-enabled profile in which
the intended X account is already signed in, and open `https://x.com/home`.
Then select that exact tab as described in the general WebMCP CLI docs.

Prepare the post without publishing:

```bash
YUI=./agent-cli/bin/yui
CDP_URL=http://127.0.0.1:9222
DIRECT_CONFIG="$(mktemp -d)"
POST_TEXT='this is a test of the webmcp connection'

"$YUI" --config-dir "$DIRECT_CONFIG" webmcp invoke \
  x_prepare_post \
  --input-json "$(jq -cn --arg text "$POST_TEXT" '{text:$text}')" \
  --cdp-url "$CDP_URL" \
  --auto-select persisted \
  --allowed-origin https://x.com \
  --allowed-origin https://www.x.com \
  --allowed-origin https://twitter.com \
  --allowed-origin https://www.twitter.com \
  --reason "prepare exact X post for review" \
  --json >x-prepare.json 2>x-prepare.receipt.json
```

Review `.data.output.data.text`, then publish that exact draft:

```bash
DRAFT_TOKEN="$(jq -r '.data.output.data.draft_token' x-prepare.json)"

"$YUI" --config-dir "$DIRECT_CONFIG" webmcp invoke \
  x_publish_post \
  --input-json "$(jq -cn --arg token "$DRAFT_TOKEN" --arg text "$POST_TEXT" \
    '{draft_token:$token,text:$text,confirm:true}')" \
  --cdp-url "$CDP_URL" \
  --auto-select persisted \
  --allowed-origin https://x.com \
  --allowed-origin https://www.x.com \
  --allowed-origin https://twitter.com \
  --allowed-origin https://www.twitter.com \
  --reason "publish reviewed exact X post" \
  --json >x-publish.json 2>x-publish.receipt.json
```

`--auto-select persisted` assumes `yui webmcp select --persist-selection` was
already run against the intended X tab using the same config directory. The
direct path requires no language model or OpenAI API key.

## Verification

Run the regression packages:

```bash
go test ./agent-cli/internal/webmcp/siteadapter \
  ./agent-cli/internal/webmcp/chrome
```

Run the credential-free stock-Chrome integration journey:

```bash
WEBMCP_X_ADAPTER_INTEGRATION=1 \
  go test ./agent-cli/internal/webmcp/chrome \
  -run '^TestXAdapterStockChromeJourney$' -count=1 -v
```

The fixture verifies signed-in context, exact text preparation, confirmation
and text mismatch failures, one successful publish, duplicate-token rejection,
and clearing a later draft. A live acceptance run must use a human-authorized
post body and intended signed-in account; never use arbitrary generated text.
