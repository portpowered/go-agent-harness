# Capital One Shopping WebMCP adapter

## Scope and safety

The adapter is active only on exact HTTPS pages at
`capitaloneshopping.com` and `www.capitaloneshopping.com`. It reads the offers
feed and scrolls to request more rendered results. It never activates an offer,
clicks through to a merchant, adds anything to a cart, or makes a purchase.

The live feed can virtualize older cards. The scanner therefore collects each
rendered batch before scrolling, retains normalized offers outside the DOM, and
deduplicates repeated observations. A scan stops at the requested bound of
1–20 batches or after two consecutive no-growth cycles. At most 1,000 distinct
offers are retained in page-local state.

## Tools

### `capital_one_shopping_get_context`

Reads the adapter and latest-scan state. It accepts an empty object and returns:

- `origin` and `path`: current page location.
- `ready`: whether the adapter registered its tools.
- `scanning`: whether a scan is currently active.
- `scan_generation`: page-local generation used to bind result paging.
- `summary`: the latest completed scan summary, or `null`.
- `capabilities`: `scan_offers`, `list_matches`, and `reset_scan`.

### `capital_one_shopping_scan_offers`

Scans lazy-loaded offer batches and applies typed reward and cost filters.

| Input | Type | Required | Meaning |
| --- | --- | --- | --- |
| `max_pages` | integer, 1–20 | yes | Maximum rendered batches to inspect. |
| `max_cost_usd` | number, minimum 0 | no | Maximum explicitly observed product cost. |
| `min_cashback_percent` | number, 0–100 | no | Minimum percentage cashback. |
| `min_bonus_usd` | number, minimum 0 | no | Minimum fixed-dollar cashback bonus. |
| `reward_match` | `any` or `all` | no | Combine supplied reward thresholds with OR or AND; defaults to `any`. |
| `unknown_cost_policy` | `separate` or `exclude` | no | Keep reward matches without explicit cost in a separate list or omit them; defaults to `separate`. |

If no reward threshold is supplied, all offers pass the reward portion of the
filter. A supplied maximum cost admits only offers whose explicit `cost_usd` is
at or below that value. Offers with no explicit cost follow
`unknown_cost_policy`.

The result includes:

- `scan_generation`, `pages_requested`, `pages_scanned`, and `load_cycles`.
- `stop_reason`: `max_pages` or `no_growth`.
- `offers_observed` and `duplicate_observations`.
- `match_count` and `unknown_cost_match_count`.
- `matches` and `unknown_cost_matches`, each initially limited to 50 offers.
- `result_limit` and a model-facing `next_step`.

### `capital_one_shopping_list_matches`

Reads a bounded page from the latest completed scan.

| Input | Type | Required | Meaning |
| --- | --- | --- | --- |
| `scan_generation` | integer, minimum 1 | yes | Must equal the generation returned by the latest scan. |
| `kind` | `matched` or `unknown_cost` | no | Select the result collection; defaults to `matched`. |
| `offset` | integer, minimum 0 | no | Zero-based result offset; defaults to 0. |
| `limit` | integer, 1–50 | no | Maximum offers returned; defaults to 50. |

The result repeats `scan_generation`, `kind`, `offset`, and `limit`, and returns
`total` plus the requested `offers` slice. Starting a new scan or resetting
state makes older generations stale.

### `capital_one_shopping_reset_scan`

Accepts an empty object, clears page-local collected offers and results, and
advances `scan_generation`. It does not change the shopping account or the
website. Reset returns `scan_in_progress` while a scan is active.

## Offer data

Every item in `matches`, `unknown_cost_matches`, or paged `offers` has this
shape:

| Field | Type | Meaning |
| --- | --- | --- |
| `offer_id` | string | Stable hash of the normalized offer contents for the current page data. |
| `merchant` | string | Merchant label observed on the offer card. |
| `description` | string | Bounded, normalized card description used as grounded evidence. |
| `cashback_percent` | number or `null` | Percentage from text such as `70% back`. |
| `bonus_usd` | number or `null` | Fixed cashback from text such as `$300 back`. |
| `reward_cap_usd` | number or `null` | Reward ceiling from text such as `($1,000 max)`. |
| `qualifying_spend_usd` | number or `null` | Required spend from text such as `when you spend $100`. |
| `cost_usd` | number or `null` | Product price only when exposed through an explicit price element. |
| `url` | string | Same-site offer URL when available; otherwise the current Capital One Shopping URL. |

Reward caps and qualifying-spend amounts are deliberately separate from
`bonus_usd` and `cost_usd`. In particular, `$1,000 max` is not a $1,000 bonus,
and `when you spend $100` is not a $100 product cost. Most homepage merchant
offers do not expose a product price, so `cost_usd: null` is expected and must
not be guessed.

