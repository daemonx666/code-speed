<p align="right"><strong>EN</strong> | <a href="./README.zh-CN.md">简体中文</a></p>

<div align="center">
  <img src="build/icon/icon-1024.png" width="96" alt="code-speed icon">
  <h1>code-speed</h1>
  <p><em>How fast your coding agents write, what they use and cost, and how much quota is left.<br>In the menu bar, the system tray and the terminal. Read from local logs; nothing leaves your computer.</em></p>
  <p>
    <img src="https://img.shields.io/badge/macOS-13%2B-0A84FF?style=flat-square&logo=apple&logoColor=white" alt="macOS 13+">
    <img src="https://img.shields.io/badge/Windows-10%2B-0078D4?style=flat-square" alt="Windows 10+">
    <img src="https://img.shields.io/badge/Linux-x64%20%7C%20arm64-64748b?style=flat-square&logo=linux&logoColor=white" alt="Linux">
    <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-A855F7?style=flat-square" alt="MIT"></a>
  </p>
</div>

```
▸ 86.4 tok/s · today 41.2M tok · $18.70 · avg 104 tok/s · Codex 5h 23% 7d 40%
```

## What it shows

| | |
| --- | --- |
| **Speed** | The live output rate of every agent at once (tok/s over the last 3 seconds, stacked by agent) and what it costs per hour at API price, each session's recent speed, today's average and peak, and the last reply with its time to first token. On macOS the live rate sits beside the menu bar icon. |
| **Usage and cost** | Tokens (input, cache reads, cache writes, output), cache hit rate, requests and API-price cost for a **day, week, month, year or everything**, charted by hour, day or month (with an axis and the average) and broken down **by agent, by model and by project**, each against the period before. Click a bar to narrow the tables to that day, hour or month; sort and filter the tables. |
| **Speed by model** | For any period, each model's output speed as a band from its slow tenth to its fast tenth with the median marked, and its median and 95th-percentile time to first token, fastest first. Kept as small per-day histograms, so a year of it stays a few hundred KB. |
| **By reasoning effort** | Usage, speed and cost split by the reasoning effort each request asked for (Codex's `low` … `xhigh`, Claude Code's effort levels), per agent, for any period. |
| **Floating orb** | An optional small window above all others, on every Space and over full-screen apps: the live speed, today's cost, a ring in each agent's colour, and a dot when a provider has an incident. Drag it anywhere; it stays there. |
| **Service status** | Links to the status pages of Claude, OpenAI, GitHub, Cursor, xAI, Google Gemini, DeepSeek, Moonshot, MiniMax, Factory, Windsurf, Augment, Mistral, Groq, Fireworks and OpenRouter; the current state and today's incidents of those with a public feed, read every 10 minutes (on by default, turn it off in Settings; a page that can't be reached is retried after 10 minutes, then less and less often, up to every 6 hours). By default it follows the agents you use. |
| **Activity** | A 53-week calendar of the days you used an agent, with the current and longest streak, active days and the busiest day. |
| **Sessions** | Today's or the week's sessions with their totals; open one for every request: tokens, speed, time to first token and cost (read again from the session's own log). |
| **Reminders** | Desktop notifications when a quota reaches 80 % / 95 %, when it would run out before resetting, when the day's or month's spend reaches 80 % / 100 % of a budget, and (opt-in) a new speed record; each once per window, day or month. |
| **Budget, currency, export** | Daily and monthly budgets with progress on the overview; costs shown in USD or 12 other currencies at a rate you set; CSV or JSON export by day, agent and model to Downloads. |
| **System** | One instance at a time (opening it again shows the panel), start at login (LaunchAgent, the Run key, XDG autostart), a global shortcut for the panel, an opt-in daily update check against GitHub releases that can install the update in place (download, SHA-256 check against the release, swap, restart; automatically if you turn that on), an opt-in daily price-table update, and an in-app editor for custom prices. |
| **Where logs are read** | More log folders per agent, more home folders (another user's, a mounted machine's, a backup) searched like your own, and on Windows the agents inside WSL. Changing an agent's folders rereads its history once. |
| **Plans vs API price** | Enter what each plan costs a month; the overview shows how much API-priced use the last 30 days bought, as a multiple of the fee. |
| **Context** | How full each active session's context window is (used / limit), per session. |
| **Quotas** | Claude's 5-hour and weekly limits (through Claude Code's status line, when turned on), Codex's 5-hour and weekly limits, Grok Build's credits and Cursor's monthly included usage (when Cursor is turned on), with when each resets and its **pace**: whether you use it faster than time passes, and when it runs out at that rate. |

