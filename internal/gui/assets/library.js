"use strict";

// The skill and MCP library's window. It reads /api/library (every target,
// every library item with its state in each, and what the agents hold that
// the library doesn't) and changes the library through the /api/library/*
// calls; ticking a target is saved at once, and "Apply" writes the
// agents' files.

Object.assign(STRINGS.en, {
  "lib.title": "Skills & MCP",
  "lib.tab.mcp": "MCP servers",
  "lib.tab.skill": "Skills",
  "lib.tab.projects": "Projects",
  "lib.tab.backups": "Backups",
  "lib.refresh": "Refresh",
  "lib.apply": "Apply",
  "lib.applyN": "Apply {n} changes",
  "lib.nothing": "Nothing to apply",
  "lib.search": "Search",
  "lib.new": "New",
  "lib.found": "Not in the library",
  "lib.foundBanner": "Your agents have {n} items that aren't in the library yet.",
  "lib.importAll": "Import all",
  "lib.import": "Import",
  "lib.importAs": "Import as…",
  "lib.importAsPrompt": "Another version named {name} exists. Import this one under the name:",
  "lib.conflict": "conflict",
  "lib.in": "in {list}",
  "lib.pick": "Pick an item on the left, or import what your agents already have.",
  "lib.user": "User-wide",
  "lib.project": "Project: {p}",
  "lib.notInstalled": "not installed",
  "lib.save": "Save",
  "lib.delete": "Delete",
  "lib.deleteConfirm": "Delete {name} from the library and take it out of: {list}?\n\nThe files are backed up first and can be restored.",
  "lib.deleteConfirmNone": "Delete {name} from the library?",
  "lib.name": "Name",
  "lib.transport": "Transport",
  "lib.command": "Command",
  "lib.args": "Arguments",
  "lib.argsHint": "One per line.",
  "lib.env": "Environment",
  "lib.envHint": "KEY=value, one per line. Values are kept in library.json (readable by you only).",
  "lib.url": "URL",
  "lib.headers": "Headers",
  "lib.headersHint": "Name: value, one per line.",
  "lib.show": "Show values",
  "lib.description": "Description",
  "lib.folder": "Folder",
  "lib.openFolder": "Open folder",
  "lib.replaceFrom": "Replace from folder…",
  "lib.addSkill": "Add a skill folder…",
  "lib.pickSkill": "Choose a skill folder (with a SKILL.md)",
  "lib.pickProject": "Choose a project folder",
  "lib.givenTo": "Given to",
  "lib.state.on": "in place",
  "lib.state.off": "",
  "lib.state.add": "will be written",
  "lib.state.update": "will be updated",
  "lib.state.remove": "will be removed",
  "lib.state.drift": "changed in the agent",
  "lib.state.foreign": "agent has its own",
  "lib.state.link": "link from another tool",
  "lib.state.unsupported": "not supported",
  "lib.state.error": "can't read",
  "lib.useAgents": "Use the agent's",
  "lib.overwrite": "Overwrite",
  "lib.overwriteSet": "will overwrite",
  "lib.results": "Results",
  "lib.close": "Close",
  "lib.res.written": "written",
  "lib.res.removed": "removed",
  "lib.res.skipped": "skipped",
  "lib.res.failed": "failed",
  "lib.resNone": "Nothing needed changing.",
  "lib.restartNote": "Agents that are running pick the change up when they start again.",
  "lib.projects.note": "Items ticked for a project are written into that project's own files (.mcp.json, .claude/skills, …), which go into its git. Servers with values in env or headers are confirmed before going into a project.",
  "lib.projects.add": "Add a project…",
  "lib.projects.none": "No projects yet.",
  "lib.projects.remove": "Remove",
  "lib.projects.removeConfirm": "Take the library's items out of {p} and forget it?",
  "lib.secretProject": "{name} has values in its env or headers. Write them into {p}, which may be committed to git?",
  "lib.backups.note": "Every apply, import and delete backs up the files it changes first. Restoring puts them back (and backs up what is there now).",
  "lib.backups.none": "No backups yet.",
  "lib.restore": "Restore",
  "lib.restoreConfirm": "Put back the {n} files and folders as they were at {t}?",
  "lib.entries": "{n} files",
  "lib.newServer": "New MCP server",
});
Object.assign(STRINGS["zh-CN"], {
  "lib.title": "Skill 与 MCP",
  "lib.tab.mcp": "MCP Server",
  "lib.tab.skill": "Skill",
  "lib.tab.projects": "项目",
  "lib.tab.backups": "备份",
  "lib.refresh": "刷新",
  "lib.apply": "应用",
  "lib.applyN": "应用 {n} 项变更",
  "lib.nothing": "没有要应用的变更",
  "lib.search": "搜索",
  "lib.new": "新建",
  "lib.found": "未入库",
  "lib.foundBanner": "各 agent 里有 {n} 项还没有入库。",
  "lib.importAll": "全部导入",
  "lib.import": "导入",
  "lib.importAs": "改名导入…",
  "lib.importAsPrompt": "已经有另一个叫 {name} 的版本。用这个名字导入：",
  "lib.conflict": "冲突",
  "lib.in": "在 {list}",
  "lib.pick": "在左边选一项，或者先导入各 agent 已有的。",
  "lib.user": "用户级",
  "lib.project": "项目：{p}",
  "lib.notInstalled": "未安装",
  "lib.save": "保存",
  "lib.delete": "删除",
  "lib.deleteConfirm": "从库里删除 {name}，并从这些位置移除：{list}？\n\n改动前会先备份，可以还原。",
  "lib.deleteConfirmNone": "从库里删除 {name}？",
  "lib.name": "名称",
  "lib.transport": "传输方式",
  "lib.command": "命令",
  "lib.args": "参数",
  "lib.argsHint": "每行一个。",
  "lib.env": "环境变量",
  "lib.envHint": "KEY=value，每行一个。值保存在 library.json 里（只有你能读）。",
  "lib.url": "URL",
  "lib.headers": "请求头",
  "lib.headersHint": "Name: value，每行一个。",
  "lib.show": "显示值",
  "lib.description": "描述",
  "lib.folder": "文件夹",
  "lib.openFolder": "打开文件夹",
  "lib.replaceFrom": "从文件夹替换…",
  "lib.addSkill": "添加 skill 文件夹…",
  "lib.pickSkill": "选择 skill 文件夹（含 SKILL.md）",
  "lib.pickProject": "选择项目文件夹",
  "lib.givenTo": "发给",
  "lib.state.on": "已写入",
  "lib.state.off": "",
  "lib.state.add": "将写入",
  "lib.state.update": "将更新",
  "lib.state.remove": "将移除",
  "lib.state.drift": "已在 agent 中修改",
  "lib.state.foreign": "agent 里有同名的",
  "lib.state.link": "其他工具建的链接",
  "lib.state.unsupported": "不支持",
  "lib.state.error": "无法读取",
  "lib.useAgents": "采用 agent 的版本",
  "lib.overwrite": "覆盖",
  "lib.overwriteSet": "将覆盖",
  "lib.results": "结果",
  "lib.close": "关闭",
  "lib.res.written": "已写入",
  "lib.res.removed": "已移除",
  "lib.res.skipped": "已跳过",
  "lib.res.failed": "失败",
  "lib.resNone": "没有需要改动的。",
  "lib.restartNote": "正在运行的 agent 重启后生效。",
  "lib.projects.note": "勾选给项目的条目会写进该项目自己的文件（.mcp.json、.claude/skills 等），会进入它的 git。env 或请求头里有值的 server 写入项目前会先确认。",
  "lib.projects.add": "添加项目…",
  "lib.projects.none": "还没有项目。",
  "lib.projects.remove": "移除",
  "lib.projects.removeConfirm": "把库里的条目从 {p} 移除，并不再管理这个项目？",
  "lib.secretProject": "{name} 的环境变量或请求头里有值。要写进 {p} 吗？它可能会被提交到 git。",
  "lib.backups.note": "每次应用、导入和删除前，都会先备份要改动的文件。还原会把它们放回去（还原前也会备份现在的状态）。",
  "lib.backups.none": "还没有备份。",
  "lib.restore": "还原",
  "lib.restoreConfirm": "把这 {n} 个文件和文件夹还原到 {t} 时的样子？",
  "lib.entries": "{n} 个文件",
  "lib.newServer": "新建 MCP server",
});