## Errors and page lifecycle

Adapter-level errors include:

- `invalid_input`: an argument violates the typed bounds or enum values.
- `scan_in_progress`: another scan is active, or reset was requested during it.
- `stale_result`: `list_matches` received an old or nonexistent generation.

The WebMCP broker can additionally return `stale_tool_ref`, `page_navigated`,
or a disconnected-page error. The homepage may navigate late during startup.
The model should list eligible tabs, select the Capital One Shopping page,
refresh the tool catalog, and retry against the stable generation.

## Direct CLI invocation without a model

Direct invocation is supported through `yui webmcp`. It does not require an
OpenAI API key or create a model session. The direct commands attach to an
already-running Chrome DevTools endpoint; they do not accept
`--browser-open`, so open Capital One Shopping in Chrome before selecting and
invoking its page tools.

### 1. Start Chrome and open the site

For example, on macOS, run this in one terminal and leave it running:

```bash
DIRECT_PROFILE="$(mktemp -d)"

"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
  --remote-debugging-port=9222 \
  --user-data-dir="$DIRECT_PROFILE" \
  https://capitaloneshopping.com/
```

Use a dedicated profile rather than a normal personal Chrome profile. The
example endpoint is loopback-only. If port 9222 is already occupied, choose a
different free port in both this command and `CDP_URL` below.

### 2. Select the exact site tab and inspect its tools

Run the remaining commands from the repository root in another terminal:

```bash
YUI=./agent-cli/bin/yui
CDP_URL=http://127.0.0.1:9222
DIRECT_CONFIG="$(mktemp -d)"

"$YUI" --config-dir "$DIRECT_CONFIG" webmcp tabs \
  --cdp-url "$CDP_URL" \
  --allowed-origin https://capitaloneshopping.com \
  --allowed-origin https://www.capitaloneshopping.com \
  --eligible \
  --json

"$YUI" --config-dir "$DIRECT_CONFIG" webmcp select \
  --cdp-url "$CDP_URL" \
  --allowed-origin https://capitaloneshopping.com \
  --allowed-origin https://www.capitaloneshopping.com \
  --auto-select single \
  --activate \
  --persist-selection \
  --json

"$YUI" --config-dir "$DIRECT_CONFIG" webmcp tools \
  --cdp-url "$CDP_URL" \
  --auto-select persisted \
  --allowed-origin https://capitaloneshopping.com \
  --allowed-origin https://www.capitaloneshopping.com \
  --name-contains capital_one_shopping_ \
  --refresh \
  --json
```

`--auto-select single` succeeds only when exactly one eligible tab matches the
origin. If multiple Capital One Shopping tabs are open, read `browser_id` and
`target_id` from `webmcp tabs`, then replace `--auto-select single` on the
`select` command with `--browser <browser_id> --tab <target_id>`.

The site can perform a late navigation during startup. If `tools` reports a
stale selection or no catalog, wait for the page to settle and repeat the
`select` and `tools --refresh` commands.

### 3. Invoke the scan directly

The scan fields are simple scalars, so the CLI's positional `key=value` form
can invoke the unique tool name directly:

```bash
PAGES=20
MAX_COST_USD=500
MIN_CASHBACK_PERCENT=70
MIN_BONUS_USD=300

"$YUI" --config-dir "$DIRECT_CONFIG" webmcp invoke \
  capital_one_shopping_scan_offers \
  max_pages="$PAGES" \
  max_cost_usd="$MAX_COST_USD" \
  min_cashback_percent="$MIN_CASHBACK_PERCENT" \
  min_bonus_usd="$MIN_BONUS_USD" \
  reward_match=any \
  unknown_cost_policy=separate \
  --cdp-url "$CDP_URL" \
  --auto-select persisted \
  --allowed-origin https://capitaloneshopping.com \
  --allowed-origin https://www.capitaloneshopping.com \
  --reason "read-only Capital One Shopping offer scan" \
  --timeout 90s \
  --invocation-timeout 90s \
  --json \
  >capital-one-scan.json \
  2>capital-one-scan.receipt.json
```

The dispatch receipt on stderr contains the invocation ID. Standard output is
one final `webmcp.tool-result.v1` envelope; the adapter result is under
`.data.output`. For example:

```bash
jq '.data.output.data | {
  pages_scanned,
  offers_observed,
  match_count,
  unknown_cost_match_count,
  matches,
  unknown_cost_matches
}' capital-one-scan.json
```

`--input-json` is equivalent and is preferable for programmatic callers:

