# TokenManager

A terminal-native dashboard for the tokens and money your AI coding agents spend.
It reads the session files that Claude Code, OpenAI Codex, Gemini CLI, Aider and
Continue already write to disk, normalizes them into one local SQLite database,
and shows usage, estimated cost, budgets and optimization advice. It runs in the
terminal (with an optional static HTML export), needs no account, and sends
nothing off your machine.

![TokenManager dashboard tour: overview, usage, agents, sessions, models and optimization advice](docs/demo.gif)

## Install

### Homebrew (macOS / Linux)

```bash
brew install ht2803-max/tap/tokenmanager
```

Prebuilt binaries for macOS, Linux and Windows are also on the
[releases page](https://github.com/ht2803-max/tokenmanager/releases).

### With Go

Requires Go 1.27.3 or newer. An older Go 1.21+ install downloads the newer toolchain automatically.

```bash
go install github.com/ht2803-max/tokenmanager/cmd/tokenmanager@main
```

This puts `tokenmanager` in `$(go env GOPATH)/bin` (usually `~/go/bin`). If the
command isn't found afterwards, add that directory to your `PATH`:

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
```

### From source

```bash
git clone https://github.com/ht2803-max/tokenmanager.git
cd tokenmanager
make build        # binary in bin/tokenmanager
make install      # or install into $(go env GOPATH)/bin
```

`make cross` builds macOS, Linux and Windows binaries into `dist/`. SQLite is
pure Go, so no C toolchain is needed.

### Releases and Homebrew

Tagged releases are built with `goreleaser release --clean` using
`.goreleaser.yaml`, which publishes archives for each platform and a formula to
the `ht2803-max/homebrew-tap` repository.

## Quick start

```bash
tokenmanager            # interactive dashboard
tokenmanager today      # one-shot summary
tokenmanager optimize   # recommendations
tokenmanager web --open # browser dashboard (static HTML file)
```

Agents are found automatically. Each command first ingests new data
incrementally: files that haven't changed are skipped without being opened, and
appended files are read from the last byte offset. A rescan with no new data
takes a few milliseconds.

## Keyboard

| Key | Action |
|---|---|
| `1`–`9`, `←/→` | Overview, Usage, Agents, Sessions, Projects, Models, Optimize, Budgets, Settings |
| `0` | Limits: current 5h block, weekly window, recent blocks ([Subscription plans](#subscription-plans)) |
| `j/k`, `↑/↓`, `pgup/pgdn`, `g/G` | move in lists |
| `tab` | switch panel (Overview), Tools panel (Usage), toggle the detail pane elsewhere |
| `enter` / `esc` | open / close details |
| `f` / `F` | date filter: today → week → month → 30 days → all |
| `/` | search the current list |
| `r` | rescan and refresh |
| `?` | help |
| `q` | quit |

The Overview includes a GitHub-style activity heatmap: one square per day over
the last 12 months (weeks as columns, Monday on top), shaded by total tokens
relative to your own active days, with your current and longest streak. It
follows `--agent/--project/--model` but not the `f` date filter. In tall
windows (about 37 rows or more) it gets its own full-width panel; in smaller
ones it takes the place of the 30-day chart, down to 80×24.

The dashboard watches agent directories with filesystem events (fsnotify) and
updates live while you work. It also rescans slowly as a safety net.

## Commands

| Command | |
|---|---|
| `today`, `week`, `month` | totals, trend sparkline, breakdown by agent / model / project |
| `agents` | detection status, data accuracy and usage per agent |
| `sessions [-n 25]` | recent sessions with duration, context fill, cost |
| `projects`, `models` | per-project and per-model comparison |
| `costs` | cost split by input / output / cache read / cache write; month projection |
| `optimize` | 30-day analysis with estimated savings |
| `tools [--by server]` | tool calls, result sizes, estimated result/carried tokens, share of input and cost per tool or MCP server ([Tools](#tools)) |
| `budget`, `budget set monthly 100 [--for agent:claude-code]`, `budget rm …` | budgets |
| `limits [-n 10]` (alias `blocks`) | subscription windows: current 5h block, burn rate, projection, weekly window, recent blocks |
| `watch` | live stream of new requests with running cost |
| `export [-f json\|csv] [-o file]` | per-request records |
| `daemon start\|stop\|status\|run` | background collector + budget and window alerts |
| `scan [--rebuild]` | ingest now / rebuild the database |
| `config`, `config init`, `config path` | configuration |
| `web [-o file] [--open] [--force]` | standalone HTML dashboard ([Browser Dashboard](#browser-dashboard)) |

Global flags: `--json`, `--csv`, `--today`, `--week`, `--month`, `--all`,
`--agent`, `--project`, `--model` (substring), `--no-scan`.

## Agents and data accuracy

| Agent | Source | Tokens |
|---|---|---|
| Claude Code | `~/.claude/projects/**/*.jsonl` (also `$CLAUDE_CONFIG_DIR`) | **actual**, per API request, including 5m/1h cache writes, subagents and fast mode |
| OpenAI Codex | `~/.codex/sessions`, `~/.codex/archived_sessions` (`$CODEX_HOME`) | **actual**, from `token_count` events; cached input is split out of input |
| Gemini CLI | `~/.gemini/tmp/*/chats/*.json` | **actual** per message; projects are identified by hash |
| Aider | `.aider.chat.history.md` per project (add paths in config) | **estimated**: Aider logs rounded counts; Aider's own cost figure is used |
| Continue | `~/.continue/dev_data/**/tokensGenerated.jsonl` | **actual** prompt/generated counts, no cache data |
| Cursor | detection only | **unavailable**: Cursor keeps usage server-side |

How double counting is avoided:

- Claude Code writes one line per content block of a response, and each line
  repeats the same usage object. Records are keyed by message id + request id.
- Resumed or forked sessions copy old history into new files. Those copies
  produce the same keys and are ignored.
- Codex sometimes emits the same `token_count` event twice. Records are keyed
  by the thread's cumulative token total.
- A partial last line (a file still being written) is left for the next read.
- Truncated or rewritten files are re-read from the start; the dedup keys keep
  this idempotent.

Adding an agent means adding an `Adapter` in `internal/agents` (roots, a file
matcher, a read mode and a parser that returns `model.Record`s) and a parser in
`internal/parser` with tests.

## Costs

Costs are **estimates at public API list prices**. On a subscription they are
the API-equivalent value of your usage, not your bill; see
[Subscription plans](#subscription-plans) for the limits that actually apply.
Anthropic prices are built in (Claude 5
family back to Claude 3.5 Haiku), including cache read/write rates, Haiku 5.5's
long-prompt tier and fast mode. OpenAI prices are built in from OpenAI's table
(GPT-6 and GPT-5.x families back to GPT-3.5 Turbo, plus o1/o3/o4-mini),
including cached-input rates and, for models that list them, 30-minute and
12-hour cache-write rates. Codex doesn't report which cache TTL was used, so
cache writes are priced at the 30-minute rate. Models without a listed cached
or write rate bill those tokens as ordinary input. Built-in Gemini prices cover
only well-established models; please verify them.

Models are matched by exact id after removing provider prefixes and date
suffixes. A model without a known price is shown as `n/a` and its tokens are
counted as unpriced. TokenManager never borrows a price from a similar-looking
name. Add prices in `config.yaml`:

```yaml
pricing:
  my-local-model: {input: 1.25, output: 10, cache_read: 0.125}
```

## Budgets

Daily, weekly and monthly limits can be set globally or per agent, project
(path substring) or model (id substring). The CLI and TUI show progress bars
and the month-end projection. The daemon sends one desktop notification per
threshold (default 50/80/100%) per period, via macOS Notification Center or
`notify-send`. Budgets are **monitoring only**: TokenManager cannot stop an
agent or change limits set on the provider side.

## Subscription plans

Claude Pro/Max and ChatGPT Plus/Pro aren't billed per token. They are limited
by usage windows: a **5-hour block** that starts with the first request after
the previous block ended, and a **weekly** cap. `tokenmanager limits`, TUI
view `0`, a panel on the Overview and a card in the HTML export show:

- the active block: start, elapsed and remaining time, tokens used
- the **burn rate** over the last 15 minutes (or the block average when there
  are too few requests), in tokens/min and API-equivalent cost/hour
- the projected total at block end, and the time until the limit at that pace
- the weekly window (rolling 7 days, or a fixed reset from config) and the
  last blocks

Block starts are floored to the hour. Only the plan's own agents count
(Claude Code for `anthropic`, Codex for `openai`; an explicit `--agent`
overrides that), and by default only input + output tokens count toward limits
(`plans.count`).

Every limit names its source, in this order of precedence:

| Source | Meaning |
|---|---|
| `reported` | Codex writes the ChatGPT plan's 5h and weekly usage (`rate_limits`: `used_percent`, `window_minutes`, `resets_at`) into its rollout files. These boundaries and percentages are the provider's own and cover the whole account, including other machines. The token figure next to them is implied from local usage ÷ percent. A report is ignored once its window has reset. |
| `configured` | `plans.<family>.session_tokens` / `weekly_tokens` from your config. |
| `personal max` | **Estimated**: your largest completed block in the last 30 days (weekly: the largest of the previous four weeks). It says where you stand against your own history, not against the provider's limit. |
| `no limit known` | Nothing to compare against; no ETA is shown. |

Claude Code doesn't write limit or reset information locally, so Anthropic
blocks are always estimated from request times and Anthropic doesn't publish
token limits. Like budgets, limits are **monitoring only**. Set
`plans.alerts: true` to have the daemon notify at `budgets.thresholds` once per
threshold per window.

## Browser Dashboard

The terminal dashboard is still the main interface. If you'd rather look at
your usage in a browser, TokenManager can also write the same data to one
standalone HTML file.

```bash
tokenmanager web                          # writes ~/.local/share/tokenmanager/dashboard.html
tokenmanager web --output ./dashboard.html
tokenmanager web --open                   # write it, then open it in the default browser
tokenmanager web --no-scan                # use the database as is, skip ingestion
```

- **Output location.** Without `--output` the file goes to
  `~/.local/share/tokenmanager/dashboard.html` (this follows `XDG_DATA_HOME`) and
  is replaced each time. If `--output` names a file that already exists, you're
  asked before it's overwritten; with no terminal attached the command stops.
  Pass `--force` to overwrite without asking. If `--output` names a directory,
  `dashboard.html` is written inside it.
- **What's inside.** Overview cards (today, week, month, projected month,
  monthly budget), daily token and spend charts, the input/output/cache mix,
  spend by agent, plus agents, sessions, projects, models, optimization
  findings, a Tools tab (per-tool table and a "share of input by tool" chart,
  last 30 days) and budgets. You can filter by date range (today, 7 days, 30 days,
  all time), agent, project and model, sort tables, expand sessions, hover
  over charts, and switch between dark and light themes.
- **Works offline.** The page is a single file with its CSS, JavaScript and
  data embedded. It loads nothing from the internet, starts no server, opens no
  port and sends no telemetry. A Content-Security-Policy in the file blocks
  network requests. You can double-click the file to open it.
- **Same numbers as the TUI.** Before writing the page, the command runs the
  normal incremental scan (unless you pass `--no-scan`). All totals, costs,
  budgets and findings come from the same analytics, pricing, budget and
  optimizer packages the CLI uses; the page itself only adds up and draws the
  pre-computed values. `--agent`, `--project` and `--model` limit what goes
  into the export.
- **A snapshot, not live.** The page doesn't refresh itself when new usage is
  collected. Run `tokenmanager web` again to regenerate it, then reload the
  page in your browser.
- **Treat it as private.** The file holds aggregated usage only: token counts,
  costs, model names, session IDs, project names and tool names with sizes. It
  never contains prompts, responses, code or tool inputs and outputs. Anyone who has the file can still read that
  data, so it's written with user-only permissions. Project paths are shown
  relative to `~`; set `privacy.hide_project_paths: true` to export only the
  last path element. The page lists at most the 1,000 most recent sessions;
  every total and chart still covers all usage.

## Daemon

`tokenmanager daemon start` runs the collector in the background
(`~/.local/share/tokenmanager/daemon.log`), so usage and alerts are captured
while the TUI is closed. To start it at login, see `packaging/launchd` (macOS)
or `packaging/systemd` (Linux).

## Tools

`tokenmanager tools` breaks usage down by tool name (`Read`, `Bash`, `Grep`,
`Edit`, `WebFetch`, Codex `exec`, Gemini `read_file`, …). MCP tools
(`mcp__<server>__<tool>`) are shown as `server/tool`, and `--by server` groups
them by MCP server. Unknown or empty names are counted as `other`. The
period, `--agent`, `--project`, `--model`, `--json` and `--csv` flags work as
they do elsewhere. In the TUI, press `tab` in Usage (view 2) to show the Tools
panel. Session details also list the top tools when the terminal is tall
enough. The browser dashboard has a Tools tab covering the last 30 days.

```
  Tool        Calls  Avg result  P95 result  Est. result tok  Est. carried tok  Share of input  Est. cost
  Bash          202         798        3.3k             161k             6.03M           13.5%      $5.11
```

**How tokens are attributed, and how accurate it is.** Providers report usage
per request, not per content block. Only the **call counts are actual**. The
rest is **estimated**:

| Figure | How it's estimated |
|---|---|
| Output tokens | A request that makes tool calls has its output tokens split across those calls, weighted by the byte length of each call's input, or evenly if lengths are unknown. Text and thinking in the same response count toward the calls too. |
| Result tokens (first-time input) | Byte length of the result text ÷ 4, the same heuristic used for instruction files. Images count as a flat 1,600 tokens each, because their base64 size says nothing about their token count. The result is charged to the **next request** in the same session file, since that's where it enters the prompt. It's priced at that request's cache-write rate if it wrote to the cache, otherwise at its input rate. A result after the session's last request is never charged. |
| Carried tokens | The same result tokens multiplied by the number of later requests that re-send them, priced at each request's cache-read rate (or its input rate if it had no cache hits). Counting stops when the session file ends, or when the prompt shrinks to less than half of the previous request's size (a compaction or summary). |
| Share of input | (first-time + carried) ÷ all prompt tokens in the period. |

Expect these numbers to be directionally right rather than exact. Tokenizers
average more or less than 4 bytes per token. Agents may truncate or reformat
results before sending them. A compaction that shrinks the prompt by less than
half isn't detected. In older Claude Code transcripts that log subagent
(sidechain) requests inside the parent file, carried context can stop early,
because the subagent's small prompts look like a compaction. Newer versions
write subagents to their own files, which are counted separately.

**What's stored.** One row per tool call, keyed by the provider's call id:
the tool and MCP server name, the session id, a timestamp, the input and
result byte counts and the estimated result tokens. Inputs, outputs, file
paths, commands and URLs are measured while parsing and then discarded.
Calls and results are merged by id, so a result read in a later scan,
or a resumed session that copies earlier history, is never counted twice.

**Upgrading.** Older databases have no tool rows, because their files had
already been read past their saved offsets. The first scan after upgrading
re-reads Claude Code, Codex and Gemini files once from the start, and prints
"Backfilled tool-call data from N existing session files". This keeps every
stored record. It deliberately doesn't run `scan --rebuild`, which would drop
history whose transcripts the agent has since deleted (Claude Code removes old
transcripts after 30 days by default). Tool data is only available for
transcripts that still exist. You can still run `tokenmanager scan --rebuild`
yourself to rebuild everything from what's on disk.

## Optimization engine

`tokenmanager optimize` (and view 7) looks at the last 30 days and reports:

- large or repetitive instruction files (`CLAUDE.md`, `AGENTS.md`, `GEMINI.md`, …) loaded into every request
- low prompt-cache hit rates, and context being re-cached (repeated context)
- requests carrying very large contexts
- premium models used for light tasks, priced against a cheaper alternative
- unusually expensive sessions, long sessions near the context limit
- projects that dominate spend
- tool usage ([Tools](#tools), all estimates):
  - **large tool results**: a built-in tool whose results are ≥15% of input tokens with a p95 ≥4k tokens, e.g. "Read results are ~40% of your input tokens; p95 Read result is ~18k tokens". Suggests reading with offset/limit or using Grep first.
  - **re-read results**: one tool returning 10 or more results over 2k tokens in a single session. This usually means content was read again instead of remembered. Only counts and sizes are known.
  - **shell output floods**: 3 or more shell commands that returned more than 5k tokens. Suggests piping through `head`/`tail` or quieter flags.
  - **MCP overhead**: an MCP server whose results are ≥10% of input tokens, plus servers called at most twice, which still add their tool definitions to every request (no savings estimate, because definition sizes aren't in local logs).
  - **carried context**: tool results re-sent by later requests make up ≥25% of prompt tokens and stay in context for 20 or more requests on average. Suggests `/compact` or a fresh session.

  Savings are priced with the actual model and cache rates of the requests
  that carried the results.

Every saving is labelled as an estimate. Instruction files are read only to
measure them (about 4 bytes per token) and are **never modified**.

## Configuration

`~/.config/tokenmanager/config.yaml` (honors `XDG_CONFIG_HOME`; `TOKENMANAGER_CONFIG`
overrides the path). See [`config.example.yaml`](config.example.yaml): enabled
agents and path overrides, pricing, budgets, thresholds, subscription plans, refresh interval,
currency and conversion rate, retention, notifications and privacy. The
database is stored at `~/.local/share/tokenmanager/usage.db` (honors `XDG_DATA_HOME`).

## Privacy

Only token counts, model ids, timestamps, session ids and project paths are
stored, plus, for each tool call, the tool and MCP server **name** and the
**byte counts** of its input and result. Prompts, responses, code, tool inputs
and outputs, file paths read by tools, commands and URLs are never stored. A
test feeds transcripts with fake API keys, paths and commands through every
parser and checks that none of them appear in the database, the JSON/CSV
exports or the HTML dashboard. Set
`privacy.hide_project_paths: true` to keep only the last path element. The tool
makes no network requests.

## Architecture

```
cmd/tokenmanager      entry point
internal/cli          Cobra commands (text / JSON / CSV output)
internal/tui          Bubble Tea dashboard (Lip Gloss, Bubbles)
internal/agents       adapter registry and discovery
internal/parser       per-agent log parsers → model.Record
internal/collector    incremental ingestion, fsnotify watcher
internal/storage      SQLite (records, tool calls, file offsets, alert state)
internal/pricing      price table and cost calculation
internal/analytics    aggregation, periods, series, projections, subscription blocks, tool attribution
internal/optimizer    findings and savings estimates
internal/budget       budget evaluation
internal/daemon       background collector, notifications
internal/web          standalone HTML dashboard export (embedded assets)
internal/config       YAML configuration
internal/format       number formatting and Unicode charts
```

The usage engine (collector, storage, analytics) doesn't depend on the UI, so
every CLI command, the TUI, the daemon and the HTML export share it.

## Development

```bash
make test     # parsers, pricing, incremental collection, live watch, analytics, TUI layout
make vet
TOKENMANAGER_DUMP=1 go test ./internal/tui -run Dump -v   # print every view
TOKENMANAGER_DEBUG=1 tokenmanager scan                     # verbose ingestion log
```

The TUI tests render every view at sizes from 60×18 to 220×60 and fail if any
line overflows the terminal.

### Not implemented yet

- Usage from provider APIs (for example Anthropic's Admin usage API). All data
  currently comes from local files.
- Cursor usage. Cursor doesn't store it locally.
- Native Windows notifications (alerts are still logged), and testing on
  Windows in general.