const $ = (id) => document.getElementById(id);
function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}
async function post(url, body) {
  const r = await fetch(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body || {}) });
  if (!r.ok) throw new Error((await r.text()).trim());
  return r.status === 204 ? null : r.json();
}
function showError(e) {
  const box = $("lib-error");
  box.hidden = !e;
  box.textContent = e ? String(e.message || e) : "";
}
function applyI18n() {
  for (const e of document.querySelectorAll("[data-i18n]")) e.textContent = t(e.dataset.i18n);
  for (const e of document.querySelectorAll("[data-i18n-ph]")) e.placeholder = t(e.dataset.i18nPh);
}

let state = null;
let view = "mcp";
let selected = null; // {kind, name} or {found: Found} or {new: "mcp"}
const overwrite = new Set(); // "kind/name/target"

const kindOf = (v) => (v === "skill" ? "skill" : "mcp");
const itemsOf = (kind) => (kind === "skill" ? state.skills : state.mcp) || [];
const targetsById = () => Object.fromEntries((state.targets || []).map((x) => [x.id, x]));
function targetName(id) {
  const x = targetsById()[id];
  if (!x) return id;
  return x.project ? `${x.label} · ${x.project.split(/[\\/]/).pop()}` : x.label;
}

async function load() {
  try {
    const r = await fetch("/api/library", { cache: "no-store" });
    if (!r.ok) throw new Error(await r.text());
    state = await r.json();
    showError(null);
  } catch (e) {
    showError(e);
    return;
  }
  render();
}

