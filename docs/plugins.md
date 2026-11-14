# Plugins and custom prices

code-speed reads many agents out of the box. For one it doesn't know, add a **plugin**: a folder in the plugins folder with a `plugin.json`. No rebuild. For an API relay or a private deployment whose prices differ from the list prices, add **custom prices**.

| | macOS | Windows | Linux |
| --- | --- | --- | --- |
| Plugins folder | `~/Library/Application Support/code-speed/plugins` | `%AppData%\code-speed\plugins` | `~/.config/code-speed/plugins` |
| Custom prices | `…/code-speed/custom-prices.json` | | |

`CODE_SPEED_HOME` moves the whole folder. After adding or changing a plugin or the prices file, press **Reload** in Settings (or restart the app).

There are two kinds of plugin.

## 1. `jsonl`: an agent that writes JSON lines

Most agents keep one JSON object per line per session. Say where the files are and where each reply's fields are; code-speed tails the files like its own sources (speed, usage, cost, context, history, all of it).

```json
{
  "id": "acme",
  "label": "Acme Agent",
  "color": "#0ea5e9",
  "type": "jsonl",
  "roots": ["~/.acme/sessions", "$ACME_HOME/sessions"],
  "match": "*.jsonl",
  "record": {
    "when": { "path": "kind", "equals": ["reply"] },
    "requestStart": { "path": "kind", "equals": ["user", "tool_result"] },
    "key": ["id"],
    "time": "ts",
    "model": "model",
    "project": "cwd",
    "tokens": {
      "input": "usage.input_tokens",
      "cachedInput": "usage.cache_read_input_tokens",
      "cacheWrite": "usage.cache_creation_input_tokens",
      "output": "usage.output_tokens"
    },
    "inputExcludesCache": true
  }
}
```

