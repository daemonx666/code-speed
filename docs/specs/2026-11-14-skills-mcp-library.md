---
title: Skill 与 MCP 库
status: done
created: 2026-11-14
project: code-speed
---

# Skill 与 MCP 库

> 一句话需求：可以管理所有的 skill 和 mcp，自由移除，复制给任意的 agent 可识别的位置。

## 1. 现状与定位
- 项目定位：code-speed 是常驻托盘/菜单栏的桌面应用（Go + Wails v3），读取各编码 agent 的本地日志，显示速度、用量、费用和额度。给同时用多个 agent 的开发者用。
- 为什么做：同一个 skill 或 MCP server 要在 Claude Code、Codex、Gemini CLI、OpenCode……里各配一遍，格式各不相同；删掉或换掉一个也要逐个去改。code-speed 已经知道本机装了哪些 agent，适合做统一入口。

相关现有功能：
| 功能 | 位置 | 现在怎么工作 | 与本需求的关系 |
|---|---|---|---|
| Agent 注册表 | `internal/source/registry.go`、`internal/source/*` | 每个 agent 一个包，`Spec` 里有名称、颜色、日志根目录（含 env 覆盖、WSL） | 复用：agent 名称、颜色、主目录发现（`CLAUDE_CONFIG_DIR`、`CODEX_HOME`……） |
| Claude 状态栏桥接 | `internal/claudebridge/bridge.go` | 唯一会写 agent 配置的地方：先备份，写临时文件后原子替换，保留键顺序（`tidwall/pretty`、`sjson`） | 复用写法；本功能把“只写一处”扩展为“按用户操作写多处” |
| 应用目录 | `internal/appdir/appdir.go` | `settings.json`、`history.json`……都在这里，`CODE_SPEED_HOME` 可改位置 | 扩展：新增 `library.json`、`library/skills/`、`backups/` |
| 面板与接口 | `internal/gui/handler.go`、`internal/gui/assets/` | 400px 下拉面板，4 个标签，纯 HTML/CSS/JS，读 `/api/*` JSON | 扩展：新增接口；新窗口复用同一套样式和 i18n |
| 窗口 | `internal/gui/app.go`、`orb.go` | 面板窗口挂在托盘上，悬浮球是第二个窗口 | 扩展：新增第三个窗口 `library` |
| 隐私承诺 | `README*.md`「隐私」 | “只读取 Agent 的日志和数据库，从不写入” | **冲突**：本功能会按用户操作写 agent 的配置，README 需改写为“只在你操作时写，写前备份” |

## 2. 对标参考
| 产品 | 做法 | 借鉴 | 不采用 |
|---|---|---|---|
| magpie（`/Users/tu/code/github/magpie/internal/library`，MIT） | 中心库 `library.json`，每项勾选给哪些 agent，同步时按各 agent 的格式写入，`Applied` 记录写过什么，只收回自己写的 | 中心库 + 勾选同步模型；`targets.go` 的各 agent 位置；`mcp.go` 的格式编解码；`edit` 包的保格式修改；项目级位置表 `project_mcp.go` | 指令文件、hooks、子 agent、插件、市场、网关路由、跨机同步等（4 万行，远超本需求） |
| cc-switch（未联网核实） | 一张 MCP 列表，每项按 Claude/Codex/Gemini 开关；skill 从仓库安装后分发 | 「条目 × agent」开关矩阵的交互 | 只覆盖少数 agent |
| 各 agent 自带命令（`claude mcp add` 等） | 只管自己 | 无 | 无法跨 agent |

结论：照 magpie 的中心库模型做，但只做 skill 和 MCP 两类，并且允许导入用户已有的条目后自由移除（magpie 不碰用户原有条目）。

## 3. 目标与非目标
目标：
- G1 一个窗口里看到本机所有已支持 agent 的用户级 skill 和 MCP server，以及它们在哪些 agent 里。
- G2 任意条目一次勾选即可发给任意已支持 agent（含指定项目），按该 agent 的格式写入；取消勾选即从该 agent 移除。
- G3 每次写 agent 文件前都有备份，可一键还原；不破坏 agent 文件里的其他内容和注释。