function render() {
  if (!state) return;
  const n = state.pending || 0;
  $("lib-apply").textContent = n ? t("lib.applyN", { n }) : t("lib.apply");
  $("lib-apply").disabled = !n && overwrite.size === 0;
  const found = state.found || [];
  $("lib-banner").hidden = found.length === 0;
  $("lib-banner-text").textContent = t("lib.foundBanner", { n: found.length });
  for (const b of document.querySelectorAll(".lib-tabs button")) b.setAttribute("aria-selected", String(b.dataset.view === view));
  $("lib-items").hidden = !(view === "mcp" || view === "skill");
  $("lib-projects").hidden = view !== "projects";
  $("lib-backups").hidden = view !== "backups";
  if (view === "mcp" || view === "skill") {
    renderList();
    renderDetail();
  } else if (view === "projects") renderProjects();
  else renderBackups();
}

function renderList() {
  const kind = kindOf(view);
  const q = $("lib-search").value.trim().toLowerCase();
  const list = $("lib-list");
  list.textContent = "";
  for (const it of itemsOf(kind)) {
    if (q && !it.name.toLowerCase().includes(q)) continue;
    const li = el("li", "item");
    li.append(el("span", "name", it.name));
    const cells = Object.values(it.cells || {});
    const on = cells.filter((c) => c.want).length;
    li.append(el("span", "badge", String(on)));
    if (cells.some((c) => c.state === "drift" || c.state === "foreign")) li.append(el("span", "badge warn", "!"));
    if (cells.some((c) => ["add", "update", "remove"].includes(c.state))) li.append(el("span", "badge ok", "•"));
    if (selected && selected.kind === kind && selected.name === it.name) li.setAttribute("aria-current", "true");
    li.addEventListener("click", () => { selected = { kind, name: it.name }; render(); });
    list.append(li);
  }
  const found = (state.found || []).filter((f) => f.kind === kind && (!q || f.name.toLowerCase().includes(q)));
  $("lib-found-h").hidden = found.length === 0;
  const fl = $("lib-found");
  fl.textContent = "";
  for (const f of found) {
    const li = el("li", "item");
    li.append(el("span", "name", f.name));
    li.append(el("span", "badge", String(f.in.length)));
    if (f.conflict) li.append(el("span", "badge warn", t("lib.conflict")));
    if (selected && selected.found === f) li.setAttribute("aria-current", "true");
    li.addEventListener("click", () => { selected = { found: f }; render(); });
    fl.append(li);
  }
  $("lib-new").textContent = kind === "skill" ? t("lib.addSkill") : t("lib.new");
}