| Field | Meaning |
| --- | --- |
| `id` | Lowercase letters, digits, `.`, `_`, `-`. The agent appears as `plugin.<id>`. |
| `roots` | Folders walked for files (recursively). `~` is your home; `$VARS` expand. |
| `match` | Glob on the file name. Default `*.jsonl`. |
| `record.when` | Which lines are replies: `path` equals one of `equals`, or (no `equals`) exists. Without it, any line with tokens. |
| `record.requestStart` | Lines that start a request (a user message, a tool result). The next reply's speed is timed from the latest one. Or give `record.start`, a path to the request's start time on the reply line. |
| `record.key` | Paths tried in order for the reply's id. Lines with the same id merge (streaming updates); the largest count of each token kind wins. |
| `record.time` | When the reply ended: an RFC 3339 string, or Unix seconds or milliseconds. |
| `record.model`, `record.provider`, `record.project` | Model id (used for the price), provider, working folder (its last part is shown). |
| `record.tokens.*` | `input`, `cachedInput`, `cacheWrite`, `cacheWrite1h`, `output`, `reasoning`. |
| `record.inputExcludesCache` | The input count leaves cache reads and writes out (Anthropic's convention): add them. OpenAI's convention includes them: leave this off. |
| `record.reasoningExcluded` | Output leaves reasoning out: add it. |
| `record.final` | Optional path that is true on a reply's last line. Without it every reply line is final. |

Paths use [gjson syntax](https://github.com/tidwall/gjson/blob/master/SYNTAX.md): `message.usage.input_tokens`, `items.0.id`, `content.#(type=="text").text`.

## 2. `command`: anything else

When the data is in a database, behind an API, or needs real logic, write a program in any language. code-speed runs it every `every` seconds, in the plugin's folder, and reads what it prints: one JSON object per line.

```json
{
  "id": "my-usage",
  "label": "My Usage Feed",
  "color": "#a855f7",
  "type": "command",
  "command": ["./usage.sh"],
  "every": 60,
  "timeout": 60,
  "online": false
}
```

A relative program is looked up in the plugin's folder (`python3`, `node`, `bun` are found on `PATH`: `["python3", "feed.py"]`). Its environment has:

| Variable | |
| --- | --- |
| `CODE_SPEED_SINCE_MS` | Report what ended at or after this time (Unix ms). The first run asks for the whole history. |
| `CODE_SPEED_STATE_DIR` | A folder of its own (`.state/` in the plugin folder) to keep a cursor between runs. |
| `CODE_SPEED_PLUGIN_DIR` | The plugin's folder. |

Lines it may print:

```json
{"type":"response","key":"r-1","startMs":1794627000000,"endMs":1794627004000,"model":"gpt-6.1-terra","project":"shop","session":"s1","input":5000,"cachedInput":4000,"cacheWrite":0,"output":300,"reasoning":0,"fast":false}
{"type":"quota","plan":"pro","windows":[{"kind":"fiveHour","usedPercent":42,"windowMinutes":300,"resetsAtMs":1794630600000}]}
{"type":"session","session":"s1","project":"shop","model":"gpt-6.1-terra","phase":"generating","inFlightMs":1794627004500,"context":5000,"contextWindow":272000}
```

- A `response` needs `key` and `endMs`. `input` is all input, cache included; `output` includes reasoning. Print a key again to update it (it replaces the earlier one). Without `startMs` there is no speed, only usage.
- A `quota` window's `kind` is `fiveHour`, `sevenDay`, `month` or anything else; the pace is worked out from `usedPercent`, `windowMinutes` and `resetsAtMs`.
- A `session` feeds the Sessions list: `phase` is `generating`, `tools` or `idle`.
- Set `"online": true` if it talks to the network, so Settings says so.

Because it runs a program, a command plugin is **off until you turn it on** in Settings → Agents.

## Checking a plugin

```
code-speed plugins                                   # what's in the folder, and why any can't load
code-speed plugin test ./acme ./acme/sample.jsonl     # parse a sample file with a jsonl plugin
code-speed plugin test ./my-usage                     # run a command plugin once
```

Both examples live in [`examples/plugins`](../examples/plugins).

## Custom prices

`custom-prices.json` adds prices on top of the bundled table. Model entries have the bundled table's form (USD per million tokens; optional `tiers`, `fast`, `periods`, see `internal/pricing/prices.json`) and win over it. Multipliers scale what a whole agent or provider costs: a relay that charges 30 % of list price, a team discount.

```json
{
  "models": [
    { "id": "my-relay/claude-opus", "aliases": ["opus-relay"],
      "prices": { "input": 1.2, "cachedInput": 0.12, "cacheWrite": 1.5, "output": 6 } },
    { "id": "claude-opus-5-5",
      "prices": { "input": 4, "cachedInput": 0.2, "cacheWrite": 5, "cacheWrite1h": 8, "output": 20 },
      "tiers": [] }
  ],
  "multipliers": {
    "sources": { "claude-code": 0.3 },
    "providers": { "my-relay": 0.5 }
  }
}
```

- An agent's multiplier wins over its provider's. Sources are the ids in Settings (`claude-code`, `codex`, `plugin.acme`, …); providers are what the agent records (Codex's `model_provider`, OpenCode's `providerID`, …).
- A model nobody prices stays *unpriced*, never $0, multiplier or not.
- New prices apply to replies read from then on. Finished days already in the history file keep the price they had; to price the past again, quit code-speed, delete `history.json`, and start it (it rereads every log).

`code-speed prices` shows the file's path and whether it loads.

## Agents on an API key, a relay or a cloud

Claude Code and Codex write the same session logs whether they use a subscription, an API key, a relay (`ANTHROPIC_BASE_URL`, a Codex `model_provider`), Bedrock or Vertex. So speed, usage, context and cost work the same way:

- **Speed** is timed from the logs, so it is what you got from that endpoint.
- **Cost** uses the model id: a standard id (`claude-opus-5-5`, `us.anthropic.claude-opus-5-5`, `gpt-6.1-terra`) is priced at the list price; a relay's own model name is unpriced until you add it to `custom-prices.json`; a relay's discount is a multiplier.
- **Quotas** are a subscription's; an API key has none to show.