Every agent has its own colour across the panel. Settings choose the language (English, 简体中文 or the system's), what the menu bar shows (live speed, speed and today's cost, today's cost, or the icon alone), the appearance, and which agents are read, in what order they are listed and which ones the overview leaves out (still counted). Replies still being written are flagged, since their numbers can change.

### Supported agents

| Agent | Read from | Speed | Usage, cost | Context | Quota |
| --- | --- | --- | --- | --- | --- |
| Claude Code | `~/.claude/projects` (`CLAUDE_CONFIG_DIR`) | yes | yes | yes | 5-hour, weekly (status line, opt-in) |
| Codex | `~/.codex/sessions`, `archived_sessions` incl. `.jsonl.zst` (`CODEX_HOME`); fast (priority) requests from Codex's own trace log | yes | yes | yes (its own window) | 5-hour, weekly |
| Gemini CLI | `~/.gemini/tmp` (`GEMINI_CLI_HOME`), JSONL sessions (v0.39+) | yes | yes | yes | — |
| Grok Build | `~/.grok/sessions` (`GROK_HOME`) | yes | yes | yes | credits |
| Kimi Code | `~/.kimi-code/sessions` (`KIMI_CODE_HOME`) | yes | yes | — | — |
| OpenCode | `~/.local/share/opencode/opencode.db` (`XDG_DATA_HOME`), read-only | yes | yes | yes | — |
| Pi | `~/.pi/agent/sessions` (`PI_CODING_AGENT_DIR`) | yes | yes | yes | — |
| Oh My Pi | `~/.omp/agent/sessions` | yes | yes | yes | — |
| DeepSeek Harness (dsh) | `~/.dsh/sessions` (`DSH_HOME`), zstd-compressed | yes | yes | yes | — |
| Factory Droid | `~/.factory/sessions` (transcript + counters) | yes | yes | — | — |
| Antigravity | `~/.gemini/antigravity*/conversations/*.db`, read-only | yes | yes | — | — |
| Qoder, Qoder CLI | Qoder's `local.db`; `~/.qoder/projects` | yes | yes | yes | — |
| WorkBuddy, WorkBuddy AI | `~/.workbuddy/projects`, `~/.workbuddy-ai/projects` | yes | yes | — | — |
| ZCode | `~/.zcode/cli/db/db.sqlite`, read-only | yes | yes | — | — |
| Hermes Agent | `~/.hermes/logs` (`HERMES_HOME`) | yes | yes | — | — |
| OpenClaw | `~/.openclaw/agents/*/agent/openclaw-agent.sqlite` | yes | yes | — | — |
| Your own (plugins) | wherever you say, see [docs/plugins.md](docs/plugins.md) | if timed | yes | if logged | if reported |
| Cursor *(online, off by default)* | cursor.com, with the sign-in Cursor keeps locally | — (Cursor logs no timing) | yes | — | monthly |

**Claude limits.** Claude Code passes its subscription limits only to its status-line command. Turning on *Claude subscription limits* in Settings (or `code-speed claude-quota on`) makes code-speed that command: it records the limits and then runs the status line you had, so what you see in Claude Code doesn't change; turning it off puts your status line back. It edits only `statusLine` in Claude Code's `settings.json`, keeps a backup, and takes 20–40 ms per reply. Claude Desktop never runs the status line, so only CLI use is seen.

Gemini CLI's legacy whole-file sessions (v0.38 and older) are not read. Cursor keeps no token log on disk: when you turn it on in Settings, code-speed asks cursor.com for your usage with the sign-in the Cursor app already has.

## Install

On macOS or Linux, one line:

```sh
curl -fsSL https://raw.githubusercontent.com/daemonx666/code-speed/main/install.sh | sh
```

It picks the build for your system and chip (macOS: one universal app for Apple silicon and Intel; Linux: the `.deb` or `.rpm` through your package manager, or the binary in `~/.local/bin`, and the terminal build when there is no desktop), checks every download against the release's `SHA256SUMS` and that file's signature, and installs. Add `-s -- --cli` for the terminal build only, `--version v0.8.0` for a given release, and `--uninstall` (`--purge` also removes settings and history) to remove it.

With a package manager:

```sh
brew install --cask daemonx666/tap/code-speed            # macOS (Homebrew)
scoop bucket add code-speed https://github.com/daemonx666/scoop-bucket
scoop install code-speed                                 # Windows (Scoop)
winget install daemonx666.code-speed                     # Windows (winget)
yay -S code-speed-bin                                    # Arch Linux (AUR)
```

Or download from [Releases](https://github.com/daemonx666/code-speed/releases):

| System | File | |
| --- | --- | --- |
| macOS 13+ (Apple silicon and Intel) | `code-speed-<ver>-macos-universal.dmg` | Drag to Applications. |
| Windows 10/11 (x64, arm64) | `code-speed-<ver>-windows-<arch>-setup.exe` | Per-user install, no admin prompt. A `-portable.zip` is there too. |
| Debian, Ubuntu 22.04+ | `code-speed_<ver>_<arch>.deb` | `sudo apt install ./code-speed_*.deb` |
| Fedora, openSUSE | `code-speed-<ver>-1.<arch>.rpm` | `sudo dnf install ./code-speed-*.rpm` |
| Other Linux | `code-speed-<ver>-linux-<arch>.tar.gz` | Needs GTK 3 and WebKitGTK 4.1. |
| Terminal only, any OS | `code-speed-cli-<ver>-<os>-<arch>` | One static binary, no webview. |

> **macOS:** builds are signed ad hoc, not notarised. The first time, right-click the app → Open, or run
> `xattr -dr com.apple.quarantine /Applications/code-speed.app`.
>
> **Windows:** WebView2 ships with Windows 10 and 11. SmartScreen may warn about an unsigned installer: More info → Run anyway.

## Use

Open the app: an icon appears in the menu bar (macOS) or system tray (Windows, Linux). Click it for the panel; right-click for Quit.

The same binary is a command-line tool:

```
code-speed            open the app (the terminal-only build runs watch)
code-speed watch      one live line, refreshed every second
code-speed today      today's usage and cost by agent and model
code-speed week       the last 7 days
code-speed stats [day|week|month|year|all] [agent|model|project]
code-speed claude-quota on|off|status
code-speed quota      subscription limits and when they reset
code-speed json       everything as one JSON snapshot
```

```
$ code-speed today
Today (2026-11-14): 41.2M tokens · $18.70 · 352 requests · avg 104 tok/s
Last reply: claude-sonnet-5-5, 812 tokens at 97.3 tok/s (Claude Code)

By agent
               Requests  Input  Cached  Output   Avg speed    Cost
  Claude Code       301  37.9M   35.8M    264K   109 tok/s  $15.92
        Codex        51  3.03M   2.41M   41.5K  71.8 tok/s   $2.78
```

`CLAUDE_CONFIG_DIR` and `CODEX_HOME` are honoured when your agents keep their files elsewhere.

## Plugins, custom prices and API keys

- **Plugins.** Add an agent code-speed doesn't know without rebuilding it: a folder with a `plugin.json` in the plugins folder. A `jsonl` plugin says which log lines are replies and where their model, times and tokens are (no code); a `command` plugin is a program in any language that prints JSON lines (responses, quotas, sessions), off until turned on since it runs code. See [docs/plugins.md](docs/plugins.md) and [examples/plugins](examples/plugins); `code-speed plugin test <folder>` checks one.
- **Custom prices.** `custom-prices.json` adds or overrides model prices (an API relay's model names, a private deployment) and scales what an agent or provider costs (a relay at 30 % of list price). `code-speed prices` shows it.
- **API keys, relays and clouds.** Claude Code and Codex write the same logs on a subscription, an API key, a relay (`ANTHROPIC_BASE_URL`, a Codex `model_provider`), Bedrock or Vertex, so speed, usage, context and cost work the same; a relay's own model names need a custom price.

## How the numbers are made

code-speed never sits between your agent and its API. It reads the transcripts the agents already write, as they write them.

- **Speed of a reply** = output tokens ÷ (time the usage was written − time the request was sent). It includes time to first token, so it is end-to-end speed, not decode speed. Replies over 15 minutes don't count as speed; under 0.5 s count as 0.5 s.
- **Live rate** = output tokens spread over each reply's interval, summed over the last 3 seconds. A reply that is still being written has no usage yet, so its share is *estimated* from that agent's recent speed (else the model's, else the agent kind's) and marked as estimated.
- **Duplicates**: resumed and forked sessions copy their history into new files; each reply counts once.
- **Cost** is the API list price of the tokens, whatever your plan actually bills. A model with no known price counts as *unpriced*, never as $0. Prices come from a bundled table (202 models, 13 vendors) with long-context tiers, fast mode, dated price changes and off-peak hours.
- **Codex quota** is read from the rate limits Codex writes into its own logs after each reply, so it is as fresh as your last Codex request.

The quota pace follows CodexBar's (MIT): the expected use is the share of the window gone; more than 2 points over it is ahead of pace. The measurement rules and the price table come from [Agent Speed](https://github.com/zoefix/agent-speed)'s specification (MIT); the pricing code passes its 95 test cases unchanged.

## Compared with CodexBar's costs

Same 7 days of real logs, both tools local: the tokens agree; the cost differs where the rules do.

| | code-speed | CodexBar |
| --- | --- | --- |
| Claude Haiku 5.5 over 100K input | the whole reply at the long-context price (Anthropic: $0.50 / $2.50 per M) | the short-context price |
| Codex fast (priority) requests | fast price, from the thread's service tier and Codex's trace log | the same, plus priority turns it remembered from earlier scans of the trace log, which keeps only about a day |
| GPT-6.1 Terra in fast mode | the table's fast price | the standard price |
| Archived Codex sessions (`.jsonl.zst`) | read | only those it had read before they were compressed |

## Footprint

Measured on an M-series Mac with 7 months of real logs (3.8 GB, about 40,000 replies):

| | |
| --- | --- |
| macOS download (universal) | 14 MB `.dmg` |
| Terminal-only binary | 11 MB |
| First start (reads the whole history once) | ~10 s in the background, the panel shows progress |
| Later starts (history kept, only new logs read) | ~0.1 s, ~37 MB |
| Running | ~16 MB (terminal), ~60 MB (app, plus the system webview's own process); ~0 % CPU |
| History file | ~85 KB for 7 months |

About 6 MB of the binary is the pure-Go SQLite reader that OpenCode and Cursor need, and 2 MB the HTTPS client for Cursor.

It polls instead of watching the file system, identically on every OS: the few files changed in the last 15 minutes once a second, a walk for new files every 10 seconds, and databases every 5 seconds.

## Privacy

code-speed reads the agents' logs and databases and never writes to them (databases are opened read-only). It makes no network requests, except to cursor.com when you turn Cursor on, to api.github.com when you turn the update check on, to raw.githubusercontent.com (or its jsDelivr mirror) for Agent Speed's public price table when you turn the price-table update on, to the providers' public status pages while status checks are on (the default; off in Settings), and to github.com when you install an update. That one is a plain GET of the same file for everyone; a table is used only if it passes its own tests and is newer than the built-in one.

It writes only its own folder (`~/Library/Application Support/code-speed`, `%AppData%\code-speed`, `~/.config/code-speed`; `CODE_SPEED_HOME` moves it):

| File | What |
| --- | --- |
| `settings.json` | your settings |
| `history.json` | per-day totals of finished days (tokens, cost, requests per agent, model, project and hour); no prompts or replies |
| `alerts.json` | which reminders have fired, so a restart doesn't repeat them |
| `prices/` | only with the price-table update on: the downloaded table and when it was last checked |
| `quota/` | only with Claude limits on: the limits Claude Code last reported, your previous status line and a backup of Claude Code's `settings.json` |

The desktop app's system webview keeps its own small site-data folder, as every app built on it does (Wails has no switch to turn it off):

| System | Folder | |
| --- | --- | --- |
| macOS | `~/Library/WebKit/io.github.daemonx666.code-speed`, `~/Library/Caches/io.github.daemonx666.code-speed` | under 1 MB |
| Windows | `%APPDATA%\code-speed.exe` (WebView2's user data) | removed by the uninstaller |
| Linux | WebKitGTK's folders under `~/.local/share` and `~/.cache` | |

## Build from source

Go 1.26+.

```
make cli            # terminal-only binary, no cgo
make build          # desktop app for this machine
make test           # gofmt, vet and tests for both builds
make dev-ui         # the panel in a browser at http://127.0.0.1:47811, for UI work
make dmg            # macOS universal .app, .dmg and .zip
make release-cli    # terminal binaries for all 6 OS/arch targets
make release-windows  # Windows .exe (cross-compiles from any OS); setup.exe if makensis is installed
make release-linux  # Linux build, .deb, .rpm and .tar.gz (on Linux)
```

Linux needs the webview headers: `sudo apt install libgtk-3-dev libwebkit2gtk-4.1-dev` (Debian, Ubuntu) or `sudo dnf install gtk3-devel webkit2gtk4.1-devel` (Fedora).

Pushing a tag `v*` runs [`.github/workflows/release.yml`](.github/workflows/release.yml), which builds every package above on GitHub's runners and publishes them with a `SHA256SUMS`.

## How it is built

One Go binary. Each agent is a package that registers itself; adding one is a parser (or a poller, for a database or an API) and its tests. The desktop shell is [Wails v3](https://github.com/wailsapp/wails): the system webview (WebKit on macOS, WebView2 on Windows, WebKitGTK on Linux), so no browser engine is bundled. The panel is plain HTML, CSS and JavaScript with no framework, reading one JSON endpoint. A `nogui` build tag drops the webview for a pure-Go terminal binary. The layout follows [magpie](https://github.com/yetone/magpie).

```
main.go, gui_on.go, gui_off.go   entry point; the build tag picks app or terminal
internal/source     agent registry, log tailing, Claude Code and Codex parsers
internal/source/*   one package per agent (gemini, grok, kimi, opencode, pi, cursor)
internal/engine     live rate, speeds, per-day usage in every dimension, periods, history file
internal/pricing    price table (prices.json) and cost rules
internal/monitor    reads the history, then follows the logs and polls the databases
internal/service    settings and the running monitor
internal/gui        tray, panel window, floating orb, the panel's page (assets/: en and zh-CN)
internal/status     providers' public status pages
internal/update     release check and in-place install
internal/cli        commands and formatting
build/              icons, Info.plist, .desktop, nfpm, NSIS installer, devui
packaging/          Homebrew cask, Scoop, winget and AUR manifests (render.sh)
```

## Roadmap

- Usage of several machines in one view, through a folder you already sync (no server).
- Developer ID signing and notarisation; a signed Windows installer.
- More agents: Copilot CLI, Qwen Code, Cline, Goose.

## Credits

- [Agent Speed](https://github.com/zoefix/agent-speed): measurement specification, price table and test fixtures (MIT).
- [magpie](https://github.com/yetone/magpie): the Go + Wails v3 architecture this follows.
- [Token Monitor](https://github.com/Javis603/token-monitor) and [CodexBar](https://github.com/steipete/CodexBar): studied for their usage and quota features.

See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## License

[MIT](LICENSE)