```bash
"$YUI" --config-dir "$DIRECT_CONFIG" webmcp invoke \
  capital_one_shopping_scan_offers \
  --input-json '{"max_pages":20,"max_cost_usd":500,"min_cashback_percent":70,"min_bonus_usd":300,"reward_match":"any","unknown_cost_policy":"separate"}' \
  --cdp-url "$CDP_URL" \
  --auto-select persisted \
  --allowed-origin https://capitaloneshopping.com \
  --allowed-origin https://www.capitaloneshopping.com \
  --timeout 90s \
  --invocation-timeout 90s \
  --json
```

The unique tool name is resolved to its generation-bound tool reference at
invocation time. A caller may instead copy `ref` from `webmcp tools` and use
`--tool-ref <ref>`, but that reference becomes stale after page navigation.

### 4. Page or reset the direct results

The scan generation is nested in the successful direct result. Use it to read
more than the first 50 results:

```bash
SCAN_GENERATION="$(jq -r '.data.output.data.scan_generation' capital-one-scan.json)"

"$YUI" --config-dir "$DIRECT_CONFIG" webmcp invoke \
  capital_one_shopping_list_matches \
  scan_generation="$SCAN_GENERATION" \
  kind=unknown_cost \
  offset=50 \
  limit=50 \
  --cdp-url "$CDP_URL" \
  --auto-select persisted \
  --allowed-origin https://capitaloneshopping.com \
  --allowed-origin https://www.capitaloneshopping.com \
  --timeout 30s \
  --json
```

Reset the page-local scan state with:

```bash
"$YUI" --config-dir "$DIRECT_CONFIG" webmcp invoke \
  capital_one_shopping_reset_scan \
  --input-json '{}' \
  --cdp-url "$CDP_URL" \
  --auto-select persisted \
  --allowed-origin https://capitaloneshopping.com \
  --allowed-origin https://www.capitaloneshopping.com \
  --timeout 30s \
  --json
```

## Manual model-driven trigger

Build the CLI, set the scan controls and API key, then run:

```bash
PAGES=20
MAX_COST_USD=500
MIN_CASHBACK_PERCENT=70
MIN_BONUS_USD=300
KEY="$(tr -d '\n' < ~/.you-agent-factory/secrets/OPENAPI_API_KEY)"

./agent-cli/bin/yui session \
  --provider openai \
  --model gpt-realtime-2.1 \
  --api-key "$KEY" \
  --prompt "Scan Capital One Shopping using max_pages=$PAGES, max_cost_usd=$MAX_COST_USD, min_cashback_percent=$MIN_CASHBACK_PERCENT, min_bonus_usd=$MIN_BONUS_USD, reward_match=any, and unknown_cost_policy=separate. If a tool reference is stale, no page is connected, or the page navigates, call webmcp_list_tabs, select the one eligible Capital One Shopping tab, call webmcp_list_tools, and retry capital_one_shopping_scan_offers once the page is stable. Report offers whose cashback is at least $MIN_CASHBACK_PERCENT percent OR whose fixed cash bonus is at least $MIN_BONUS_USD dollars. Do not interpret reward caps or qualifying spend as product cost. Clearly separate offers whose cost is unknown." \
  --browser-tools webmcp \
  --browser-open https://capitaloneshopping.com/ \
  --browser-auto-select off \
  --browser-allowed-origin https://capitaloneshopping.com \
  --browser-allowed-origin https://www.capitaloneshopping.com \
  --browser-approval never \
  --browser-invocation-timeout 90s \
  --browser-close-on-exit=false \
  --no-terminal-tools \
  --max-duration 60s
```

Add `--browser-record --browser-record-arguments --browser-record-results`,
`--record <provider-capture.json>`, and `--record-dir <recording-directory>`
when an auditable acceptance artifact is required.

## Tests

Run the ordinary adapter regressions:

```bash
go test ./agent-cli/internal/webmcp/siteadapter \
  ./agent-cli/internal/webmcp/chrome -count=1
```

Run the credential-free stock-Chrome journey. Its fixture covers lazy loading,
virtualization, deduplication, known and unknown costs, reward caps, qualifying
spend, bounded paging, stale generations, reset, and invalid inputs:

```bash
WEBMCP_CAPITAL_ONE_SHOPPING_ADAPTER_INTEGRATION=1 \
  go test ./agent-cli/internal/webmcp/chrome \
  -run '^TestCapitalOneShoppingAdapterStockChromeJourney$' -count=1 -v
```

Run the opt-in read-only live-site gate against a temporary Chrome profile. It
does not require model credentials and performs a bounded 20-page scan without
activating an offer:

```bash
WEBMCP_CAPITAL_ONE_SHOPPING_LIVE=1 \
  go test ./agent-cli/internal/webmcp/chrome \
  -run '^TestCapitalOneShoppingAdapterLive$' -count=1 -v
```

Live offers change over time. Acceptance should assert the requested scan
bound, structured reward/cost semantics, successful tool lifecycle, and
grounded reporting rather than a permanent merchant list.