非目标（本版不做）：
- 指令文件（CLAUDE.md、AGENTS.md）、hooks、子 agent、Claude Code 插件、环境变量的管理。
- skill/MCP 市场、从 URL 或 git 仓库安装 skill。
- 多机同步、MCP 路由网关。
- 符号链接分发（skill 一律复制）。
- 修改 Claude Code 插件自带的 skill 和 server（只读展示）。

## 4. 用户与场景
- 用户：同时用 2 个以上编码 agent 的开发者。
- 主流程：
  1. 托盘菜单或设置页点「Skill 与 MCP」，打开库窗口。
  2. 首次打开时扫描各 agent，列出「未入库」的条目（及它们所在的 agent）。用户全部或逐项「导入」。
  3. 在 MCP 或 Skill 标签里选一个条目，右侧勾选要给的 agent（和项目），勾上即写入。
  4. code-speed 备份涉及的文件，按格式写入，底部提示写入了哪些 agent（跳过或失败的写明原因）。
  5. 取消勾选某个 agent，即从那个 agent 移除；在库里删除条目，即从所有 agent 移除。
  6. 写错了在「备份」里按时间还原。

## 5. 功能需求

### FR-1 发现 agent 及其位置
- 描述：列出本机已安装、且已知 skill 目录或 MCP 文件位置的 agent（用户级），每个给出 skill 目录、MCP 文件路径和格式。
- 规则：
  - 位置和格式取自 magpie `library/targets.go`（只取下表的 agent），尊重各自的 env 覆盖（`CLAUDE_CONFIG_DIR`、`CODEX_HOME`、`QWEN_HOME`、`COPILOT_HOME`、`GOOSE_PATH_ROOT`、`CLINE_DATA_DIR`……）。
  - 判断「已安装」：其配置目录存在。未安装的 agent 折叠在「未安装（n）」里，不能勾选；已安装的排在前面。
  - 首版 agent（用户级）：

    | Agent | Skill 目录 | MCP 文件（格式） |
    |---|---|---|
    | Claude Code | `~/.claude/skills` | `~/.claude.json` `mcpServers`（claude） |
    | Codex | `~/.codex/skills` | `~/.codex/config.toml` `[mcp_servers]`（codex，TOML） |
    | Gemini CLI | `~/.gemini/skills` | `~/.gemini/settings.json` `mcpServers`（gemini） |
    | Qwen Code | `~/.qwen/skills` | `~/.qwen/settings.json` `mcpServers`（gemini） |
    | OpenCode | `<config>/skills`（也读 Claude 的） | `opencode.json` `mcp`（opencode） |
    | Cursor | `~/.cursor/skills` | `~/.cursor/mcp.json`（cursor） |
    | Copilot CLI | `$COPILOT_HOME/skills`（默认 `~/.copilot`，也读 `~/.agents/skills`） | `$COPILOT_HOME/mcp-config.json` `mcpServers`（copilot：本地 `type:"local"`，远程 `type:"http"`） |
    | Kimi Code | 见 magpie（读第一个存在的目录） | `mcp.json`（kimi） |
    | Pi / Oh My Pi | `<agentDir>/skills` | `mcp.json`（piNative / omp） |
    | Factory Droid | `~/.factory/skills` | `~/.factory/mcp.json`（claude） |
    | Grok Build | `~/.grok/skills` | grok 配置（grok） |
    | Qoder CLI | `<dir>/skills` | settings（omp） |
    | Antigravity | `~/.gemini/config/skills` | `mcp_config.json`（antigravity） |
    | ZCode | `~/.zcode/skills` | `cli/config.json` `mcp.servers`（zcode） |
    | Hermes Agent | `<home>/skills` | `config.yaml` `mcp_servers`（hermes，YAML） |
    | Goose | `~/.agents/skills` | `config.yaml` `extensions`（goose，YAML） |
    | Cline | `~/.cline/skills` | `<dataDir>/settings/cline_mcp_settings.json`（cline） |
    | WorkBuddy | `<dir>/skills` | `<dir>/mcp.json`（claude） |
    | 共享目录 | `~/.agents/skills`（多个 agent 读） | — |

  - DeepSeek Harness 的 MCP 在 profile 补丁层里，首版只管它的 skill。