function renderDetail() {
  const box = $("lib-detail");
  box.textContent = "";
  if (!selected) {
    box.append(el("p", "muted lib-empty", t("lib.pick")));
    return;
  }
  if (selected.found) return renderFound(box, selected.found);
  if (selected.new) return renderServerForm(box, null);
  const it = itemsOf(selected.kind).find((x) => x.name === selected.name);
  if (!it) {
    selected = null;
    return renderDetail();
  }
  if (it.kind === "mcp") renderServerForm(box, it);
  else renderSkill(box, it);
  renderGrid(box, it);
}

function renderFound(box, f) {
  const card = el("div", "card");
  const head = el("div", "lib-head");
  head.append(el("h1", "", f.name));
  if (f.conflict) head.append(el("span", "badge warn", t("lib.conflict")));
  card.append(head);
  card.append(el("p", "muted", t("lib.in", { list: f.in.map(targetName).join("、") })));
  if (f.description) card.append(el("p", "", f.description));
  if (f.server) card.append(el("pre", "mono", serverText(f.server)));
  const row = el("div", "row");
  const imp = el("button", "btn primary", t("lib.import"));
  imp.disabled = f.conflict;
  imp.addEventListener("click", () => doImport(f, ""));
  const as = el("button", "btn", t("lib.importAs"));
  as.addEventListener("click", () => {
    const name = prompt(t("lib.importAsPrompt", { name: f.name }), f.name + "-2");
    if (name) doImport(f, name.trim());
  });
  row.append(imp, as);
  if (f.path) {
    const open = el("button", "btn", t("lib.openFolder"));
    open.addEventListener("click", () => post("/api/library/reveal", { path: f.path }).catch(showError));
    row.append(open);
  }
  card.append(row);
  box.append(card);
}

async function doImport(f, as) {
  try {
    await post("/api/library/import", { items: [{ kind: f.kind, name: f.name, hash: f.hash, as }] });
    selected = { kind: f.kind, name: as || f.name };
    await load();
  } catch (e) { showError(e); }
}

function serverText(s) {
  if (s.transport === "stdio") return [s.command, ...(s.args || [])].join(" ") + envText(s.env, "=", true);
  return `${s.transport} ${s.url}` + envText(s.headers, ": ", true);
}
function envText(m, sep, mask) {
  const keys = Object.keys(m || {});
  if (!keys.length) return "";
  return "\n" + keys.map((k) => `${k}${sep}${mask ? "••••" : m[k]}`).join("\n");
}
function parseLines(text, sep) {
  const out = {};
  for (const line of text.split("\n")) {
    const i = line.indexOf(sep);
    if (i <= 0) continue;
    out[line.slice(0, i).trim()] = line.slice(i + sep.length).trim();
  }
  return out;
}

function field(form, label, input, hint) {
  form.append(el("label", "", label), input);
  if (hint) form.append(el("p", "muted small hint", hint));
}

