# The web panel

Core serves the web panel at `/panel/` while the management API is on. Sign in with pluxy's
**Management key**: `pluxy key management --show-key`, run on the computer pluxy runs on, prints it.
With **Remember on this device** ticked, the browser keeps the key after it restarts; without it,
the key is gone when the tab closes.

To open the panel from another computer, set `listen` to an address that computer can reach and
`management_allow_remote` to `true` ([config.md](config.md#top-level)), then restart pluxy. Over
plain HTTP the management key crosses the network unencrypted: turn on [`tls`](config.md#tls) or put
HTTPS in front of pluxy.

The desktop app shows the same pages, and adds its own ([app.md](app.md)).

Nothing in the panel spends from an account behind a click except **Send a test request** on an
account's page and in Setup's third step; both state that the test uses the account before you press
them.

## Navigation

The frame has five destinations in its navigation (**Home**, **Accounts**, **Activity**, **Tools**,
and **Settings** at the foot), a header strip for the page title, destination tabs and main actions,
and a status block at the foot of the navigation.

- **Status block:** shows whether pluxy answers (**Checking**, **Running**, or **Not answering**)
  and its host. While the tab is visible and signed in, it checks pluxy every 15 seconds. Selecting
  it opens a menu with pluxy's full address and **Copy**.
- **Names hidden:** appears at the end of the header strip whenever **Hide names** is turned on in
  **General**; pressing it turns hiding off.
- **Safe address:** adding `?safe` to the panel URL (for example,
  `http://127.0.0.1:8317/panel/?safe`) turns off custom CSS and keeps it off until you re-enable
  **Custom CSS** on **Themes**.

| Destination | Page, tab or topic | Address | `data-page` | What it holds |
| --- | --- | --- | --- | --- |
| **Home** | **Home** | `#/status` | `status` | Service verdict, accounts needing attention, provider pools, connection details, 7-day summary and latest requests |
| **Accounts** | **All accounts** (tab) | `#/accounts`, `#/quota` | `accounts` | Every account grouped by condition, provider filter, list or card view, search and batch actions |
| **Accounts** | An account's page (under **All accounts**) | `#/accounts/<id>` | `account` | One account's limits, facts, weekly usage, settings, models and test request |
| **Accounts** | **Add an account** (under **All accounts**) | `#/add-account` | `add-account` | Provider picker, sign-in and API-key flows, and CLIProxyAPI auth-file import |
| **Accounts** | **Order & fallback** (tab) | `#/routing` | `routing` | Per-provider account priority and weight, model fallback chains and routing rules |
| **Activity** | **Requests** (tab) | `#/activity`, `#/usage` | `usage` | Recent requests kept in memory, filters, routing explanation and CSV export |
| **Activity** | **Usage** (tab) | `#/activity/usage` | `activity/usage` | Request, success-rate, token and latency totals, chart and breakdowns over 24 hours, 7 days or 30 days |
| **Tools** | **Tools** | `#/tools` | `tools` | Detected coding tools, connect and disconnect, setup snippets, address for tools and client keys |
| **Settings** | **General** (topic, **This device**) | `#/settings` | `settings` | Appearance, **Hide names** and **Sign out** |
| **Settings** | **Themes** (topic, **This device**) | `#/themes` | `themes` | Active theme, layout overrides, adding themes and custom CSS |
| **Settings** | **Config** (topic, **pluxy**) | `#/config`, `#/config/source` | `config` | Live network, remote-access, logging and management-key settings, plus the raw settings file editor |
| **Settings** | **Extensions** (topic, **pluxy**) | `#/extensions` | `extensions` | Installed provider extensions, extension settings forms and available packages |
| **Settings** | **Logs** (topic, **pluxy**) | `#/logs`, `#/logs/<trace ID>` | `logs` | Core's log file and full logs of failed requests |
| **Settings** | **System** (topic, **pluxy**) | `#/system` | `system` | Version, build info, served models and base URLs by API format |
| **Settings** | **Setup** (topic, **pluxy**) | `#/setup` | `setup` | Three-step guided setup and CLIProxyAPI setup import |
| **Settings** | Extension pages (topics, **From extensions**) | `#/ext/<publisher>/<name>/<page>` | `ext/<publisher>/<name>/<page>` | Sandboxed pages provided by running extensions |

Settings opens on **General** and groups its topics into **This device** (**General**, **Themes**),
**pluxy** (**Config**, **Extensions**, **Logs**, **System**, **Setup**) and **From extensions**.

## Home

- **Address:** `#/status` (`data-page="status"`).
- **Refresh:** reads accounts, status, 7-day usage and the latest 10 requests every 60 seconds while
  the tab is visible. The header shows `Updated {when}` and **Refresh** (which also forces fresh
  quota reads for shown accounts). If a refresh fails over existing data, a warning notice names
  when the shown data was read and offers **Try again**.
- **Empty state (no accounts):** shows **pluxy has no accounts yet.** with **Add an account**
  (`#/add-account`), **Guided setup** (`#/setup`), provider quick links (**Claude**, **ChatGPT
  (Codex)**, **Gemini**, **Another service with an API key**, and **More**), a line listing detected
  coding tools (or `Next: connect a coding tool.`), and the **Point a tool at pluxy** card.
- **Verdict strip and notices:** shows the current verdict headline and note — in priority order:
  safe mode, accounts needing you, provider status unread, nothing serving (with when the first
  waiting account returns), most 7-day requests failed, accounts waiting, or everything serving —
  plus a counts line (waiting, serving, not checked, turned off, and 7-day request total and success
  rate) and a proportional state bar. When core refused the latest settings-file reload, a warning
  notice gives the error and links to **Open Config** (`#/config`).
- **Needs you band:** lists every account requiring action (**Needs sign-in**, **Closed by
  provider**, **Cannot be read**, **Provider stopped**) with its inline fix (**Sign in again**,
  **See what to do**, **Open Extensions**, or **Turn on**).
- **Pools:** one card per provider that has accounts. Each card shows the provider mark and name,
  the shortest shared request window (**5-hour**, **Daily**, **Weekly**, **Monthly**, or window
  label), the average remaining percentage across serving accounts (or summed USD **Balance** when
  no window is reported, or `Not reported by {provider}` / `Nothing is serving`), a segmented bar
  with one segment per account, `{serving} of {total} serving`, and the soonest reset gain
  (`+{gain}% back in {duration}`) or return time.
- **Accounts:** lists the remaining accounts grouped by provider (ordered waiting soonest-back
  first, then serving least-left first, then unchecked, then turned off), with a link to **All
  accounts** (`#/accounts`). Each row shows the account's condition, plan pill, two shortest request
  limits (read for up to 24 accounts per view), and inline fix (**Turn on** or **Clear cooldown**).
- **Point a tool at pluxy:** links to **Tools** (`#/tools`), switches between **OpenAI** (`/v1`),
  **Claude** (origin) and **Gemini** (origin) to display the matching **Base URL** with **Copy**,
  shows the first client key's name and masked value with a **Keys** link to **Tools**, and lists
  detected coding tools as chips linking to **Tools**.
- **Last 7 days:** links to **Usage** (`#/activity/usage`). Displays request count, **went through**
  percentage, **tokens** (when reported), and a 7 local-day stacked bar chart (succeeded and failed,
  with hover/focus readout). When at least 3 requests failed and more than half asked for one model,
  a line names that model with a **See them** link to filtered **Requests**. If `usage.enabled` is
  off, shows a note with **Turn it on in Config** (`#/config/logging`); if 0 requests ran in 7 days,
  shows whether detected coding tools are connected, with a **Connect it** / **Connect a tool** link
  to **Tools**.
- **Latest requests:** links to **All requests** (`#/activity`) and lists up to 10 compact request
  rows showing time, provider mark, model, account, and result (check mark, failure chip, or backup
  try chip such as **2nd**). Selecting any row opens that request on **Requests**
  (`#/activity?trace=<id>`).
- **Spends or deletes:** nothing.
- **Settings read:** `usage.enabled` ([config.md](config.md#usage)).

## Accounts

- **Address:** `#/accounts` (also `#/quota`; `data-page="accounts"`), tab **All accounts**.
- **Empty state:** when core has no accounts, shows **No accounts yet.** and **Add an account**
  (`#/add-account`).
- **Summary and provider filter:** above the list, a counts line (total accounts and provider count,
  or one provider's account count when filtered, plus non-zero counts for accounts needing you,
  serving, waiting, not checked, and turned off) sits over a proportional state bar. When accounts
  span 2 or more providers, **Filter by provider** chips (**All** and one chip per provider with its
  count) filter the summary, state bar, and groups to one provider and show that provider's pool
  card above the groups (when provider status is checked).
- **Listing, grouping and sorting:** groups matching accounts by condition in order: **Needs you**,
  **Waiting**, **Serving**, and **Turned off**, each heading showing its count. When core's provider
  status could not be read on initial load, accounts whose condition depends on whether their
  provider runs appear under **Not checked** (sorted by name, then ID) in place of **Waiting** and
  **Serving**. **Needs you** and **Waiting** sort by soonest return time (accounts without a return
  time last), then name, then ID; **Serving** and **Turned off** sort by name, then ID.
  - Each row (or card) shows the provider mark with condition dot, account name (linking to
    `#/accounts/<id>`), condition word and reason (or provider name and `base_url` host when simply
    serving; provider name is omitted when filtered to one provider), plan pill, two shortest
    request limits (read for up to 24 serving or waiting accounts per view, cached 5 minutes), 7-day
    request count (`{count} this week`, omitted if usage could not be read), and inline fix.
  - At most one filled primary button appears on the page: the first repair action in **Needs you**,
    or **Add an account** when no repair is needed.
- **Refresh:** polls accounts, provider status, and 7-day usage every 10 seconds while visible
  (paused while **Select** is active or a batch action runs). If a refresh fails over existing data,
  or provider status or 7-day usage could not be read, a warning notice keeps the last loaded rows
  and offers **Try again**.
- **Actions:**
  - **Add an account:** opens `#/add-account`.
  - **Filter by provider** (when 2 or more providers have accounts): **All** or one provider chip.
  - **Find an account** (shown when more than 6 accounts exist, or while a search is entered):
    filters by account label, ID, public ID, provider name, extension ID, condition word, or last
    error (case-insensitive). Offers **Clear search** when nothing matches.
  - **Account view** (**List** / **Cards**): switches this page between rows and cards. Starts from
    the active theme's **Lists** layout and remembers a page-only choice in browser storage
    (`pluxy.accounts.view`) without changing **Themes**.
  - Inline row fixes: **Sign in again** (`#/accounts/<id>/sign-in`), **See what to do**
    (`#/accounts/<id>`), **Open Extensions** (`#/extensions`), **Turn on**, **Clear cooldown**.
  - **Select** (shown when 2 or more accounts exist): adds per-row checkboxes and a bottom batch bar
    (**Actions for the selected accounts**) showing `{count} selected` (or
    `{count} selected, {hidden} hidden` when search or the provider filter hides selected accounts),
    **Select all** / **Clear** (for visible accounts), **Turn on**, **Turn off**, **Clear
    cooldown**, **Remove…**, and **Done**.
- **Spends or deletes:** **Remove…** opens a confirmation dialog (**Remove** / **Cancel**), noting
  if search or the provider filter hides selected accounts, before deleting saved sign-ins from
  core's credential store. Accounts defined in the settings file are skipped by batch **Turn off**
  and **Remove…** (reported in a notice); batch **Turn on** and **Clear cooldown** still run on
  them. Nothing spends from an account.
- **Settings read:** `usage.enabled` (for 7-day counts; [config.md](config.md#usage)); the active
  theme's **Lists** layout (default view before `pluxy.accounts.view` is set in browser storage).

## An account's page

- **Address:** `#/accounts/<id>` (`data-page="account"`). `#/accounts/<id>/sign-in` opens **Sign in
  again** on the page; `#/accounts/<id>?alias=<name>` opens **Edit settings** with a new alias row
  prefilled with `<name>`.
- **What it shows:**
  - Opening header: provider and plan pill, condition and reason, last error (when failing or
    waiting), and the one fix action (**Sign in again**, **Turn on**, **Clear cooldown**, **Open
    Extensions**, or **Open the settings file** for config-file accounts).
  - **Limits:** every limit reported by the provider, **Manual resets** remaining, **Subscription
    ends** date, read timestamp, and **Refresh**.
  - **About:** **Provider**, **Signs in with**, **Sign-in** (auto-renewal status, renewal error or
    expiry), **Order** (links to **Order & fallback**), **Right now** (active request count),
    **Added** date, and **Known to tools as** (public ID).
  - **This week** and **Latest requests:** 7-day request total, success percentage, top 5 models
    with **See its requests** (`#/activity?account=<id>`), and up to 6 expandable recent requests.
  - **Change it:** for accounts added by sign-in, offers **Rename**, **Turn off** (when enabled and
    not closed), **Edit settings**, and **Remove…** (unreadable accounts offer only **Remove…**).
    For config-file accounts, links to **Open the settings file** (`#/config/source?account=<id>`).
  - **Settings, models and a test request** (expandable): read-only **Account settings**, **Models
    it serves** (with aliases), **Test with** model selector and **Send a test request**, and
    **Renew sign-in now** (for refreshable sign-in accounts).
- **Actions:**
  - **Rename:** dialog changing the account's display label (up to 64 characters; blank clears it).
  - **Edit settings:** dialog editing `prefix`, `proxy_url`, `priority`, `weight`,
    `disable_cooling`, `websockets` (Codex accounts only), `request_retry` (**Inherit** or
    **Custom**), `max_concurrency`, `models`, `excluded_models`, `headers`, `model_alias`, and
    extension `settings` JSON; asks before discarding unsaved edits (**Discard changes** / **Keep
    editing**).
  - **Send a test request:** sends one live request through this account using the chosen model (or
    **The first model it serves**) and shows whether it succeeded, the model served, latency, error,
    and reply preview.
  - **Renew sign-in now:** triggers an immediate credential refresh.
  - **Sign in again** (`#/accounts/<id>/sign-in`): replaces the credential using the provider's
    sign-in methods; keeps non-secret settings optional and refuses a sign-in to a different
    upstream account.
- **Spends or deletes:** **Send a test request** spends one request against this account (marked
  `Uses one request.`). **Remove…** asks for confirmation (**Remove**) before deleting the saved
  sign-in and returning to **Accounts**.
- **Settings keys:** account fields under `extensions.<id>.accounts[]`
  ([config.md](config.md#accounts)).

## Add an account

- **Address:** `#/add-account` (`data-page="add-account"`).
- **Empty state:** when no installed provider offers sign-in methods, shows a notice and **Open
  Extensions** (`#/extensions`).
- **Pick a provider** (skipped when only one provider runs): the page asks **Which account do
  you want to add?** with up to 4 main tiles (**Claude**, **ChatGPT (Codex)**, **Gemini**, **Another
  service with an API key**, filled from other running providers up to 4). Any remaining running
  providers appear under **More providers** (`{count} more`).
- **Pick a way to add it:** the page says **Choose how to add it.** and shows **All providers** to
  go back, the provider heading, and each login method the extension offers (browser sign-in, device
  code, or API key). When a provider offers two or more methods, the first browser sign-in is marked
  **Recommended**. Press **Start** on a method.
- **Sign-in flow:**
  - Methods requiring upfront inputs show required fields first and optional fields under **More
    options**. Every secret field provides **Load from file** (reads UTF-8 text up to 1 MiB). Press
    **Continue** to begin, or **Cancel** to go back.
  - While waiting: shows provider instructions, **Open sign-in page** and **Copy link** (browser
    sign-in), the device code and **Copy code** (device flow), a countdown timer when `expires_in`
    is set, and any follow-up input (such as **Paste the address you land on**) with **Continue**.
  - **Cancel** cancels the in-flight sign-in (**Cancelling…** while waiting for core) and checks
    whether the sign-in finished before returning to the method list; leaving or closing the page
    also cancels a pending sign-in. On failure, shows the reason and **Try again**.
- **After adding:** shows the added account name (and plan pill if known), polls core's automatic
  provider check every second (up to 10 times) until it reports success or failure, and offers:
  - **Name** and **Save name:** optional display label (up to 64 characters). Prefills the
    provider-supplied label or the host from `base_url`; when **Hide names** is on, starts empty
    with the masked name as placeholder.
  - **Open account** (`#/accounts/<id>`) and **Add another**.
- **Or bring them from CLIProxyAPI:** **Import files…** picks one or more `.json` auth files (up to
  1 MiB each), imports each one, and lists added accounts, ignored unsupported settings, or skip
  reasons until you leave the page.
- **Spends or deletes:** nothing.

## Order & fallback

- **Address:** `#/routing` (`data-page="routing"`), tab **Order & fallback**.
- **Header action:** **Refresh** reloads accounts, routing rules, fallback chains, served models,
  config format and status.
- **Account order:** shows one section per provider that has two or more accounts, grouped by
  priority (**First**, **Backup 1**, **Backup 2**, …; providers with one account appear in a summary
  line below).
  - Move sign-in accounts up or down a group with **↑** / **↓**, or drag a row onto a group or the
    top/bottom drop edges; each move saves immediately.
  - Config-file accounts cannot be moved here and link to **In the settings file**
    (`#/config/source?account=<id>`).
  - When **How a group shares requests** is set to **Split by weight**, each sign-in account shows
    **Weight {n}**; pressing it opens an inline integer weight input (`0` excludes it, empty resets
    to `1`, saves on Enter or blur).
- **Fallbacks:** lists model fallback chains (`routing.chains`). Unavailable when core runs on
  `config.yaml` (displays a notice pointing to `pluxy import cpa`).
  - **+ Add a fallback** and **Edit** open the fallback editor: **When a tool asks for** (read-only
    when editing), up to 8 ordered steps with **Model**, **Provider** (**Any provider** or a running
    provider), **Only some accounts** checkboxes, **↑** / **↓**, **Remove step**, **+ Add a step**,
    **Save**, and **Cancel**. Warns if no step names the requested model itself.
  - **Remove…** asks for confirmation (**Remove**) before deleting the fallback chain.
- **Rules:** each switch or select saves immediately; number and text fields save on Enter or blur.

| Rule label | Where shown | Config key ([config.md](config.md#routing)) |
| --- | --- | --- |
| **How a group shares requests** (**Take turns**, **Split by weight**, **Fill one first**, **Least busy first**, **Soonest reset first**) | Always | `routing.strategy` |
| **Keep a conversation on one account** | Always | `routing.session_affinity.enabled` |
| **Release a quiet conversation after** | **More rules** | `routing.session_affinity.ttl_ms` |
| **Extra rounds when every account fails** | **More rules** | `routing.request_retry` |
| **Accounts to try per round** | **More rules** | `routing.max_accounts` |
| **Longest wait for a free account (seconds)** | **More rules** | `routing.max_wait_ms` |
| **Rest an account after it fails** | **More rules** | `routing.disable_cooling` (inverted) |
| **Rest after a provider error (seconds)** | **More rules** | `routing.transient_error_cooldown_ms` |
| **Show tools when a limit resets** | **More rules** | `routing.quota_response_mode` (`usage-limit` / `retryable`) |
| **Prefixed accounts answer only prefixed names** | **More rules** | `routing.force_model_prefix` |

- **Spends or deletes:** **Remove…** deletes the chosen fallback chain after confirmation; nothing
  spends from an account.

## Activity

Activity has two tabs: **Requests** and **Usage**. When `usage.enabled` is off, both tabs show a
single notice linking to **Turn it on in Config** (`#/config/logging`).

### Requests

- **Address:** `#/activity` or `#/usage` (`data-page="usage"`). Supports query parameters
  `failed=1`, `provider`, `account` (ID or public ID), `model`, `key`, and `trace=<id>` (reads down
  up to 10,000 events to locate and open that request).
- **Header actions:**
  - **Refresh:** reloads requests, accounts, keys, logging settings and summary. Requests do not
    auto-refresh on a timer, so rows do not shift while you inspect them.
  - **Save as spreadsheet:** exports up to 10,000 newest matching requests to
    `pluxy-requests-YYYY-MM-DD.csv` with columns `Time, Model, Provider, Account, Client key,
    Failed, Status, Latency, Tokens, Trace ID`. Notes if the export hit the 10,000-row cap, and
    warns when **Hide names** is on that the downloaded CSV contains real names.
- **Filters:** **All** / **Failed** switch, **Filter** button opening dropdowns for **Provider**,
  **Account**, **Model** and **Key** (each appears when at least 2 options exist or the filter is
  active), removable filter chips, and **Clear filters**.
- **Table:** groups requests under local day headings (**Today**, **Yesterday**, or full date),
  loading 50 rows at a time with **Show older**. Columns: **Time**, **Model**, **Account** (with
  detour chips such as **2nd**, **3rd**, **4th**, **5th or later**, **fallback {step}**, or **via
  backup**), **Key**, **Tokens**, **Time taken**, and **Result** (check mark or failure word chip);
  narrower list panes hide **Key**, **Tokens**, and then **Time taken** (and wide screens hide all
  three while the side detail pane is open), though every field stays in the detail pane. At the end
  of an unfiltered list, states how far back core's memory keeps requests and links to **Usage**
  when older day files exist. If core dropped or failed to write usage records to disk since
  starting, a warning banner states the count and links to **Logs**.
- **Request detail pane:** opens when you select a row. Shows:
  - Provider, account, outcome chip, and three tiles: **Time taken**, **Tokens** (`{input} →
    {output}`), and **Tries** (or **Fallback step**).
  - Routing story: decision sentence (for example, `Tried <account>: rate limited. Then <account>
    answered in 2.4 s.`), fallback note, accounts left out and why, and why the answering account
    was chosen (linking to **Order & fallback** when driven by account order). When no account
    serves the requested model, lists served models per account with **Add an alias** / **Add an
    alias on {account}** links (opening `#/accounts/<id>?alias=<model>`) or **Open {account}** for
    config-file accounts, plus a link to **Tools**.
  - **Attempts** list when more than one attempt ran.
  - The request's facts: **Time**, **Account**, **Provider**, **Model used** (when different from
    requested), **Key**, **Path**, **Tokens** (`in`, `out`, `cached`), and **Trace** (links to
    `#/logs/<trace>` for failed requests or when `logs.request_log` is on; otherwise copyable text).
  - Next-step button: **Open {account}** or **Open its log**.
- **Spends or deletes:** nothing.
- **Settings read:** `usage.enabled`, `usage.max_events`, `usage.max_age_ms`
  ([config.md](config.md#usage)); `logs.request_log` ([config.md](config.md#logs)).

### Usage

- **Address:** `#/activity/usage` (`data-page="activity/usage"`). Supports a single filter in the
  query (`provider`, `account`, `model`, or `key`).
- **Refresh:** auto-refreshes every 60 seconds while visible; **Refresh** in the header reloads
  immediately.
- **Range selector:** **24 hours** (1 hour per bar), **7 days** (default, 1 local day per bar), or
  **30 days** (1 local day per bar). Aggregates closed day files from disk (`usage.keep_days`,
  default 90 days) with today's events from memory; notes when pluxy's record starts after the
  beginning of the selected range.
- **Summary tiles and chart:** four tiles — **Requests** (with failed count), **Went through**
  (percentage and ratio), **Tokens** (with cached count), and **Avg time** (per request). Selecting
  **Requests**, **Went through**, or **Tokens** switches the bar chart below; **Avg time** is
  read-only. Chart bars support mouse hover and keyboard navigation (`ArrowLeft`, `ArrowRight`,
  `Home`, `End`) to read individual bar figures. **Show the numbers** / **Hide the numbers** toggles
  the tabular data behind the chart.
- **Failure hotspot:** when at least 3 requests failed in the range and one model accounts for half
  or more, shows a line naming that model with **See them** linking to filtered **Requests**.
- **Breakdown table:** switch between **Provider**, **Model**, **Account**, and **Key**. Columns
  show name, **Requests**, **Share** (stacked bar and percentage), **Failed**, **Tokens**, and **Avg
  time**. Displays the top 10 rows with **Show all {count}** / **Show the top 10**. Selecting any
  row filters **Usage** to that item, displaying a removable filter chip and a **See these
  requests** link to **Requests**.
- **Spends or deletes:** nothing.
- **Settings read:** `usage.enabled`, `usage.keep_days` ([config.md](config.md#usage)).

## Tools

- **Address:** `#/tools` (`data-page="tools"`).
- **Header action:** **Refresh** runs a fresh tool scan and reloads keys and 7-day key usage. If
  core has never scanned, opening the page starts one scan and polls every second while it runs.
- **Coding tools:** lists coding tools detected on the computer core runs on, with a summary line
  (`{installed} installed · {connected} connected · checked {when}`, plus **Detected on the
  computer pluxy runs on.** when viewed from another computer). Connected tools appear first, then
  other installed tools; uninstalled or unchecked tools sit under **Other tools ({count})** (open
  automatically alongside **No coding tools found.** when no tool is installed).
  - Each row shows the tool name (**Codex** for `codex`), version (when found), state badge
    (**Connected**, **Installed · not connected**, **Settings need update**, **Cannot change
    settings**, **Not installed**, or **Not checked**), settings file path, and action.
  - Tools without paste snippets (**Claude Desktop**, **Grok Build**, **Hermes Agent**) read **Set
    up from the desktop app** (and a note below the list says the same unless all three appear as
    rows).
  - Warns with an **Add an account** link (`#/add-account`) when pluxy has no accounts. On cores
    without `GET /setup`, displays a note to use **Address for tools** and **Keys** below.
- **Connecting and disconnecting a tool from the panel:**
  - **Which tools:** **Claude Code** (`~/.claude/settings.json`) and **Codex**
    (`~/.codex/config.toml`), when installed on core's computer and their settings file is neither
    refused nor already connected by hand.
  - **Buttons and hint:** an unconnected eligible row shows **Connect** and **Set up by hand**,
    with a line naming the file and noting that **Connect** makes a key, writes it into that file
    on core's computer, keeps a copy of the file as it was, and **Disconnect** puts it back. Once
    connected by pluxy, the row shows **Disconnect** (and **Connect again** if the file later stops
    pointing at pluxy). A tool connected by hand keeps **Set up** alone.
  - **What Connect writes and where:** runs immediately without asking first. Core makes a client
    key named after the tool (reusing an existing core key if the file already holds one), uses the
    model picked in **Set up by hand** if any (otherwise the best-matching served chat model, or
    leaves model settings alone when core serves no chat model yet), and merges only pluxy's
    settings into the tool's file in the home folder of the user core runs as (preserving unrelated
    settings and tightening permissions to owner-only: `0600` for new files, or removing group and
    other bits on existing files):
    - **Claude Code** (`~/.claude/settings.json`): merges `env` (`ANTHROPIC_BASE_URL`,
      `ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY`,
      `CLAUDE_CODE_AUTO_MODE_SERVER`, plus `ANTHROPIC_DEFAULT_OPUS_MODEL`,
      `ANTHROPIC_DEFAULT_SONNET_MODEL`, `ANTHROPIC_DEFAULT_HAIKU_MODEL` and top-level `model` when
      a model is chosen) and removes `env.ANTHROPIC_API_KEY`.
    - **Codex** (`~/.codex/config.toml`): sets root `model_provider = "pluxy"` (and `model` when
      chosen) above tables, sets `[model_providers.pluxy]` (`name`, `base_url` ending in `/v1`,
      `wire_api = "responses"`, `experimental_bearer_token`), and removes `env_key` and
      `requires_openai_auth` from `[model_providers.pluxy]`.
    - Core refuses symlinks, non-regular files, files larger than 2 MiB, files or folders owned by
      another user, unexpected formats, or a file modified while being updated.
    - Before writing, core saves a private backup record (original file contents, permissions,
      whether the file or folder was created, applied bytes, and created key ID) in
      `state/tools/<tool>.json` beside core's config. A note under the row prompts you to restart
      the tool after connecting.
  - **What Disconnect puts back:** runs immediately without asking first (available only for tools
    pluxy connected). If the file still matches what **Connect** wrote, restores the original file
    bytes and permissions (or deletes the file — and its folder if empty — when **Connect** created
    them). If the file changed since, keeps those later changes and reverts or removes only the
    settings **Connect** changed that still hold **Connect**'s values (and removes an empty
    `[model_providers.pluxy]` table that **Connect** added). Deletes the backup record and removes
    the client key **Connect** created unless another connected tool's record uses it or it is the
    last client key. A note under the row reports whether the file was restored, merged, or
    removed.
- **Set up by hand** / **Set up** (**Claude Code**, **Codex**, **OpenCode**, **Pi**, **Oh My Pi**;
  toggled with **Hide**, one row open at a time): opens three inline steps:
  1. **Make key** (or **Make another key** if a key with that tool's name already exists): creates
     a client key named after the tool and substitutes it directly into step 2's snippet in page
     memory only (closing its one-time notice in **Keys**, deleting the key, or leaving the page
     clears it from the snippet).
  2. **Model {tool} starts with** selector (when pluxy serves models; picking a model here also
     sets the model **Connect** writes for that tool; when none are served, keeps `<model-id>` and
     links to **Models your tools can ask for** on **System**) and config file snippet(s) with
     **Copy**.
  3. Restart the tool to load the new settings.
- **Address for tools:** displays pluxy's base address with **Copy**, notes whether other computers
  on your network can reach it (when viewing a local core), and links to **Addresses for other
  programs** on **System** (`#/system`).
- **Keys:** titled **Keys ({count})** once loaded, lists every client key with its name (and masked
  value, or masked key if unnamed) and 7-day request count (`{count} request this week`,
  `{count} requests this week`, or `Not used this week`). When no keys exist, shows **No keys.
  Until there is one, any program that reaches pluxy can use it.** and **New key**.
  - **New key:** opens a form with an optional **Name** (up to 64 characters), **Make key**, and
    **Cancel**. Creating a key displays its full value once with **Copy** and **Close** (held only
    in page memory until closed, deleted, or you leave the page).
  - **Rename:** edits the key's label (`client_key_labels`, up to 64 characters; saves with
    **Save**, cancels with **Cancel**).
  - **Delete:** asks for confirmation (**Yes, delete** / **Cancel**) before deleting the key;
    disabled when only one key remains.
- **Spends or deletes:** nothing spends from an account. **Connect** and **Disconnect** write or
  revert the tool's settings file on core's computer immediately without asking first (**Connect**
  creates a client key and backs up the original file; **Disconnect** restores, merges, or removes
  the file and deletes the created key if unused and not the last key). **Delete** in **Keys**
  permanently removes a client key after confirmation (tools using it stop working).
- **Settings keys:** `client_keys`, `client_key_labels` ([config.md](config.md#top-level)); backup
  records in `state/tools/<tool>.json`.

## Settings

Settings lists its pages as topics in three groups: **This device** (**General**, **Themes**),
**pluxy** (**Config**, **Extensions**, **Logs**, **System**, **Setup**), and **From extensions**
(pages added by running extensions).

### General

- **Address:** `#/settings` (`data-page="settings"`), group **This device**.
- What it sets (kept in this browser, each saved at once):
  - **Appearance:** **Light**, **Dark**, or **Follow the system**. When the active theme supports
    only one appearance, displays a notice linking to **Themes**.
  - **Hide names:** masks account e-mail addresses, names, IDs and key labels on screen across every
    page (does not mask copied text or downloaded files) and adds the **Names hidden** button to the
    header strip.
  - **Sign out:** asks for confirmation, then removes the management key from browser storage and
    returns to the sign-in screen.
- **Spends or deletes:** **Sign out** clears the stored management key from this browser after
  confirmation.

### Themes

- **Address:** `#/themes` (`data-page="themes"`), group **This device**. See [themes.md](themes.md)
  for the theme file format, tokens, hooks and T3 Code compatibility.
- **Theme** (stored in core, shared across all windows): lists the built-in themes (**Default**,
  **Terminal**, **Paper**) and any added themes. Selecting a theme applies and saves it immediately.
  - **Save theme file:** downloads the active theme's JSON file.
  - **Remove:** shown for user-added themes (and unusable stored themes); asks for confirmation
    (**Yes, remove {name}**) before deleting the theme from core and reverting to Default.
- **Layout** (device-local; overrides the active theme until you press **Reset layout**):
  - **Frame:** **Inset**, **Flat**
  - **Navigation:** **Sidebar**, **Rail**, **Top**
  - **Width:** **Wide**, **Full**, **Narrow**
  - **Density:** **Compact**, **Normal**, **Roomy**
  - **Tabs:** **Top**, **Side**
  - **Details:** **Beside**, **Below**
  - **Lists:** **Table**, **Cards**
- **Add a theme:** upload a `.json` file (**Theme file**, checked against the 256 KiB limit before
  reading) or paste JSON into **Or paste a theme file** and press **Add**. Valid themes are stored
  in core and turned on immediately.
- **Custom CSS** (device-local):
  - **Custom CSS** switch: turns both the theme's CSS and your own CSS on or off (if custom CSS
    hides this switch, reload with `?safe`).
  - **Your CSS:** applies as you type on top of every theme.
- **Spends or deletes:** **Remove** deletes a stored theme from core after confirmation.

### Config

- **Address:** `#/config` (**Settings** tab) and `#/config/source` (**Settings file** tab;
  `?account=<id>` highlights that account in the editor), `data-page="config"`, group **pluxy**.
- **Top notices:** warns if the settings file on disk does not parse (disabling Settings controls
  with **Try again**) or if core rejected the latest reload and is running the previous version.
- **Settings tab** (switches save immediately; text and number fields save on Enter or blur):

| Section | Label | Config key ([config.md](config.md)) | Notes |
| --- | --- | --- | --- |
| **Network** | **Outbound proxy** | `proxy_url` | Shows saved proxy without password; changing the address requires re-entering credentials if a password was saved |
| **Network** | **Account order, retries and cooldowns** | — | **Open Order & fallback** links to `#/routing` |
| **Other computers** | **Let other computers manage pluxy** | `management_allow_remote` | Takes effect at once; asks (**Turn off** / **Keep it on**) before disabling when accessed remotely |
| **Other computers** | **Address pluxy listens on** | `listen` | Takes effect on next start; asks (**Save the address** / **Do not change it**) if the new host or port could lock out the current session |
| **Logging** | **Write pluxy's log to a file** | `logs.to_file` | Required to view **pluxy's log** on **Logs** |
| **Logging** | **Largest log size (MB)** | `logs.max_total_size_mb` | `0` means no limit |
| **Logging** | **Failed requests to keep** | `logs.error_logs_max_files` | `0` keeps all |
| **Logging** | **Keep a record of requests** | `usage.enabled` | Controls recording for **Home** and **Activity** |
| **Logging** | **Log every request** | `logs.request_log` | While on, the **Failed requests** tab on **Logs** lists none; open logs by trace ID |
| **Keys** | **Keys for coding tools** | — | **Open Tools** links to `#/tools` |
| **Keys** | **Management key** (**Replace key**) | `management_key` | Asks (**Yes, replace key**), generates a new random key, signs this browser in with it, and shows it once with **Copy** and **Close** (other windows and commands must sign in again) |

- **Settings file tab:** edits the raw `config.json` (or `config.yaml`) with secrets shown as
  placeholders (leaving a placeholder unchanged preserves the secret).
  - **Find in file** with **Previous** and **Next**.
  - **Check for mistakes:** validates the draft with core without saving.
  - **Review and save:** opens a line diff (masking any newly typed secrets) before **Save**.
  - **Refresh:** reloads from disk (asks before discarding unsaved edits).
  - **Discard edits:** reverts the editor to the last loaded file.
  - Leaving the tab or page with unsaved edits asks (**Leave** / **Stay**).
- **Spends or deletes:** **Replace key** invalidates the previous management key after confirmation;
  **Save** overwrites the config file after diff review.

### Extensions

- **Address:** `#/extensions` (`data-page="extensions"`), group **pluxy**.
- **Header action:** **Refresh** reloads installed packages, running status and registries. Polls
  every 1.5 seconds while any extension is in state **Starting**. Displays a warning notice when
  core runs with `pluxy serve -safe-mode`.
- **Installed:** lists installed extensions with provider name, package ID, version and state badge.
  Selecting an extension opens its detail view:
  - Facts: **State**, **Version**, **Installed from**, **Installed**, **Checksum** (first 16 hex
    chars), **Why it stopped** (if any), and for each running process copy: **Process ID**,
    **Requests now**, **Restarts**, and **Last error**.
  - **Settings** form (when enabled and the extension declares a settings schema): saves
    extension-specific settings with **Save** (secret fields show `A value is set.` with a **Clear
    {label}** checkbox).
  - **Turn off** / **Turn on:** stops or starts the extension program (its accounts stop taking
    requests while off, but are kept).
  - **Remove:** asks for confirmation, then deletes the extension program while keeping its
    accounts.
- **Available to install:** lists uninstalled packages from `packages.registries` (and the bundled
  registry) with **Install**. Warns individually for any registry that could not be read; links to
  **Open the settings file** (`#/config/source`) when no registry is configured.
- **Spends or deletes:** **Remove** deletes the installed extension package after confirmation
  (accounts stay).
- **Settings keys:** `packages.registries` ([config.md](config.md#packages)).

### Logs

- **Address:** `#/logs` (`data-page="logs"`), group **pluxy**. `#/logs/<trace ID>` opens the
  **Failed requests** tab on that trace ID. When **Hide names** is on, a notice states that masking
  applies on screen only and downloads contain the raw log.
- **pluxy's log** (tab):
  - Follows core's log file every 2 seconds while visible and **Follow new lines** is on (both
    **Follow new lines** and **Wrap lines** start on). Keeps the latest 10,000 lines in browser
    memory.
  - **Find in log** and **Show** (**All lines**, **Warnings**, **Errors**) filter the loaded lines.
  - **Download:** saves the currently shown lines as `pluxy-log.txt` (unmasked).
  - **Clear log:** asks for confirmation (**Yes, clear**) and deletes core's log file; failed
    request logs stay.
  - When `logs.to_file` is off, shows **Turn on file logging** (enables `logs.to_file` in one click)
    and **Logging settings** (`#/config/logging`).
- **Failed requests** (tab):
  - Lists full logs kept for recent failed requests (columns: **Request**, **Time**, **Trace ID**,
    **Size**). While `logs.request_log` is on, the list is empty and requests are opened by trace
    ID.
  - **Trace ID** and **Open:** validates the trace ID format (`YYYYMMDDHHMMSS-<16 hex>`) and opens
    that request's log.
  - Opening a log shows **Download** and **Copy trace ID** above the log text. Logs larger than 2
    MiB cannot be shown or downloaded in the panel (read them in core's `logs` folder).
- **Spends or deletes:** **Clear log** deletes core's log file after confirmation.
- **Settings keys:** `logs.to_file`, `logs.max_total_size_mb`, `logs.error_logs_max_files`,
  `logs.request_log` ([config.md](config.md#logs)).

### System

- **Address:** `#/system` (`data-page="system"`), group **pluxy**.
- **Header action:** **Refresh** reloads status and served models. Shows a warning notice when core
  runs with `pluxy serve -safe-mode`.
- **About:** shows **Version** (and 12-character commit hash), **Built** timestamp, **Address** with
  **Copy**, and **Settings file** (whether the latest file is in use, or why reload refused it).
- **Models your tools can ask for:** lists served models grouped by provider and extension ID, with
  **Model** (and **Copy**), **Also called** (aliases), and **Accounts**. When none are served, links
  to **Open Accounts** (`#/accounts`) and **Open Extensions** (`#/extensions`).
- **Addresses for other programs:** lists base URLs with **Copy** for **OpenAI API**
  (`<origin>/v1`), **Anthropic (Claude) API** (`<origin>`), **Gemini API** (`<origin>`), **Codex
  with an API key** (`<origin>/v1`), and **Codex signed in with ChatGPT**
  (`<origin>/backend-api/codex`), and links to **Tools** (`#/tools`).
- **Spends or deletes:** nothing.

### Setup

- **Address:** `#/setup` (`data-page="setup"`), group **pluxy**. Titled **Set up pluxy** on a first
  run and **Setup** afterwards.
- **First-run behavior:** on a first run, signing in with no page in the URL hash opens Setup
  automatically in place of Home. If core does not serve `GET /setup` (404), Setup is omitted from
  Settings.
- **CLIProxyAPI import line** (shown above the steps when core detects a CLIProxyAPI setup):
  - **Preview import** runs a dry run and shows three folded lists (**Will import**, **Set
    differently in pluxy**, **Left out**).
  - **Import now** (available only from a current preview; a newer scan clears the preview) runs
    that exact import, reloads setup and accounts, and pre-ticks needed providers in step 1 (naming
    any unavailable provider with an **Open Extensions** link). Import never changes core's
    management key.
  - Once an import is recorded, the line shows when it ran and offers **Import again**. If core runs
    on `config.yaml` (409) or lacks the endpoint (404), **More details** shows the terminal command
    (`pluxy init` or `pluxy import cpa`).
- **Three numbered steps** (one open at a time; first incomplete step opens on first run, all start
  folded afterwards):
  1. **Your AI accounts:** tick provider kinds (detected kinds are pre-ticked with core's reason;
     additional kinds sit under **More ({count})**), press **Add {names}** to install them, then
     sign in with the embedded **Add an account** form. Press **Continue** (once at least one
     account can take requests) or **Skip**.
  2. **Your coding tool:** shows the same coding-tool list, **Connect** / **Disconnect** actions
     (for Claude Code and Codex), and three-step **Set up** / **Set up by hand** snippets as
     **Tools** (keys made in **Set up** / **Set up by hand** stay in memory only while step 2 is
     open). Press **Continue** / **Skip**, or **Refresh** to re-scan.
  3. **Check it works:** choose an account (when 2 or more can be tested) and optionally **Choose a
     model**, then press **Send a test request**. The test is not listed in Activity or Usage. Press
     **Finish setup** (after passing), **Test again**, **Finish anyway**, or **Finish without
     testing**.
- **Leaving setup:** on first run, **Leave setup for now** marks setup done and opens **Home**
  (Setup stays under Settings). Afterwards, **Back to Home** returns to **Home**.
- **Spends or deletes:** **Send a test request** spends one request against the chosen account
  (stated above the button). **Import now** writes imported accounts and settings only after you
  preview and confirm. In step 2, **Connect** and **Disconnect** write or revert the tool's
  settings file on core's computer (and create or remove its client key) immediately without asking
  first.

### Extension pages

- **Address:** `#/ext/<publisher>/<name>/<page>` (`data-page="ext/<publisher>/<name>/<page>"`),
  group **From extensions**.
- **How they run:** each page contributed by a running extension renders inside a sandboxed
  `<iframe>` served from core's separate extension-pages origin (`extension_pages`). The frame never
  receives the management key; it communicates with the panel over a message channel that provides
  theme tokens, layout, **Hide names** state, and the extension's own settings. If the frame
  navigates to another document, the panel closes its channel and offers **Reload**.
- **Settings keys:** `extension_pages.listen`, `extension_pages.url`
  ([config.md](config.md#extension_pages)). See the
  [extension pages contract](extension-pages.md).

## Where API keys added in the panel are kept

An API key added on **Add an account** is stored in core's credential store (`credentials_dir`, by
default `credentials` beside the config file; [config.md](config.md#top-level)), one file per
account, readable only by core's user (mode `0600` on Unix; restricted to core's user and `SYSTEM`
on Windows). It is never written to `config.json` or `config.yaml`, and no management reply shows it
again.

Accounts already defined in the config file appear in the panel, keep working, and are edited in the
**Settings file** tab of **Config**.