- 验收：
  - [ ] Given 本机装了 Claude Code 和 Codex，When 打开库窗口，Then 两者显示为可用，并显示各自的 skill 目录和 MCP 文件路径。
  - [ ] Given 设置了 `CODEX_HOME=/x`，Then Codex 的位置是 `/x/skills`、`/x/config.toml`。
  - [ ] Given 某 agent 未安装，Then 它显示为灰色，不能勾选。

### FR-2 扫描与导入
- 描述：读出每个 agent 现有的 skill（含 `SKILL.md` 的文件夹）和 MCP server，与库比对；不在库里的列为「未入库」，可全部或逐项导入。
- 规则：
  - MCP 按「名称 + 规范化内容」（命令、参数、env 键、url、headers 键）去重：多个 agent 里相同的算一项，记录它在哪些 agent 里。同名但内容不同的列为冲突，导入时让用户选一个版本，另一个改名导入或跳过。
  - Skill 按文件夹名 + 内容哈希去重，同上处理冲突。skill 的名称和描述读自 `SKILL.md` 的 frontmatter。
  - 导入：MCP 存进 `library.json`；skill 复制进 `library/skills/<name>/`。同时把它当前所在的 agent 记为「已勾选」和「已写入」（`Applied`），之后取消勾选即可从那个 agent 移除。
  - Claude Code 插件里的 skill 和 server 只读展示（标「插件」），不能导入或移除。
- 验收：
  - [ ] Given `~/.claude.json` 和 `~/.cursor/mcp.json` 里都有内容相同的 `github` server，When 扫描，Then 列出一项 `github`，所在 agent 为 Claude Code、Cursor。
  - [ ] Given 导入 `github` 后取消勾选 Cursor，Then `~/.cursor/mcp.json` 里没有 `github`，其他 server 和格式不变。
  - [ ] Given 两个 agent 里同名 skill 内容不同，When 导入，Then 提示冲突，让用户选版本。

### FR-3 新建与编辑条目
- 描述：在库里新建或编辑 MCP server（名称、stdio 的命令/参数/env，或远程的 url/headers 和 http/sse 传输）；新建 skill（选一个本地文件夹或 `SKILL.md` 复制进库）；编辑后可重新应用。
- 规则：
  - 名称在库内唯一，只允许各 agent 都接受的字符（字母、数字、`-`、`_`）。
  - env 和 headers 的值界面上默认遮住，可点开查看。`library.json` 权限 0600。
  - 某 agent 不支持某种传输（如 Goose 不支持 SSE、Codex 项目级不支持 SSE）时，勾选框禁用并说明原因（规则取自 magpie `mcpFile.supports`）。
- 验收：
  - [ ] Given 新建一个 SSE server，Then Goose 的勾选框禁用并显示原因。
  - [ ] Given 编辑一个已发给 3 个 agent 的 server 的参数并保存，Then 3 个文件都更新为新参数。

### FR-4 勾选与应用（同步）
- 描述：每个条目有一组 agent 勾选（用户级）和项目勾选（FR-6）。勾选、取消勾选、编辑保存、采用 agent 版本都立即对这一条目生效（像 magpie）：新勾的写入，内容变了的覆盖，取消勾选的移除。每组有「全部」开关，一次勾上或取消该组所有可用的 agent。顶部「应用 N 项变更」只在还有遗留（写入失败、从库里删掉但还在 agent 里）时出现。
- 规则：
  - 只改涉及的条目；agent 文件里的其他内容、键顺序、注释和缩进保持不变（JSON/JSONC、TOML、YAML 都用 magpie `edit` 包的保格式修改）。
  - skill 写入：把库里的文件夹完整复制到 agent 的 skill 目录（先复制到临时目录再改名替换）。
  - 移除：只移除库里登记过的条目（导入或写入过的），从不碰库外条目。
  - 漂移：应用前若发现 agent 文件里该条目与上次写入的不同（用户在 agent 里改过），不覆盖，标为「已在 agent 中修改」，让用户选「用 agent 的版本更新库」或「覆盖」。
  - 每个 agent 单独报告结果：已写入 / 已移除 / 无变化 / 跳过（原因）/ 失败（错误）。一个失败不影响其他。
  - agent 正在运行时照样写；提示「重启 agent 后生效」。