function renderServerForm(box, it) {
  const s = it ? it.server : { name: "", transport: "stdio", command: "", args: [], env: {}, url: "", headers: {} };
  const card = el("div", "card");
  const head = el("div", "lib-head");
  head.append(el("h1", "", it ? it.name : t("lib.newServer")));
  card.append(head);
  const form = el("div", "lib-form");
  const name = el("input"); name.type = "text"; name.value = s.name;
  const tr = el("select");
  for (const v of ["stdio", "http", "sse"]) { const o = el("option", "", v); o.value = v; tr.append(o); }
  tr.value = s.transport || "stdio";
  const cmd = el("input"); cmd.type = "text"; cmd.value = s.command || "";
  const args = el("textarea"); args.rows = 3; args.value = (s.args || []).join("\n");
  const env = el("textarea", "secret"); env.rows = 3; env.value = envText(s.env, "=", false).trimStart();
  const url = el("input"); url.type = "text"; url.value = s.url || "";
  const headers = el("textarea", "secret"); headers.rows = 3; headers.value = envText(s.headers, ": ", false).trimStart();
  field(form, t("lib.name"), name);
  field(form, t("lib.transport"), tr);
  const stdio = [], remote = [];
  const mark = (arr, fn) => { const before = form.childElementCount; fn(); for (let i = before; i < form.childElementCount; i++) arr.push(form.children[i]); };
  mark(stdio, () => field(form, t("lib.command"), cmd));
  mark(stdio, () => field(form, t("lib.args"), args, t("lib.argsHint")));
  mark(stdio, () => field(form, t("lib.env"), env, t("lib.envHint")));
  mark(remote, () => field(form, t("lib.url"), url));
  mark(remote, () => field(form, t("lib.headers"), headers, t("lib.headersHint")));
  const sync = () => {
    for (const e of stdio) e.hidden = tr.value !== "stdio";
    for (const e of remote) e.hidden = tr.value === "stdio";
  };
  tr.addEventListener("change", sync);
  sync();
  card.append(form);
  const row = el("div", "row");
  const show = el("label", "small");
  const cb = el("input"); cb.type = "checkbox";
  cb.addEventListener("change", () => { env.classList.toggle("secret", !cb.checked); headers.classList.toggle("secret", !cb.checked); });
  show.append(cb, document.createTextNode(" " + t("lib.show")));
  const save = el("button", "btn primary", t("lib.save"));
  save.addEventListener("click", async () => {
    const server = { name: name.value.trim(), transport: tr.value };
    if (tr.value === "stdio") Object.assign(server, { command: cmd.value.trim(), args: args.value.split("\n").map((x) => x.trim()).filter(Boolean), env: parseLines(env.value, "=") });
    else Object.assign(server, { url: url.value.trim(), headers: parseLines(headers.value, ":") });
    const agents = it ? Object.entries(it.cells).filter(([, c]) => c.want).map(([id]) => id) : [];
    try {
      await post("/api/library/server", { old: it ? it.name : "", server, agents });
      selected = { kind: "mcp", name: server.name };
      await load();
    } catch (e) { showError(e); }
  });
  row.append(show, save);
  if (it) row.append(deleteButton(it));
  card.append(row);
  box.append(card);
}

function renderSkill(box, it) {
  const card = el("div", "card");
  const head = el("div", "lib-head");
  head.append(el("h1", "", it.name));
  card.append(head);
  if (it.description) card.append(el("p", "", it.description));
  card.append(el("p", "mono muted", it.path));
  const row = el("div", "row");
  const open = el("button", "btn", t("lib.openFolder"));
  open.addEventListener("click", () => post("/api/library/reveal", { path: it.path }).catch(showError));
  const rep = el("button", "btn", t("lib.replaceFrom"));
  rep.addEventListener("click", async () => {
    const { path } = await post("/api/library/pick", { title: t("lib.pickSkill") });
    if (!path) return;
    const agents = Object.entries(it.cells).filter(([, c]) => c.want).map(([id]) => id);
    try { await post("/api/library/skill", { name: it.name, path, agents }); await load(); } catch (e) { showError(e); }
  });
  row.append(open, rep, deleteButton(it));
  card.append(row);
  box.append(card);
}

