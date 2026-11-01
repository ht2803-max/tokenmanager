# TokenManager

A terminal-native dashboard for the tokens and money your AI coding agents spend.
It reads the session files that Claude Code, OpenAI Codex, Gemini CLI, Aider and
Continue already write to disk, normalizes them into one local SQLite database,
and shows usage, estimated cost, budgets and optimization advice. It has no web
UI, needs no account, and sends nothing off your machine.

```
 ◆ TokenManager  1 Overview  2 Usage  3 Agents  4 Sessions  5 Projects  6 Models  7 Optimize  8 Budgets  9 Settings
╭ Today ──────────╮╭ This week ──────╮╭ This month ─────────────╮╭ Projected month ────────╮
│ $5.21           ││ $38.60          ││ $199.73                 ││ $200.79                 │
│ 6.58M tokens    ││ 61M tokens      ││ 580M tokens · cache 571M││ budget $250.00          │
╰─────────────────╯╰─────────────────╯╰─────────────────────────╯╰─────────────────────────╯
```

## Install

Requires Go 1.27.3 or newer. An older Go 1.21+ install downloads the newer toolchain automatically.

```bash
go install github.com/ht2803-max/tokenmanager/cmd/tokenmanager@latest
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
the `ht2803-max/homebrew-tap` repository (create that repository first). Once a
release exists:

```bash
brew install ht2803-max/tap/tokenmanager
```

## Quick start

```bash
tokenmanager            # interactive dashboard
tokenmanager today      # one-shot summary
tokenmanager optimize   # recommendations
```

Agents are found automatically. Each command first ingests new data
incrementally: files that haven't changed are skipped without being opened, and
appended files are read from the last byte offset. A rescan with no new data
takes a few milliseconds.

## Keyboard

| Key | Action |
|---|---|
| `1`–`9`, `←/→` | Overview, Usage, Agents, Sessions, Projects, Models, Optimize, Budgets, Settings |
| `j/k`, `↑/↓`, `pgup/pgdn`, `g/G` | move in lists |
| `tab` | switch panel (Overview), toggle the detail pane elsewhere |
| `enter` / `esc` | open / close details |
| `f` / `F` | date filter: today → week → month → 30 days → all |
| `/` | search the current list |
| `r` | rescan and refresh |
| `?` | help |
| `q` | quit |

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
| `budget`, `budget set monthly 100 [--for agent:claude-code]`, `budget rm …` | budgets |
| `watch` | live stream of new requests with running cost |
| `export [-f json\|csv] [-o file]` | per-request records |
| `daemon start\|stop\|status\|run` | background collector + budget alerts |
| `scan [--rebuild]` | ingest now / rebuild the database |
| `config`, `config init`, `config path` | configuration |

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

Costs are **estimates at public API list prices**. If you are on a
subscription (Claude Pro/Max, ChatGPT Plus/Pro), read them as the API-equivalent
value of your usage, not as your bill. Anthropic prices are built in (Claude 5
family back to Claude 3.5 Haiku), including cache read/write rates, Haiku 5.5's
long-prompt tier and fast mode. Built-in OpenAI and Gemini prices cover only
well-established models; please verify them.

Models are matched by exact id after removing provider prefixes and date
suffixes. A model without a known price is shown as `n/a` and its tokens are
counted as unpriced. TokenManager never borrows a price from a similar-looking
name. Add prices in `config.yaml`:

```yaml
pricing:
  gpt-5.6-terra: {input: 1.25, output: 10, cache_read: 0.125}
```

## Budgets

Daily, weekly and monthly limits can be set globally or per agent, project
(path substring) or model (id substring). The CLI and TUI show progress bars
and the month-end projection. The daemon sends one desktop notification per
threshold (default 50/80/100%) per period, via macOS Notification Center or
`notify-send`. Budgets are **monitoring only**: TokenManager cannot stop an
agent or change limits set on the provider side.

## Daemon

`tokenmanager daemon start` runs the collector in the background
(`~/.local/share/tokenmanager/daemon.log`), so usage and alerts are captured
while the TUI is closed. To start it at login, see `packaging/launchd` (macOS)
or `packaging/systemd` (Linux).

## Optimization engine

`tokenmanager optimize` (and view 7) looks at the last 30 days and reports:

- large or repetitive instruction files (`CLAUDE.md`, `AGENTS.md`, `GEMINI.md`, …) loaded into every request
- low prompt-cache hit rates, and context being re-cached (repeated context)
- requests carrying very large contexts
- premium models used for light tasks, priced against a cheaper alternative
- unusually expensive sessions, long sessions near the context limit
- projects that dominate spend

Every saving is labelled as an estimate. Instruction files are read only to
measure them (about 4 bytes per token) and are **never modified**.

## Configuration

`~/.config/tokenmanager/config.yaml` (honors `XDG_CONFIG_HOME`; `TOKENMANAGER_CONFIG`
overrides the path). See [`config.example.yaml`](config.example.yaml): enabled
agents and path overrides, pricing, budgets, thresholds, refresh interval,
currency and conversion rate, retention, notifications and privacy. The
database is stored at `~/.local/share/tokenmanager/usage.db` (honors `XDG_DATA_HOME`).

## Privacy

Only token counts, model ids, timestamps, session ids and project paths are
stored. Prompts, responses and code are never stored. Set
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
internal/storage      SQLite (records, file offsets, alert state)
internal/pricing      price table and cost calculation
internal/analytics    aggregation, periods, series, projections
internal/optimizer    findings and savings estimates
internal/budget       budget evaluation
internal/daemon       background collector, notifications
internal/config       YAML configuration
internal/format       number formatting and Unicode charts
```

The usage engine (collector, storage, analytics) doesn't depend on the UI, so
every CLI command, the TUI and the daemon share it.

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