- 验收：
  - [ ] Given 库里 server `fs` 勾选了 Codex，When 勾选或保存（立即写入），Then `~/.codex/config.toml` 多出 `[mcp_servers.fs]`，文件里原有的注释仍在。
  - [ ] Given 用户在 `~/.claude.json` 里手改了 `fs` 的参数，When 勾选或保存（立即写入），Then 不覆盖，`fs` 在 Claude Code 列显示「已修改」。
  - [ ] Given `~/.gemini/settings.json` 不是合法 JSON，When 勾选或保存（立即写入），Then Gemini 报「文件无法解析，未修改」，其他 agent 照常写入。

### FR-5 删除与备份还原
- 描述：在库里删除条目，即从所有 agent 和项目移除，并删除库内副本；每次应用前备份所有将被修改的文件和 skill 文件夹，可在「备份」列表里按时间还原。
- 规则：
  - 备份放在 `<appdir>/backups/<时间戳>/`，内含原文件和一个 `manifest.json`（原路径 → 备份路径、操作说明）。
  - 保留最近 30 次或 30 天，取较多者。
  - 还原：把这次备份里的文件放回原路径（若该文件之后又被改过，先再备份一次），并把库状态恢复到那次应用之前。
  - 删除条目前要确认，确认框里列出将从哪些 agent 移除。
- 验收：
  - [ ] Given 删除一个发给 4 个 agent 的 skill，When 确认，Then 4 个 skill 目录里都没有它，备份里有这 4 份。
  - [ ] Given 在备份列表里还原刚才那次，Then 4 份 skill 回到原位，库里也恢复该条目。

### FR-6 指定项目
- 描述：用户可添加若干项目文件夹；每个条目除用户级勾选外，还能勾选「发给某项目里的某 agent」，写入该 agent 在项目内认的位置。
- 规则：
  - 项目级位置取自 magpie `project_mcp.go` 的 `projectMCPFiles`，以及各 agent 的项目 skill 目录：Claude Code `.mcp.json`、`.claude/skills`；Codex `.codex/config.toml`；Gemini CLI `.gemini/settings.json`、`.gemini/skills`；Qwen Code `.qwen/settings.json`、`.qwen/skills`；Cursor `.cursor/mcp.json`、`.cursor/skills`；OpenCode `opencode.json`；Pi `.pi/mcp.json`；Qoder `.qoder/settings.json`；Droid `.factory/mcp.json`；Copilot CLI `.github/mcp.json`（也读 `.mcp.json`）、`.github/skills`；共享 `.agents/skills`。没有项目级位置的 agent 在项目里不能勾选。
  - 写入项目的文件会进入该项目的 git，界面上提示一次；env/headers 值含密钥时，写入项目前额外确认。
  - 项目扫描与导入同 FR-2，条目标注所属项目。
- 验收：
  - [ ] Given 添加项目 `~/code/app` 并给其中的 Claude Code 勾选 `fs`，When 勾选或保存（立即写入），Then `~/code/app/.mcp.json` 里有 `fs`，`~/.claude.json` 不变。
  - [ ] Given 某 server 的 env 里有值，When 勾选写入项目，Then 先弹出确认。

### FR-7 入口
- 描述：托盘菜单新增「Skill 与 MCP…」，设置页新增同名按钮，都打开库窗口；已打开时置前。命令行新增 `code-speed library`，列出库里的条目及其所在的 agent（只读）。
- 验收：
  - [ ] Given 库窗口已打开，When 再点托盘菜单项，Then 不新开窗口，只把它置前。

## 6. 数据与接口
- `<appdir>/library.json`（0600）：
  ```json
  {
    "version": 1,
    "mcp":    [{"name":"fs","transport":"stdio","command":"npx","args":["-y","@x/fs"],"env":{"K":"v"},"url":"","headers":{},
                "agents":["claude","codex"], "projects":{"/Users/u/code/app":["claude"]}}],
    "skills": [{"name":"pdf","hash":"sha256:…","description":"…","agents":["claude","shared"],"projects":{}}],
    "projects": ["/Users/u/code/app"],
    "applied": {"claude": {"mcp": {"fs":"<规范化内容哈希>"}, "skills": {"pdf":"sha256:…"}},
                "project:/Users/u/code/app:claude": {"mcp": {"fs":"…"}}}
  }
  ```
  `applied` 记录每个位置上由库写入或导入时登记的条目和当时的内容哈希，用于判断「可移除」和「漂移」。