function deleteButton(it) {
  const del = el("button", "btn danger", t("lib.delete"));
  del.addEventListener("click", async () => {
    const where = Object.entries(it.cells).filter(([, c]) => c.state === "on" || c.state === "update" || c.state === "remove" || c.state === "drift").map(([id]) => targetName(id));
    const msg = where.length ? t("lib.deleteConfirm", { name: it.name, list: where.join("、") }) : t("lib.deleteConfirmNone", { name: it.name });
    if (!confirm(msg)) return;
    try {
      const r = await post("/api/library/delete", { kind: it.kind, name: it.name });
      selected = null;
      await load();
      showResults(r);
    } catch (e) { showError(e); }
  });
  return del;
}

function hasSecrets(it) {
  const s = it.server;
  return s && (Object.values(s.env || {}).some(Boolean) || Object.values(s.headers || {}).some(Boolean));
}

function renderGrid(box, it) {
  const card = el("div", "card");
  card.append(el("h2", "", t("lib.givenTo")));
  const groups = new Map();
  for (const x of state.targets || []) {
    const key = x.project || "";
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(x);
  }
  for (const [project, ts] of groups) {
    const g = el("div", "grid-group");
    g.append(el("h3", "", project ? t("lib.project", { p: project }) : t("lib.user")));
    const grid = el("div", "targets");
    for (const x of ts) {
      const c = it.cells[x.id];
      if (!c) continue;
      const unusable = c.state === "unsupported" || c.state === "error";
      if (unusable && !c.want && x.skillsVia) continue; // Goose: given through ~/.agents
      const cell = el("div", "target" + (!x.installed && !c.want ? " off" : ""));
      const label = el("label");
      const cb = el("input"); cb.type = "checkbox"; cb.checked = c.want;
      cb.disabled = (unusable && !c.want) || (!x.installed && !c.want);
      cb.addEventListener("change", async () => {
        if (cb.checked && project && hasSecrets(it) && !confirm(t("lib.secretProject", { name: it.name, p: project }))) {
          cb.checked = false;
          return;
        }
        const agents = Object.entries(it.cells).filter(([id, cc]) => (id === x.id ? cb.checked : cc.want)).map(([id]) => id);
        try { await post("/api/library/agents", { kind: it.kind, name: it.name, agents }); await load(); } catch (e) { showError(e); }
      });
      label.append(cb, document.createTextNode(x.label));
      cell.append(label);
      const key = `${it.kind}/${it.name}/${x.id}`;
      let why = c.reason || t("lib.state." + c.state);
      if (!x.installed) why = t("lib.notInstalled") + (why ? " · " + why : "");
      if (overwrite.has(key)) why = t("lib.overwriteSet");
      if (why) {
        const w = el("span", "why", why);
        w.title = why;
        if (c.state === "drift" || c.state === "foreign") w.classList.add("badge", "warn");
        cell.append(w);
      }
      if (c.state === "drift" || c.state === "foreign") {
        const acts = el("div", "acts");
        const adopt = el("button", "link", t("lib.useAgents"));
        adopt.addEventListener("click", async () => {
          try { await post("/api/library/adopt", { kind: it.kind, name: it.name, target: x.id }); await load(); } catch (e) { showError(e); }
        });
        const over = el("button", "link", t("lib.overwrite"));
        over.addEventListener("click", () => { overwrite.has(key) ? overwrite.delete(key) : overwrite.add(key); render(); });
        acts.append(adopt, over);
        cell.append(acts);
      }
      grid.append(cell);
    }
    g.append(grid);
    card.append(g);
  }
  box.append(card);
}

function showResults(r) {
  const list = $("lib-results-list");
  list.textContent = "";
  const res = (r && r.results) || [];
  if (r && r.error) list.append(el("li", "", r.error));
  if (!res.length) list.append(el("li", "muted", t("lib.resNone")));
  for (const x of res) {
    const li = el("li");
    const cls = { written: "ok", removed: "ok", skipped: "warn", failed: "bad" }[x.action] || "";
    li.append(el("span", "badge " + cls, t("lib.res." + x.action)));
    li.append(el("span", "", `${x.name} → ${targetName(x.target)}`));
    if (x.reason) li.append(el("span", "muted small", x.reason));
    list.append(li);
  }
  if (res.some((x) => x.action === "written" || x.action === "removed")) list.append(el("li", "muted small", t("lib.restartNote")));
  $("lib-results").showModal();
}

async function renderProjects() {
  const list = $("lib-project-list");
  list.textContent = "";
  if (!(state.projects || []).length) list.append(el("li", "muted", t("lib.projects.none")));
  for (const p of state.projects || []) {
    const li = el("li");
    li.append(el("span", "mono", p));
    const rm = el("button", "btn", t("lib.projects.remove"));
    rm.style.marginLeft = "auto";
    rm.addEventListener("click", async () => {
      if (!confirm(t("lib.projects.removeConfirm", { p }))) return;
      try { const r = await post("/api/library/project", { remove: p }); await load(); showResults(r); } catch (e) { showError(e); }
    });
    li.append(rm);
    list.append(li);
  }
}

async function renderBackups() {
  const list = $("lib-backup-list");
  list.textContent = "";
  let bs = [];
  try { bs = await (await fetch("/api/library/backups", { cache: "no-store" })).json(); } catch (e) { showError(e); }
  if (!bs.length) list.append(el("li", "muted", t("lib.backups.none")));
  for (const b of bs) {
    const li = el("li");
    const when = new Date(b.time).toLocaleString(LANG);
    li.append(el("span", "", when), el("span", "", b.what), el("span", "muted small", t("lib.entries", { n: b.entries.length })));
    const rs = el("button", "btn", t("lib.restore"));
    rs.style.marginLeft = "auto";
    rs.addEventListener("click", async () => {
      if (!confirm(t("lib.restoreConfirm", { n: b.entries.length, t: when }))) return;
      try { await post("/api/library/restore", { id: b.id }); await load(); } catch (e) { showError(e); }
    });
    li.append(rs);
    list.append(li);
  }
}

// --- wiring ------------------------------------------------------------------

for (const b of document.querySelectorAll(".lib-tabs button")) {
  b.addEventListener("click", () => { view = b.dataset.view; selected = null; render(); });
}
$("lib-search").addEventListener("input", renderList);
$("lib-refresh").addEventListener("click", load);
$("lib-import-all").addEventListener("click", async () => {
  try { await post("/api/library/import", { items: [] }); await load(); } catch (e) { showError(e); }
});
$("lib-apply").addEventListener("click", async () => {
  $("lib-apply").disabled = true;
  try {
    const r = await post("/api/library/apply", { overwrite: [...overwrite] });
    overwrite.clear();
    await load();
    showResults(r);
  } catch (e) { showError(e); await load(); }
});
$("lib-new").addEventListener("click", async () => {
  if (view === "skill") {
    const { path } = await post("/api/library/pick", { title: t("lib.pickSkill") });
    if (!path) return;
    try {
      const name = path.split(/[\\/]/).filter(Boolean).pop();
      await post("/api/library/skill", { name, path, agents: [] });
      selected = { kind: "skill", name };
      await load();
    } catch (e) { showError(e); }
    return;
  }
  selected = { new: "mcp" };
  render();
});
$("lib-project-add").addEventListener("click", async () => {
  const { path } = await post("/api/library/pick", { title: t("lib.pickProject") });
  if (!path) return;
  try { await post("/api/library/project", { add: path }); await load(); } catch (e) { showError(e); }
});
// the window is hidden, not closed: read again whenever it comes back
document.addEventListener("visibilitychange", () => { if (!document.hidden) load(); });

(async function start() {
  setLang("auto");
  try {
    const sv = await (await fetch("/api/settings", { cache: "no-store" })).json();
    setLang(sv.settings.language);
    const theme = sv.settings.theme;
    if (theme === "light" || theme === "dark") document.documentElement.dataset.theme = theme;
  } catch {}
  applyI18n();
  await load();
})();