- `<appdir>/library/skills/<name>/`：skill 的库内副本。
- `<appdir>/backups/<时间戳>/manifest.json` 及备份文件。
- HTTP 接口（库窗口用，与面板同一个 handler）：
  - `GET /api/library`：库内容、各 agent 与项目的位置和可用性、每个条目在各位置的状态（已写入、未写入、已修改、不支持）。
  - `GET /api/library/scan`：未入库条目和冲突。
  - `POST /api/library/import`、`/item`（新建或编辑）、`/delete`、`/apply`（返回逐个 agent 的结果）、`/projects`（添加或删除项目）。
  - `GET /api/library/backups`、`POST /api/library/restore`。

## 7. 界面与交互
- 新窗口 `library`：960×640，可调大小、可最小化，显示在任务栏或 Dock；沿用面板的配色、字体和 i18n（中英文）。
- 布局：顶部两个标签「MCP」「Skill」，加上「项目」「备份」入口。左栏是条目列表（搜索、未入库标记、冲突标记），右栏是详情：
  - MCP：表单（名称、传输、命令/参数/env 或 url/headers）。
  - Skill：名称、描述、文件列表、在访达或资源管理器中打开。
  - 下方是 agent 勾选网格，已安装的在前，未安装的折叠；每组标题旁有「全部」。每格显示状态：已写入（绿色）、空、「已修改」（橙色）、灰色加原因（不支持）。网格下方按项目分组，是项目勾选。
  - 每次改动后底部弹出提示：写入了哪些 agent、从哪些移除、哪些跳过及原因；导入后提示「已入库，某某里已有它」并选中该条目。
- 状态：
  - 首次打开时扫描中显示进度。
  - 没有任何 agent 时显示空态，说明支持哪些 agent。
  - 有未入库条目时，顶部横幅显示「发现 N 项未入库 · 全部导入 / 查看」。

## 8. 边界与异常
- agent 文件不存在：写入时按该 agent 的格式新建；目录不存在则建目录。
- 文件无法解析（坏 JSON 或 TOML）：该 agent 跳过并报错，不改文件。
- 文件不可写或权限不足：报错，其他 agent 继续。
- 应用期间 agent 同时写同一文件：写前再读一次比对，变了就重新合并；JSON/TOML/YAML 写入都走临时文件加原子改名。
- 同名冲突：库内名称唯一；某 agent 里已有同名的库外条目时，不覆盖，提示改名或先导入。
- skill 很大（>50 MB）或含符号链接：复制前提示；符号链接按链接复制，不跟随。
- Windows 路径、WSL：首版不处理 WSL 里的 agent（只读展示的也不做），在 Windows 上只管 Windows 本机的 agent。
- 密钥：`library.json` 0600；导出、日志里不打印 env 和 headers 的值。

## 9. 技术方案要点
- 复用：
  - `internal/source` 的 agent 名称和颜色，以及主目录发现函数（各包的 `roots` 里已有 env 覆盖）。
  - `internal/appdir`（新增路径函数）。
  - `internal/claudebridge` 的 `writeFile`（原子写）思路。
  - `internal/gui/assets` 的样式和 i18n。
- 修改：
  - `internal/gui/app.go`：创建 `library` 窗口，托盘菜单项，单实例唤起时置前。
  - `internal/gui/handler.go`：挂 `/api/library/*`。
  - `assets/index.html`、`app.js`、`i18n.js`：设置页入口按钮。
  - `internal/cli/cli.go`：`library` 子命令。
  - `README*.md`：隐私一节改为「只在你于库窗口里操作时写 agent 配置，写前备份」；功能表加一行。
  - `THIRD_PARTY_NOTICES.md`：magpie 署名范围扩大。
- 新增：
  - `internal/edit/`：从 magpie `internal/edit` 移植保格式的 JSON/JSONC、TOML、YAML 修改（只移植用到的函数及其测试）。
  - `internal/library/`：
    - `targets.go`：上表位置，从 magpie 精简。
    - `mcp.go`：`Server` 模型和各格式的 encode/decode/supports，从 magpie 精简到上表的格式。
    - `skills.go`：扫描、哈希、复制。
    - `library.go`：`library.json`、扫描比对、导入、apply 差异、`applied`、漂移。
    - `backup.go`：备份与还原。
    - `project.go`：项目级位置。
    - 各自的测试：每种格式读写往返；保留注释；移除不碰库外条目。
  - `internal/gui/assets/library.html`、`library.js`：库窗口页面。
- 依赖与风险：
  - 新依赖：`github.com/pelletier/go-toml/v2`、`gopkg.in/yaml.v3`（magpie `edit` 包所用），二进制增大约 1 MB。
  - 写 agent 配置是本项目第一次大范围写外部文件，风险集中在格式破坏。对策：每种格式都做往返和保注释测试，写前备份，写前写后都校验能否解析（写后解析失败则立即回滚该文件）。
  - 各 agent 格式会变：位置和格式表要随 magpie 和各 agent 文档更新。
  - magpie 是 MIT 许可，移植部分在 `THIRD_PARTY_NOTICES.md` 注明。

## 10. 任务拆分
- [x] T1 移植 `internal/edit`（JSON/JSONC、TOML、YAML 保格式增删改）及测试（FR-4）
- [x] T2 `internal/library/targets.go`：用户级位置表和 env 覆盖、安装判断（FR-1）
- [x] T3 `mcp.go`：`Server` 模型；claude、gemini、cursor、copilot、kimi、piNative、omp、antigravity、zcode、cline、grok、opencode、codex、goose、hermes 各格式的 decode/encode/supports 及往返测试（FR-2、FR-3、FR-4）
- [x] T4 `skills.go`：扫描 `SKILL.md`、frontmatter、内容哈希、原子复制和删除（FR-2、FR-4）
- [x] T5 `library.go`：`library.json` 读写、扫描比对和去重/冲突、导入、新建/编辑、apply 差异、`applied` 与漂移检测（FR-2～FR-5）
- [x] T6 `backup.go`：应用前备份、保留策略、还原（FR-5）
- [x] T7 `project.go`：项目列表、项目级位置、写入项目前的密钥确认标记（FR-6）
- [x] T8 `/api/library/*` 接口和 handler 测试（FR-2～FR-7）
- [x] T9 `library` 窗口、托盘菜单项、设置页入口（FR-7）
- [x] T10 `library.html`/`library.js`：列表、详情、勾选网格、应用结果、未入库横幅、项目、备份、中英文（第 7 节）
- [x] T11 `code-speed library` 命令（FR-7）
- [x] T12 README 中英文（功能、隐私）、THIRD_PARTY_NOTICES
- [x] T13 对照验收标准自查，三平台 CI 通过

## 11. 假设与待定
- 假设：
  - 界面只做独立窗口，面板里只放一个入口按钮。
  - 「已安装」以配置目录存在为准，不检测可执行文件。
  - Claude Code 插件内的条目只读展示。
  - 首版不支持 WSL 里的 agent；Claude Desktop、Zed、Kiro 等不在首版。
  - 只在用户改动某条目时写入该条目，不在后台自动同步；agent 文件被外部改动只在打开窗口或改动时检测。
  - skill 目录的 frontmatter 不合法也照样复制，只是名称回退为文件夹名。
- 待定：
  - Kimi Code 读多个目录中第一个存在的那个：写入时选哪个目录，按 magpie 的规则实现后再核对。

## 变更记录
- 2026-11-14 创建
- 2026-11-14 实现（与规格的差异）：
  - 项目级勾选没有单独的 `projects` 字段：项目位置是独立的目标，id 为 `proj:<项目路径>#<agent>`，和用户级目标一样放在条目的 `agents` 里。
  - 条目名称只允许字母、数字、`-`、`_`（各 agent 的 MCP 键路径不能含点号）。
  - Claude Code 插件里的 skill 和 server 首版没有只读展示，移到路线图。
  - Cline 的 MCP 按 Cline CLI 的嵌套 `transport` 结构写入，也能读扁平结构。
  - OpenCode、Cursor 不区分 SSE 和 http，判断「已修改」时按格式能表达的内容比较（`mcpfmt.File.Same`）。
- 2026-11-15 试用反馈后改为点一下就生效（照 magpie）：勾选只保存、点「应用」才写入，用户感觉不到导入和勾选是否成功。现在 `/api/library/agents`、`server`、`skill`、`adopt` 保存后立即做一次只限该条目的应用（`ApplyOptions.Items`）并返回结果；界面加了提示条、「全部」开关，已安装的 agent 在前、未安装的折叠。
