"use strict";

// The panel. Polls /api/snapshot every second, /api/period while the Stats
// tab is open, and reads and writes /api/settings. No framework.

const $ = (id) => document.getElementById(id);
const SPARK = 90; // seconds of live history
const spark = []; // [{source: tok/s}], one a second
let snap = null;
let sourceInfo = {}; // id → {label, color}
let settingsView = null;
let periodView = null;
const prefs = load("prefs") || { tab: "overview", period: "week", metric: "cost", dim: "source" };

function load(k) {
  try { return JSON.parse(localStorage.getItem("cs." + k)); } catch { return null; }
}
function save(k, v) {
  try { localStorage.setItem("cs." + k, JSON.stringify(v)); } catch { /* private mode */ }
}

// --- formatting -------------------------------------------------------------

function fmt(v) { return v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2); }
let NUMBERS = "metric"; // or "cjk": 万 and 亿
function tokens(n) {
  const a = Math.abs(n);
  if (NUMBERS === "cjk") {
    if (a >= 1e8) return fmt(n / 1e8) + "亿";
    if (a >= 1e4) return fmt(n / 1e4) + "万";
    return String(Math.round(n));
  }
  if (a >= 1e9) return fmt(n / 1e9) + "B";
  if (a >= 1e6) return fmt(n / 1e6) + "M";
  if (a >= 1e3) return fmt(n / 1e3) + "K";
  return String(n);
}
// money is kept in USD; it is shown in the user's currency at their rate
let CUR = { code: "USD", rate: 1, sym: "$" };
function setCurrency(sv) {
  const c = (sv && sv.settings.currency) || {};
  CUR = { code: c.code || "USD", rate: c.rate || 1, sym: ((sv && sv.currencies) || {})[c.code] || "$" };
}
function usd(micro) {
  const d = (micro / 1e6) * CUR.rate;
  const whole = CUR.code === "JPY" || CUR.code === "KRW";
  if (d === 0) return CUR.sym + "0";
  if (!whole && d < 0.01) return "<" + CUR.sym + "0.01";
  if (d >= 1000 || whole) return CUR.sym + Math.round(d).toLocaleString();
  return CUR.sym + d.toFixed(2);
}
function usdShort(micro) {
  const d = (micro / 1e6) * CUR.rate;
  return d >= 10 ? CUR.sym + Math.round(d).toLocaleString() : usd(micro);
}
function speed(v, unit = true) {
  if (!v || v <= 0) return "—";
  const s = v >= 100 ? v.toFixed(0) : v.toFixed(1);
  return unit ? s + " tok/s" : s;
}
function until(nowMs, atMs) {
  if (!atMs) return "?";
  const s = Math.round((atMs - nowMs) / 1000);
  if (s <= 0) return LANG === "zh-CN" ? "现在" : "now";
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
  const zh = LANG === "zh-CN";
  if (d) return zh ? `${d} 天 ${h} 小时` : `${d}d ${h}h`;
  if (h) return zh ? `${h} 小时 ${m} 分` : `${h}h ${m}m`;
  if (m) return zh ? `${m} 分钟` : `${m}m`;
  return zh ? `${s} 秒` : `${s}s`;
}
function windowName(w) {
  const k = "window." + w.kind;
  if (t(k) !== k) return t(k);
  const m = w.windowMinutes;
  return m % 1440 === 0 ? `${m / 1440}d` : m % 60 === 0 ? `${m / 60}h` : `${m}m`;
}
function hitRate(cached, input) {
  return input > 0 ? ((cached / input) * 100).toFixed(0) + "%" : "—";
}
function money(usdValue) {
  return usd(usdValue * 1e6);
}
function dayLabel(date, opts) {
  return new Date(date + "T00:00").toLocaleDateString(LANG, opts);
}

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}
function color(src) { return (sourceInfo[src] && sourceInfo[src].color) || "#85858d"; }
function label(src) { return (sourceInfo[src] && sourceInfo[src].label) || src; }
function dot(src) { return colorDot(color(src)); }
function colorDot(c) {
  const d = el("span", "dot");
  d.style.setProperty("--c", c);
  return d;
}

// --- order and hidden agents ------------------------------------------------
// Settings can put agents first (order) and leave them out of the
// overview's lists (hidden); they are still counted in every total.

function curSettings() { return settingsView ? (desired || settingsView.settings) : {}; }
function shown(src) { return !(curSettings().hidden || []).includes(src); }
// byOrder puts the ordered agents first, in their order; the rest keep theirs
function byOrder(list, srcOf) {
  const o = curSettings().order || [];
  if (!o.length) return list;
  const rank = (x) => { const i = o.indexOf(srcOf(x)); return i < 0 ? o.length : i; };
  return list.map((x, i) => [x, i]).sort((a, b) => rank(a[0]) - rank(b[0]) || a[1] - b[1]).map((p) => p[0]);
}

// --- tooltip ----------------------------------------------------------------
// One floating tip for the whole panel. An element gets one with tipText
// (plain text, \n for lines) or tipRich (a function that builds a node).

const tipEl = () => $("tip");
const richTips = new WeakMap();

function tipText(e, text) {
  e.dataset.tip = text;
}
function tipRich(e, build) {
  richTips.set(e, build);
  e.dataset.tip = "";
}

function placeTip(x, y) {
  const tip = tipEl();
  const pad = 8, r = tip.getBoundingClientRect();
  let left = x + 12, top = y + 14;
  if (left + r.width > window.innerWidth - pad) left = Math.max(pad, x - r.width - 12);
  if (top + r.height > window.innerHeight - pad) top = Math.max(pad, y - r.height - 12);
  tip.style.left = left + "px";
  tip.style.top = top + "px";
}

function showTip(content, x, y) {
  const tip = tipEl();
  tip.replaceChildren(typeof content === "string" ? document.createTextNode(content) : content);
  tip.hidden = false;
  placeTip(x, y);
}

function hideTip() {
  tipEl().hidden = true;
}

let tipFor = null;
let mouseX = -1, mouseY = -1;

// retip keeps the tip on what is under the mouse after a redraw replaced
// the element it was for
function retip() {
  if (!tipFor || tipFor.isConnected || tipEl().hidden) return;
  const under = document.elementFromPoint(mouseX, mouseY);
  const next = under && under.closest && under.closest("[data-tip]");
  if (!next) {
    tipFor = null;
    return hideTip();
  }
  tipFor = next;
  const build = richTips.get(next);
  showTip(build ? build() : next.dataset.tip, mouseX, mouseY);
}

document.addEventListener("mouseover", (e) => {
  const target = e.target.closest && e.target.closest("[data-tip]");
  if (!target) return;
  tipFor = target;
  const build = richTips.get(target);
  showTip(build ? build() : target.dataset.tip, e.clientX, e.clientY);
});
document.addEventListener("mousemove", (e) => {
  mouseX = e.clientX;
  mouseY = e.clientY;
  if (tipFor && !tipEl().hidden) placeTip(e.clientX, e.clientY);
});
document.addEventListener("mouseout", (e) => {
  if (!tipFor) return;
  const to = e.relatedTarget && e.relatedTarget.closest && e.relatedTarget.closest("[data-tip]");
  if (to === tipFor) return;
  tipFor = null;
  hideTip();
});
document.addEventListener("scroll", hideTip, true);

// tipRows builds a tip: a title, one row per agent, and a total line
function tipRows(title, rows, total) {
  const box = document.createElement("div");
  const head = el("b", "", title);
  box.append(head);
  for (const r of rows) {
    const row = el("div", "tip-row");
    row.append(r.color ? colorDot(r.color) : dot(r.src), el("span", "", r.name || label(r.src)), el("span", "num", r.a), el("span", "num muted", r.b || ""));
    box.append(row);
  }
  if (total) box.append(el("div", "tip-total", total));
  return box;
}

// --- language and theme -----------------------------------------------------

function applyI18n() {
  for (const e of document.querySelectorAll("[data-i18n]")) e.textContent = t(e.dataset.i18n);
  // icon buttons: their name for screen readers and the tooltip
  for (const e of document.querySelectorAll("[data-i18n-label]")) {
    e.setAttribute("aria-label", t(e.dataset.i18nLabel));
    tipText(e, t(e.dataset.i18nLabel));
  }
}
function applyTheme(theme) {
  if (theme === "light" || theme === "dark") document.documentElement.dataset.theme = theme;
  else delete document.documentElement.dataset.theme;
}

// --- tabs and segmented controls ---------------------------------------------

const tabs = [...document.querySelectorAll('[role="tab"]')];
function showTab(name, focus) {
  for (const tab of tabs) {
    const on = tab.id === "tab-" + name;
    tab.setAttribute("aria-selected", on);
    tab.tabIndex = on ? 0 : -1;
    $(tab.getAttribute("aria-controls")).hidden = !on;
    if (on && focus) tab.focus();
  }
  prefs.tab = name;
  save("prefs", prefs);
  if (name === "stats") refreshPeriod();
  if (name === "settings") refreshSettings();
  if (name === "sessions") refreshSessions();
}
for (const tab of tabs) {
  tab.addEventListener("click", () => showTab(tab.id.slice(4)));
  tab.addEventListener("keydown", (e) => {
    const i = tabs.indexOf(tab);
    const next = e.key === "ArrowRight" ? i + 1 : e.key === "ArrowLeft" ? i - 1 : e.key === "Home" ? 0 : e.key === "End" ? tabs.length - 1 : null;
    if (next === null) return;
    e.preventDefault();
    showTab(tabs[(next + tabs.length) % tabs.length].id.slice(4), true);
  });
}

function seg(id, value, onPick) {
  const box = $(id);
  const set = (v) => {
    for (const b of box.querySelectorAll("button")) b.setAttribute("aria-checked", b.dataset.v === v);
  };
  set(value);
  box.addEventListener("click", (e) => {
    const b = e.target.closest("button");
    if (!b) return;
    set(b.dataset.v);
    onPick(b.dataset.v);
  });
}

// --- overview -----------------------------------------------------------------

function drawSpark() {
  const svg = $("spark");
  const n = SPARK - 1;
  const srcs = [...new Set(spark.flatMap((p) => Object.keys(p)))];
  const totals = spark.map((p) => Object.values(p).reduce((a, b) => a + b, 0));
  const max = Math.max(1, ...totals);
  const x = (i) => (((i + SPARK - spark.length) / n) * 120).toFixed(1);
  const y = (v) => (29 - (v / max) * 27).toFixed(1);
  svg.replaceChildren();
  const base = spark.map(() => 0);
  for (const src of srcs) {
    const top = spark.map((p, i) => base[i] + (p[src] || 0));
    let d = "M" + top.map((v, i) => `${x(i)} ${y(v)}`).join(" L");
    d += " L" + base.map((v, i) => `${x(i)} ${y(v)}`).reverse().join(" L") + " Z";
    const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
    path.setAttribute("d", d);
    path.setAttribute("fill", color(src));
    path.setAttribute("opacity", ".75");
    svg.append(path);
    top.forEach((v, i) => (base[i] = v));
  }
}

// hovering the live curve reads it: seconds ago, each agent's speed
$("spark").addEventListener("mousemove", (e) => {
  const svg = $("spark");
  const r = svg.getBoundingClientRect();
  const i = Math.round(((e.clientX - r.left) / r.width) * (SPARK - 1)) - (SPARK - spark.length);
  svg.querySelector(".cursor")?.remove();
  if (i < 0 || i >= spark.length) return hideTip();
  const p = spark[i];
  const x = (((i + SPARK - spark.length) / (SPARK - 1)) * 120).toFixed(1);
  const line = document.createElementNS("http://www.w3.org/2000/svg", "line");
  line.setAttribute("class", "cursor");
  Object.entries({ x1: x, x2: x, y1: 0, y2: 30 }).forEach(([k, val]) => line.setAttribute(k, val));
  svg.append(line);
  const ago = spark.length - 1 - i;
  const rows = Object.entries(p).sort((a, b) => b[1] - a[1]).map(([src, v]) => ({ src, a: speed(v) }));
  const total = Object.values(p).reduce((a, b) => a + b, 0);
  showTip(tipRows(ago ? t("tip.ago", { s: ago }) : t("tip.now"), rows, rows.length > 1 ? `Σ ${speed(total)}` : rows.length ? "" : t("live.idle")), e.clientX, e.clientY);
});
$("spark").addEventListener("mouseleave", () => {
  $("spark").querySelector(".cursor")?.remove();
  hideTip();
});

function sessionItem(a, nowMs) {
  const li = el("li");
  if (a.id) li.addEventListener("click", () => openSession(a.id));
  const head = el("div", "s-head");
  head.append(dot(a.source));
  const name = el("span", "s-name", `${a.project || "?"} · ${a.model || label(a.source)}${a.subagent ? " · " + t("sessions.sub") : ""}`);
  tipText(name, `${label(a.source)}\n${a.project || "?"} · ${a.model || ""}${a.contextWindow ? `\n${t("context")} ${tokens(a.context)} / ${tokens(a.contextWindow)}` : ""}\n${t("click.open")}`);
  head.append(name, el("span", "phase " + a.phase, t("phase." + a.phase)),
    el("span", "s-rate", a.phase === "generating" ? speed(a.live || a.speed) : speed(a.speed)));
  li.append(head);
  if (a.context) {
    const ctx = el("div", "s-ctx");
    const pct = a.contextWindow ? Math.min(100, (a.context / a.contextWindow) * 100) : 0;
    const meter = el("div", "meter" + (pct >= 90 ? " bad" : pct >= 75 ? " warn" : ""));
    const fill = el("i");
    fill.style.width = (a.contextWindow ? pct : 0) + "%";
    fill.style.setProperty("--c", color(a.source));
    meter.append(fill);
    const used = a.contextWindow ? t("context.of", { used: tokens(a.context), limit: tokens(a.contextWindow) }) : tokens(a.context);
    ctx.append(el("span", "", t("context")), meter, el("span", "", used + (a.contextWindow ? ` · ${pct.toFixed(0)}%` : "")));
    li.append(ctx);
  }
  return li;
}

function renderOverview(s) {
  const point = {};
  for (const [src, v] of Object.entries(s.liveBySource || {})) point[src] = v;
  spark.push(point);
  if (spark.length > SPARK) spark.shift();
  drawSpark();

  $("live").textContent = speed(s.live, false);
  $("live-cost").hidden = !(s.liveCostPerHour > 0);
  $("live-cost").textContent = t("live.cost", { c: usd(s.liveCostPerHour) });
  $("est").hidden = !(s.liveEstimated > 0.5);
  const ls = $("live-sources");
  ls.replaceChildren();
  const live = Object.entries(s.liveBySource || {}).filter(([src]) => shown(src)).sort((a, b) => b[1] - a[1]);
  if (!live.length) ls.append(el("span", "muted", t("live.idle")));
  for (const [src, v] of live) {
    const sp = el("span");
    sp.append(dot(src), document.createTextNode(`${label(src)} ${speed(v)}`));
    ls.append(sp);
  }
  $("avg").textContent = speed(s.today.avgSpeed);
  $("peak").textContent = speed(s.today.peak);
  if (s.last) {
    $("last").textContent = speed(s.last.speed);
    let d = `${label(s.last.source)} · ${s.last.model || "?"} · ${tokens(s.last.output)} tokens`;
    if (s.last.ttftMs) d += " · " + t("firstToken", { s: (s.last.ttftMs / 1000).toFixed(1) });
    $("last-detail").textContent = d;
  }

  const td = s.today;
  $("t-tokens").textContent = tokens(td.input + td.output);
  $("t-cost").textContent = usd(td.costMicro);
  $("t-req").textContent = String(td.requests);
  $("t-cache").textContent = td.input > 0 ? t("cacheHit.today", { p: hitRate(td.cachedInput, td.input) }) : "";
  $("unpriced").hidden = !td.unpriced;
  $("unpriced").textContent = t("unpriced", { n: td.unpriced });
  $("pending").hidden = !s.pending;
  $("pending").textContent = t("pending", { n: s.pending || 0 });
  renderTodayRows(td);

  const q = $("quotas");
  q.replaceChildren();
  const quotas = byOrder((s.quotas || []).filter((x) => shown(x.source)), (x) => x.source);
  for (const quota of quotas) {
    const box = el("div", "quota");
    const head = el("div", "row between");
    const who = el("span", "row");
    who.append(dot(quota.source), el("strong", "", label(quota.source)));
    head.append(who, el("span", "muted small", (quota.plan || "").replace(/_/g, " ")));
    box.append(head);
    for (const w of quota.windows || []) {
      const pct = Math.max(0, Math.min(100, w.usedPercent));
      const meter = el("div", "meter" + (pct >= 90 ? " bad" : pct >= 70 ? " warn" : ""));
      const fill = el("i");
      fill.style.width = pct + "%";
      fill.style.setProperty("--c", color(quota.source));
      meter.append(fill);
      if (w.pace) {
        const tick = el("span", "exp");
        tick.style.left = `calc(${w.pace.expectedPercent.toFixed(1)}% - 1px)`;
        meter.append(tick);
      }
      const row = el("div", "window");
      row.append(el("span", "", t("quota.used", { w: windowName(w), p: pct.toFixed(0) })),
        el("span", "muted", t("quota.resets", { t: until(s.nowMs, w.resetsAtMs) })));
      box.append(meter, row);
      if (w.pace) box.append(paceLine(w.pace));
    }
    if (quota.reached) box.append(el("p", "small", t("quota.reached")));
    q.append(box);
  }
  $("quota-card").hidden = !quotas.length;

  renderSubs(s);
  renderBudget(s);

  const ul = $("overview-sessions");
  ul.classList.add("clickable");
  ul.replaceChildren();
  const agents = (s.agents || []).filter((a) => shown(a.source));
  for (const a of agents.slice(0, 3)) ul.append(sessionItem(a, s.nowMs));
  if (!agents.length) ul.append(el("li", "muted small", t("sessions.none")));
}

// paceLine says how a window is used against its time: on, ahead of or
// under pace, and whether it lasts to the reset
function paceLine(p) {
  const d = Math.round(Math.abs(p.deltaPercent));
  let text = p.deltaPercent > 2 ? t("pace.ahead", { p: d }) : p.deltaPercent < -2 ? t("pace.behind", { p: d }) : t("pace.onTrack");
  let cls = p.deltaPercent > 6 ? " ahead" : p.deltaPercent < -2 ? " behind" : "";
  if (p.lastsToReset) text += " · " + t("pace.lasts");
  else {
    text += " · " + t("pace.runsOut", { t: until(0, p.runsOutInMs) });
    cls = " out";
  }
  return el("div", "pace" + cls, text);
}

// renderBudget shows today's and this month's spend against the budgets
function renderBudget(s) {
  const b = (settingsView && settingsView.settings.budget) || {};
  const box = $("budgets");
  box.replaceChildren();
  for (const [key, spent, budget] of [["budget.today", s.today.costMicro, b.daily], ["budget.month", s.monthCostMicro, b.monthly]]) {
    if (!(budget > 0)) continue;
    const pct = (spent / (budget * 1e6)) * 100;
    const row = el("div", "budget");
    const head = el("div", "window");
    head.append(el("span", "", t(key)), el("span", "muted", t("budget.of", { a: usd(spent), b: money(budget) }) + ` · ${pct.toFixed(0)}%`));
    const meter = el("div", "meter" + (pct >= 100 ? " bad" : pct >= 80 ? " warn" : ""));
    const fill = el("i");
    fill.style.width = Math.min(100, pct) + "%";
    meter.append(fill);
    row.append(head, meter);
    box.append(row);
  }
  $("budget-card").hidden = !box.childElementCount;
}

async function refreshAlerts() {
  try {
    const list = await (await fetch("/api/alerts", { cache: "no-store" })).json();
    const ul = $("alert-list");
    ul.replaceChildren();
    for (const a of (list || []).slice(0, 5)) {
      const li = el("li");
      li.append(el("span", "", a.title), el("small", "", `${clock(a.atMs, true)}${a.body ? " · " + a.body : ""}`));
      ul.append(li);
    }
    $("alerts-card").hidden = !ul.childElementCount;
  } catch { /* the next tick tries again */ }
}

// --- service status -----------------------------------------------------------

let statusView = null;
async function refreshStatus(check) {
  try {
    const r = await fetch("/api/status", { method: check ? "POST" : "GET", cache: "no-store" });
    statusView = await r.json();
    renderStatus();
  } catch { /* the next tick tries again */ }
}

function renderStatus() {
  const v = statusView;
  if (!v) return;
  const ul = $("status-list");
  ul.replaceChildren();
  let checked = 0;
  for (const p of v.providers || []) {
    const li = el("li");
    const failing = v.check && p.feed && p.fails > 0;
    const known = p.checkedMs > 0;
    const ind = !v.check || !p.feed || !known ? "none-known" : p.indicator;
    const d = el("span", "sdot " + ind + (failing ? " stale" : ""));
    const name = el("span", "sname", p.name);
    let desc = "";
    if (!p.feed) desc = t("status.linkOnly");
    else if (!v.check) desc = "";
    else if (failing) desc = t("status.unreachable", { n: p.fails, t: clock(p.nextMs) });
    else if (!known) desc = t("status.checking");
    else desc = p.description || t("status." + p.indicator);
    const open = el("button", "mini", "↗");
    open.type = "button";
    open.setAttribute("aria-label", t("status.open", { p: p.name }));
    tipText(open, t("status.open", { p: p.name }));
    open.addEventListener("click", () => post("/api/open", { target: "status:" + p.id }).catch(() => {}));
    const small = el("small", failing ? "warn" : "muted", desc);
    li.append(d, name, small, open);
    const tip = [`${p.name}: ${desc}`];
    if (failing && known) tip.push(t("status.lastKnown", { s: p.description || t("status." + p.indicator), t: clock(p.checkedMs) }));
    if (failing && p.error) tip.push(p.error);
    tipText(li, tip.join("\n"));
    // today's incidents: the open ones first
    if (v.check && p.feed && known) {
      const today = (p.today || []).slice().sort((a, b) => (a.resolved - b.resolved) || (b.startMs - a.startMs));
      const box = el("div", "incidents");
      if (!today.length) box.append(el("span", "muted", t("status.todayNone")));
      for (const inc of today.slice(0, 3)) {
        const row = el(inc.shortlink ? "button" : "span", "incident" + (inc.resolved ? " done" : " open"));
        if (inc.shortlink) {
          row.type = "button";
          row.addEventListener("click", () => post("/api/open", { target: "incident:" + inc.shortlink }).catch(() => {}));
        }
        row.append(el("span", "idot " + (inc.resolved ? "done" : inc.impact || "minor")),
          el("span", "itime", clock(inc.startMs, new Date(inc.startMs).toDateString() !== new Date().toDateString())), el("span", "iname", inc.name),
          el("span", "istate", inc.resolved ? t("status.resolved") : t("status.ongoing")));
        tipText(row, `${inc.name}\n${clock(inc.startMs, true)} · ${inc.resolved ? t("status.resolved") : t("status.ongoing")}${inc.shortlink ? "\n" + t("click.open") : ""}`);
        box.append(row);
      }
      if (today.length > 3) box.append(el("span", "muted", t("status.more", { n: today.length - 3 })));
      li.append(box);
    }
    checked = Math.max(checked, p.checkedMs || 0);
    ul.append(li);
  }
  $("status-card").hidden = !(v.providers || []).length;
  $("status-sub").textContent = !v.check ? t("status.off") : checked ? t("status.checked", { t: clock(checked) }) : t("status.checking");
  $("status-refresh").hidden = !v.check;
  renderStatusPick();
}

// the Settings chips: which providers the overview lists
function renderStatusPick() {
  const v = statusView, sv = settingsView;
  if (!v || !sv) return;
  const st = desired || sv.settings;
  $("set-status-check").checked = !!st.statusCheck;
  $("set-status-auto").checked = !st.statusCustom;
  const box = $("status-pick");
  box.replaceChildren();
  const shown = new Set((v.providers || []).map((p) => p.id));
  const picked = st.statusCustom ? new Set(st.statusProviders || []) : shown;
  for (const p of v.all || []) {
    const b = el("button", "chip", p.name);
    b.type = "button";
    b.setAttribute("aria-pressed", String(picked.has(p.id)));
    b.disabled = !st.statusCustom;
    b.addEventListener("click", () => {
      const next = new Set(picked);
      if (next.has(p.id)) next.delete(p.id);
      else next.add(p.id);
      changeSetting({ statusCustom: true, statusProviders: (v.all || []).map((x) => x.id).filter((id) => next.has(id)) });
      saving.then(() => refreshStatus());
    });
    box.append(b);
  }
}

// renderSubs sets each plan's monthly fee against the last 30 days' API
// price of the agent's use
function renderSubs(s) {
  const box = $("subs");
  box.replaceChildren();
  const fees = (settingsView && settingsView.settings.subscriptions) || {};
  const entries = Object.entries(fees).filter(([, fee]) => fee > 0);
  if (!entries.length) {
    box.append(el("p", "muted small", t("subs.none")));
    return;
  }
  box.append(el("p", "muted small", t("subs.period")));
  let api = 0, paid = 0;
  for (const [src, fee] of entries) {
    const used = ((s.last30BySource || {})[src] || 0) / 1e6;
    api += used;
    paid += fee;
    box.append(subsRow(src, label(src), used, fee));
  }
  if (entries.length > 1) box.append(subsRow("", t("subs.total"), api, paid));
}

function subsRow(src, name, used, fee) {
  const row = el("div", "subs-row");
  const who = el("span", "row");
  if (src) who.append(dot(src));
  who.append(el("span", "", name));
  const x = fee > 0 ? used / fee : 0;
  row.append(who, el("span", "ratio" + (x < 1 ? " low" : ""), x >= 10 ? x.toFixed(0) + "×" : x.toFixed(1) + "×"),
    el("small", "", t("subs.row", { api: money(used), fee: money(fee) })));
  return row;
}

// renderTodayRows splits today's totals by agent, or by model (the top
// ten), as the card's switch says
function renderTodayRows(td) {
  const rows = prefs.todayBy === "model"
    ? (td.byModel || []).filter((r) => shown(r.source)).slice(0, 10)
    : byOrder((td.bySource || []).filter((r) => shown(r.source)), (r) => r.source);
  shareRows($("today-sources"), rows);
}

function shareRows(box, rows) {
  box.replaceChildren();
  const total = rows.reduce((a, r) => a + r.costMicro, 0) || rows.reduce((a, r) => a + r.input + r.output, 0) || 1;
  const byCost = rows.some((r) => r.costMicro > 0);
  for (const r of rows) {
    const row = el("div", "share-row");
    tipText(row, `${r.label}${r.label !== label(r.source) ? " · " + label(r.source) : ""}\n${r.requests.toLocaleString()} ${t("requests")} · ${t("mix.input")} ${tokens(r.input)} · ${t("mix.output")} ${tokens(r.output)}\n${t("cacheHit")} ${hitRate(r.cachedInput, r.input)} · ${t("stats.avg")} ${speed(r.avgSpeed)} · ${usd(r.costMicro)}`);
    const name = el("div", "name");
    name.append(dot(r.source), el("span", "", r.label));
    row.append(name, el("span", "muted", `${tokens(r.input + r.output)} · ${usd(r.costMicro)}`));
    const bar = el("div", "bar");
    const fill = el("i");
    fill.style.width = (((byCost ? r.costMicro : r.input + r.output) / total) * 100).toFixed(1) + "%";
    fill.style.setProperty("--c", color(r.source));
    bar.append(fill);
    row.append(bar);
    box.append(row);
  }
}

// --- sessions -----------------------------------------------------------------

function renderSessions(s) {
  const box = $("session-groups");
  box.replaceChildren();
  const groups = {};
  for (const a of s.agents || []) (groups[a.source] ||= []).push(a);
  for (const [src, list] of byOrder(Object.entries(groups), (g) => g[0])) {
    const card = el("div", "card");
    const head = el("div", "group-head");
    head.append(dot(src), el("span", "", label(src)), el("span", "count", String(list.length)));
    const ul = el("ul", "sessions clickable");
    for (const a of list) ul.append(sessionItem(a, s.nowMs));
    card.append(head, ul);
    box.append(card);
  }
  $("sessions-none").hidden = Object.keys(groups).length > 0;
}

function clock(ms, withDate) {
  const d = new Date(ms);
  const o = { hour: "2-digit", minute: "2-digit", hour12: false };
  if (withDate) Object.assign(o, { month: "short", day: "numeric" });
  return d.toLocaleString(LANG, o);
}

function duration(ms) {
  const m = Math.round(ms / 60000);
  if (m < 1) return LANG === "zh-CN" ? "不到 1 分钟" : "<1 min";
  if (m < 60) return LANG === "zh-CN" ? `${m} 分钟` : `${m} min`;
  const h = Math.floor(m / 60), mm = m % 60;
  return LANG === "zh-CN" ? `${h} 小时 ${mm} 分` : `${h}h ${mm}m`;
}

let sessionsRange = "today";
let openSessionId = "";

async function refreshSessions() {
  if (openSessionId) return;
  try {
    const r = await fetch("/api/sessions?since=" + (sessionsRange === "week" ? "week" : "today"), { cache: "no-store" });
    renderSessionList(await r.json());
    retip();
  } catch { /* the next tick tries again */ }
}

function renderSessionList(list) {
  const ul = $("session-list");
  ul.replaceChildren();
  const multiDay = sessionsRange === "week";
  for (const s of list) {
    const li = el("li");
    const head = el("div", "s-head");
    head.append(dot(s.source), el("span", "s-name", `${s.project || "?"} · ${s.model || s.label}${s.subagent ? " · " + t("sessions.sub") : ""}`),
      el("span", "phase " + s.phase, t("phase." + s.phase)), el("span", "s-rate", usd(s.costMicro)));
    const sub = el("div", "s-sub");
    sub.append(el("span", "", `${s.requests} ${t("requests")} · ${tokens(s.input + s.output)} · ${speed(s.avgSpeed)}`),
      el("span", "", `${clock(s.firstMs, multiDay)} – ${clock(s.lastMs, false)}`));
    li.append(head, sub);
    tipText(li, `${s.label} · ${s.project || "?"}\n${s.model || ""}\n${t("click.open")}`);
    li.addEventListener("click", () => openSession(s.agent));
    ul.append(li);
  }
  if (!list.length) ul.append(el("li", "muted small", t("sessions.noneToday")));
}

let sdRows = [], sdAll = false, lastDetail = null;
async function openSession(id) {
  if (!id) return;
  if (prefs.tab !== "sessions") showTab("sessions");
  openSessionId = id;
  $("session-list-view").hidden = true;
  $("session-detail").hidden = false;
  $("sd-title").textContent = "…";
  try {
    const r = await fetch("/api/session?id=" + encodeURIComponent(id), { cache: "no-store" });
    if (!r.ok) throw new Error(await r.text());
    renderSessionDetail(await r.json());
  } catch (e) {
    $("sd-title").textContent = String(e).replace(/^Error: /, "");
  }
}

function closeSession() {
  openSessionId = "";
  $("session-detail").hidden = true;
  $("session-list-view").hidden = false;
  refreshSessions();
}

function renderSessionDetail(d) {
  lastDetail = d;
  const s = d.session;
  $("sd-dot").style.setProperty("--c", color(s.source));
  $("sd-title").textContent = `${s.label} · ${s.project || "?"}`;
  $("sd-meta").textContent = `${s.model || ""}${s.subagent ? " · " + t("sessions.sub") : ""} · ` +
    t("session.span", { from: clock(s.firstMs, true), to: clock(s.lastMs, true), d: duration(s.lastMs - s.firstMs) }) +
    (s.contextWindow ? ` · ${t("context")} ${tokens(s.context)} / ${tokens(s.contextWindow)}` : "");
  $("sd-tokens").textContent = tokens(s.input + s.output);
  $("sd-cost").textContent = usd(s.costMicro) + (s.unpriced ? "*" : "");
  $("sd-req").textContent = String(s.requests);
  $("sd-speed").textContent = speed(s.avgSpeed);
  $("sd-from").textContent = t(d.from === "log" ? "session.from.log" : "session.from.memory") +
    (s.unpriced ? " " + t("unpriced", { n: s.unpriced }) : "");
  sdRows = (d.requests || []).slice().reverse();
  sdAll = false;
  drawSessionChart(d.requests || []);
  renderRequestRows();
}

// drawSessionChart: one bar per request, height = output tokens, shade =
// speed; the request's numbers on hover
function drawSessionChart(rq) {
  const svg = $("sd-chart");
  svg.replaceChildren();
  if (!rq.length) return;
  const n = rq.length, W = 360, H = 60;
  const max = Math.max(1, ...rq.map((q) => q.output));
  const fast = Math.max(1, ...rq.map((q) => q.speed || 0));
  const bw = W / n;
  rq.forEach((q, i) => {
    const h = Math.max(1, (q.output / max) * (H - 4));
    const g = document.createElementNS("http://www.w3.org/2000/svg", "g");
    const hit = document.createElementNS("http://www.w3.org/2000/svg", "rect");
    Object.entries({ x: i * bw, y: 0, width: bw, height: H, fill: "transparent" }).forEach(([k, v]) => hit.setAttribute(k, v));
    const bar = document.createElementNS("http://www.w3.org/2000/svg", "rect");
    Object.entries({ x: i * bw + bw * 0.1, y: H - h, width: Math.max(bw * 0.8, 0.6), height: h, rx: 1 }).forEach(([k, v]) => bar.setAttribute(k, v));
    bar.setAttribute("fill", "var(--accent)");
    bar.setAttribute("opacity", q.speed ? (0.35 + 0.65 * (q.speed / fast)).toFixed(2) : "0.25");
    g.append(hit, bar);
    tipText(g, requestTip(q));
    svg.append(g);
  });
}

function requestTip(q) {
  const lines = [`${clock(q.endMs, true)} · ${q.model || "?"}${q.effort ? " · " + effortName(q.effort) : ""}${q.fast ? " · fast" : ""}`,
    `${t("mix.input")} ${tokens(q.input)} (${t("mix.cacheRead")} ${tokens(q.cachedInput)}, ${t("mix.cacheWrite")} ${tokens(q.cacheWrite)})`,
    `${t("mix.output")} ${tokens(q.output)}${q.reasoning ? ` (reasoning ${tokens(q.reasoning)})` : ""}`,
    `${speed(q.speed)}${q.ttftMs ? ` · ${t("col.ttft")} ${(q.ttftMs / 1000).toFixed(1)}s` : ""} · ${q.priced ? usd(q.costMicro) : t("unpricedShort")}`];
  return lines.join("\n");
}

function renderRequestRows() {
  const table = $("sd-rows");
  table.replaceChildren();
  const head = el("tr");
  for (const k of ["col.time", "col.in", "col.out", "col.speed", "col.ttft", "col.cost"]) head.append(el("th", "", t(k)));
  table.append(head);
  const shown = sdAll ? sdRows : sdRows.slice(0, 50);
  for (const q of shown) {
    const tr = el("tr");
    tr.append(el("td", "", clock(q.endMs, sessionsRange === "week")), el("td", "muted", tokens(q.input)), el("td", "", tokens(q.output)),
      el("td", "muted", speed(q.speed, false)), el("td", "muted", q.ttftMs ? (q.ttftMs / 1000).toFixed(1) + "s" : "—"),
      el("td", "", q.priced ? usd(q.costMicro) : "—"));
    tipText(tr, requestTip(q));
    table.append(tr);
  }
  const more = $("sd-more");
  more.hidden = sdRows.length <= 50;
  more.textContent = sdAll ? t("table.less") : t("table.more", { n: sdRows.length });
}

// --- stats --------------------------------------------------------------------

let focus = "";
let lastPeriodText = "";
async function refreshPeriod(force) {
  try {
    const q = "/api/period?p=" + encodeURIComponent(prefs.period) + (focus ? "&focus=" + encodeURIComponent(focus) : "");
    const r = await fetch(q, { cache: "no-store" });
    const text = await r.text();
    if (!force && text === lastPeriodText) return; // unchanged: no redraw under the mouse
    lastPeriodText = text;
    periodView = JSON.parse(text);
    renderStats();
    retip();
  } catch { /* the next tick tries again */ }
}

function pointLabel(p, period) {
  if (period === "day") return p.label;
  if (period === "week") return dayLabel(p.label, { weekday: "short" });
  if (period === "month") return String(Number(p.label.slice(8)));
  return dayLabel(p.label + "-01", { month: "short" });
}

// pointTitle names a bar in full: "Wed, Nov 12", "14:00 – 15:00", "Nov 2026"
function pointTitle(p, period) {
  if (period === "day") return `${p.label}:00 – ${String((Number(p.label) + 1) % 24).padStart(2, "0")}:00`;
  if (period === "week" || period === "month") return dayLabel(p.label, { weekday: "short", month: "short", day: "numeric" });
  return dayLabel(p.label + "-01", { year: "numeric", month: "long" });
}

// niceMax rounds a chart's top up to 1, 2 or 5 × a power of ten
function niceMax(v) {
  if (v <= 0) return 1;
  const e = Math.pow(10, Math.floor(Math.log10(v)));
  const f = v / e;
  return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10) * e;
}

function drawChart(v) {
  const box = $("chart");
  box.replaceChildren();
  const metric = prefs.metric === "tokens" ? "tokens" : "costMicro";
  const fmtv = (x) => (metric === "tokens" ? tokens(Math.round(x)) : usdShort(x));
  const pts = v.series || [];
  const W = 360, H = 130, padB = 14, padL = 30, top = 6;
  const plotH = H - padB - top, plotW = W - padL;
  const max = niceMax(Math.max(0, ...pts.map((p) => p[metric])));
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", `0 0 ${W} ${H}`);
  svg.setAttribute("role", "img");
  const summary = pts.filter((p) => p[metric] > 0).map((p) => `${pointLabel(p, v.period)}: ${metric === "tokens" ? tokens(p.tokens) : usd(p.costMicro)}`);
  svg.setAttribute("aria-label", summary.join(", ") || t("stats.empty"));
  const ns = (tag, attrs) => {
    const e = document.createElementNS("http://www.w3.org/2000/svg", tag);
    for (const [k, val] of Object.entries(attrs)) e.setAttribute(k, val);
    return e;
  };
  const yOf = (val) => top + plotH - (val / max) * plotH;
  // the axis: the top, half and zero
  for (const g of [max, max / 2, 0]) {
    svg.append(ns("line", { x1: padL, x2: W, y1: yOf(g).toFixed(1), y2: yOf(g).toFixed(1), class: "grid" }));
    if (g > 0) {
      const lab = ns("text", { x: padL - 4, y: (yOf(g) + 3).toFixed(1), "text-anchor": "end", class: "ylab" });
      lab.textContent = fmtv(g);
      svg.append(lab);
    }
  }
  const bw = plotW / Math.max(pts.length, 1);
  const every = pts.length > 14 ? Math.ceil(pts.length / 7) : 1;
  const parts = chartParts(v);
  const partsOf = (p) => (parts.byModel ? p.models : p.sources) || {};
  if (focus) svg.classList.add("focused");
  pts.forEach((p, i) => {
    let y = top + plotH;
    const x0 = padL + i * bw;
    const g = ns("g", { class: "bar" + (focus === p.label ? " sel" : "") });
    // a full-height hit area, so short bars are as easy to point at
    g.append(ns("rect", { class: "hit", x: x0.toFixed(1), y: 0, width: bw.toFixed(1), height: H - padB }));
    for (const part of parts.list) {
      const sp = partsOf(p)[part.key];
      if (!sp) continue;
      const val = metric === "tokens" ? sp.tokens : sp.costMicro;
      if (val <= 0) continue;
      const h = (val / max) * plotH;
      y -= h;
      g.append(ns("rect", { x: (x0 + bw * 0.15).toFixed(1), y: y.toFixed(1), width: (bw * 0.7).toFixed(1), height: Math.max(h, 0.5).toFixed(1), fill: part.color, rx: 1.5 }));
    }
    tipRich(g, () => {
      const rows = parts.list.filter((part) => partsOf(p)[part.key])
        .map((part) => ({ part, tk: partsOf(p)[part.key].tokens, c: partsOf(p)[part.key].costMicro }))
        .sort((a, b) => (metric === "tokens" ? b.tk - a.tk : b.c - a.c))
        .map((r) => ({ color: r.part.color, name: r.part.name, a: tokens(r.tk), b: usd(r.c) }));
      const total = p.requests
        ? `${tokens(p.tokens)} tokens · ${usd(p.costMicro)} · ${p.requests.toLocaleString()} ${t("requests")}\n${t("click.drill")}`
        : t("stats.empty");
      return tipRows(pointTitle(p, v.period), rows, total);
    });
    g.addEventListener("mouseenter", () => { svg.classList.add("hovering"); g.classList.add("hot"); });
    g.addEventListener("mouseleave", () => { svg.classList.remove("hovering"); g.classList.remove("hot"); });
    g.addEventListener("click", () => {
      focus = focus === p.label ? "" : p.label;
      refreshPeriod(true);
    });
    svg.append(g);
    if (i % every === 0) {
      const tx = ns("text", { x: (x0 + bw / 2).toFixed(1), y: H - 2, "text-anchor": "middle", class: "axis" });
      tx.textContent = pointLabel(p, v.period);
      svg.append(tx);
    }
  });
  // the average over the bars
  if (pts.length > 1) {
    const avg = pts.reduce((a, p) => a + p[metric], 0) / pts.length;
    if (avg > 0) {
      svg.append(ns("line", { x1: padL, x2: W, y1: yOf(avg).toFixed(1), y2: yOf(avg).toFixed(1), class: "avg" }));
      const lab = ns("text", { x: W, y: (yOf(avg) - 2).toFixed(1), "text-anchor": "end", class: "avglab" });
      lab.textContent = `${t("chart.avg")} ${fmtv(avg)}`;
      svg.append(lab);
    }
  }
  box.append(svg);
  const lg = $("chart-legend");
  lg.replaceChildren();
  for (const part of parts.list) {
    const sp = el("span");
    sp.append(colorDot(part.color), document.createTextNode(part.name));
    lg.append(sp);
  }
  if (parts.note) lg.append(el("span", "note", parts.note));
}

// The chart's bars split by agent or, by day and month, by model: the
// period's top models in colours of their own, the rest as one grey part.
const MODEL_COLORS = ["#6366f1", "#0ea5e9", "#10b981", "#f59e0b", "#ef4444", "#a855f7", "#ec4899"];
const OTHER_COLOR = "#9a9aa3";
function chartParts(v) {
  const want = prefs.chartBy === "model";
  if (!want || v.period === "day" || !(v.models || []).length) {
    return {
      byModel: false,
      list: (v.sources || []).map((s) => ({ key: s.id, color: color(s.id), name: s.label })),
      note: want && v.period === "day" ? t("chart.hourByAgent") : "",
    };
  }
  const names = {};
  for (const m of v.models) names[m.model] = (names[m.model] || 0) + 1;
  return {
    byModel: true,
    list: v.models.map((m, i) => m.key === "other"
      ? { key: m.key, color: OTHER_COLOR, name: t("chart.otherModels") }
      // the same model under two agents: say whose
      : { key: m.key, color: MODEL_COLORS[i % MODEL_COLORS.length], name: names[m.model] > 1 ? `${m.model} · ${label(m.source)}` : m.model }),
  };
}

// delta writes how a total moved against the period before
function delta(id, cur, prev, higherIsBetter, prevText) {
  const e = $(id);
  e.className = "delta";
  if (prev == null) {
    e.textContent = "";
    return;
  }
  let text;
  if (prev === 0) text = cur > 0 ? "▲ new" : "";
  else {
    const p = ((cur - prev) / prev) * 100;
    text = Math.abs(p) < 0.5 ? "≈ 0%" : `${p > 0 ? "▲" : "▼"} ${Math.abs(p) >= 100 ? Math.round(Math.abs(p)) : Math.abs(p).toFixed(1)}%`;
    if (Math.abs(p) >= 0.5) e.classList.add((p > 0) === !higherIsBetter ? "up" : "down");
  }
  e.textContent = text;
  tipText(e.parentElement, prevText);
}

const sortState = load("sort") || { key: "cost", dir: -1 };
let dimFilter = "", dimAll = false;

// effortName names a reasoning effort in the panel's language
function effortName(e) {
  const k = "effort." + (e === "—" ? "none" : e);
  const s = t(k);
  return s === k ? e : s;
}

function rowValue(r, key) {
  switch (key) {
    case "name": return r.label.toLowerCase();
    case "tokens": return r.input + r.output;
    case "cache": return r.input > 0 ? r.cachedInput / r.input : -1;
    case "speed": return r.avgSpeed;
    default: return r.costMicro;
  }
}

function renderStats() {
  const v = periodView;
  if (!v) return;
  $("s-tokens").textContent = tokens(v.input + v.output);
  $("s-cost").textContent = usd(v.costMicro);
  $("s-req").textContent = v.requests.toLocaleString();
  $("s-avg").textContent = speed(v.avgSpeed);
  const pv = v.prev;
  const pvAvg = pv && pv.measuredMs ? pv.measuredOut / (pv.measuredMs / 1000) : null;
  const vs = pv ? t("prev." + v.period) : "";
  const pr = (val) => (pv ? `${vs}\n` + t("prev.range", { from: pv.from, to: pv.to, v: val }) : "");
  delta("d-tokens", v.input + v.output, pv ? pv.input + pv.output : null, true, pr(pv ? tokens(pv.input + pv.output) : ""));
  delta("d-cost", v.costMicro, pv ? pv.costMicro : null, false, pr(pv ? usd(pv.costMicro) : ""));
  delta("d-req", v.requests, pv ? pv.requests : null, true, pr(pv ? pv.requests.toLocaleString() : ""));
  delta("d-avg", v.avgSpeed, pvAvg, true, pr(speed(pvAvg)));
  const sub = [];
  if (v.period !== "day") {
    sub.push(`${dayLabel(v.from, { month: "short", day: "numeric" })} – ${dayLabel(v.to, { month: "short", day: "numeric" })}`);
    sub.push(`${t("stats.days")} ${v.activeDays}`);
    if (v.activeDays) sub.push(`${t("stats.perDay")} ${usd(v.costMicro / v.activeDays)}`);
  }
  if (pv) sub.push(vs);
  if (v.historyFrom) sub.push(t("stats.since", { d: v.historyFrom }));
  $("s-sub").textContent = sub.join(" · ");
  $("chart-title").textContent = t("period." + v.period);
  drawChart(v);

  $("s-cache").textContent = v.input > 0 ? `${t("cacheHit")} ${hitRate(v.cachedInput, v.input)}` : "";
  const mix = [
    ["mix.input", Math.max(0, v.input - v.cachedInput - v.cacheWrite), "#6366f1"],
    ["mix.cacheRead", v.cachedInput, "#a5b4fc"],
    ["mix.cacheWrite", v.cacheWrite, "#f59e0b"],
    ["mix.output", v.output, "#10b981"],
  ];
  const all = mix.reduce((a, m) => a + m[1], 0) || 1;
  const bar = $("mix");
  bar.replaceChildren();
  const lg = $("mix-legend");
  lg.replaceChildren();
  for (const [k, n, c] of mix) {
    const seg = el("i");
    seg.style.width = ((n / all) * 100).toFixed(2) + "%";
    seg.style.background = c;
    tipText(seg, `${t(k)} ${tokens(n)} (${((n / all) * 100).toFixed(1)}%)`);
    bar.append(seg);
    const sp = el("span");
    const d = el("span", "dot");
    d.style.setProperty("--c", c);
    sp.append(d, document.createTextNode(`${t(k)} ${tokens(n)} (${((n / all) * 100).toFixed(0)}%)`));
    lg.append(sp);
  }

  // the bar the rows are narrowed to
  const chip = $("focus-chip");
  chip.hidden = !v.focus;
  if (v.focus) {
    const pt = (v.series || []).find((p) => p.label === v.focus) || { label: v.focus };
    const f = v.focused || {};
    const left = el("div");
    left.append(el("span", "", t("focus.on", { x: pointTitle(pt, v.period) })),
      el("small", "", `${tokens((f.input || 0) + (f.output || 0))} tokens · ${usd(f.costMicro || 0)} · ${(f.requests || 0).toLocaleString()} ${t("requests")}` +
        (v.period === "day" ? " · " + t("focus.hourNote") : "")));
    const clear = el("button", "", t("focus.clear"));
    clear.type = "button";
    clear.addEventListener("click", () => { focus = ""; refreshPeriod(true); });
    chip.replaceChildren(left, clear);
  }

  const efforts = (v.byEffort || []).map((r) => ({ ...r, label: `${label(r.source)} · ${effortName(r.name)}` }));
  let rows = ({ source: v.bySource, model: v.byModel, project: v.byProject, effort: efforts }[prefs.dim] || []).slice();
  if (dimFilter) rows = rows.filter((r) => `${r.label} ${label(r.source)}`.toLowerCase().includes(dimFilter));
  rows.sort((a, b) => {
    const x = rowValue(a, sortState.key), y = rowValue(b, sortState.key);
    return (x < y ? -1 : x > y ? 1 : 0) * sortState.dir;
  });
  const table = $("dim-rows");
  table.replaceChildren();
  const head = el("tr");
  for (const [k, key] of [["col.name", "name"], ["col.tokens", "tokens"], ["col.cache", "cache"], ["col.speed", "speed"], ["col.cost", "cost"]]) {
    const th = el("th", "", t(k));
    if (sortState.key === key) th.setAttribute("aria-sort", sortState.dir < 0 ? "descending" : "ascending");
    th.addEventListener("click", () => {
      sortState.dir = sortState.key === key ? -sortState.dir : key === "name" ? 1 : -1;
      sortState.key = key;
      save("sort", sortState);
      renderStats();
    });
    head.append(th);
  }
  table.append(head);
  const total = rows.reduce((a, r) => a + r.costMicro, 0) || 1;
  for (const r of dimAll ? rows : rows.slice(0, 15)) {
    const tr = el("tr");
    const name = el("td");
    tipText(tr, `${label(r.source)} · ${r.label}\n${r.requests.toLocaleString()} ${t("requests")} · ${t("mix.input")} ${tokens(r.input)} · ${t("mix.output")} ${tokens(r.output)}\n${t("cacheHit")} ${hitRate(r.cachedInput, r.input)} · ${speed(r.avgSpeed)} · ${usd(r.costMicro)}${r.unpriced ? `\n${t("unpriced", { n: r.unpriced })}` : ""}`);
    name.append(dot(r.source), document.createTextNode(r.label));
    const pct = el("span", "pct");
    const fill = el("i");
    fill.style.width = ((r.costMicro / total) * 100).toFixed(1) + "%";
    fill.style.setProperty("--c", color(r.source));
    pct.append(fill);
    name.append(pct);
    tr.append(name, el("td", "", tokens(r.input + r.output)), el("td", "muted", hitRate(r.cachedInput, r.input)), el("td", "muted", speed(r.avgSpeed)),
      el("td", "", usd(r.costMicro) + (r.unpriced ? "*" : "")));
    table.append(tr);
  }
  const more = $("dim-more");
  more.hidden = rows.length <= 15;
  more.textContent = dimAll ? t("table.less") : t("table.more", { n: rows.length });
  $("dim-filter").placeholder = t("table.filter");
  $("stats-empty").hidden = rows.length > 0;
  renderSpeeds(v);
}

// --- speed ranking ----------------------------------------------------------
// Each model's output speed as a band from its slow tenth to its fast
// tenth with the median marked, on one scale, fastest first.

let speedAll = false;
function secs(ms) { return ms >= 10000 ? (ms / 1000).toFixed(0) + "s" : (ms / 1000).toFixed(1) + "s"; }

function renderSpeeds(v) {
  const rows = (v.speeds || []).filter((r) => shown(r.source));
  const box = $("speed-rows");
  box.replaceChildren();
  const timed = rows.filter((r) => r.p50 > 0);
  const top = Math.max(1, ...timed.map((r) => r.p90));
  const list = speedAll ? rows : rows.slice(0, 8);
  for (const r of list) {
    const row = el("div", "speed-row");
    const name = el("div", "name");
    name.append(dot(r.source), el("span", "", r.model));
    const track = el("div", "band");
    if (r.p50 > 0) {
      const band = el("i");
      band.style.left = ((r.p10 / top) * 100).toFixed(1) + "%";
      band.style.width = Math.max(1, ((r.p90 - r.p10) / top) * 100).toFixed(1) + "%";
      band.style.setProperty("--c", color(r.source));
      const med = el("b");
      med.style.left = `calc(${((r.p50 / top) * 100).toFixed(1)}% - 1px)`;
      track.append(band, med);
    }
    const val = el("span", "num", r.p50 > 0 ? speed(r.p50) : "—");
    const first = el("small", "muted num", r.first50 > 0 ? secs(r.first50) : "");
    row.append(name, track, val, first);
    const lines = [`${label(r.source)} · ${r.model}`];
    if (r.p50 > 0) lines.push(t("speedRank.tipSpeed", { p50: speed(r.p50), p10: speed(r.p10), p90: speed(r.p90), n: r.timed.toLocaleString() }));
    if (r.first50 > 0) lines.push(t("speedRank.tipFirst", { p50: secs(r.first50), p95: secs(r.first95), n: r.firstN.toLocaleString() }));
    lines.push(`${r.requests.toLocaleString()} ${t("requests")}`);
    tipText(row, lines.join("\n"));
    box.append(row);
  }
  const more = $("speed-more");
  more.hidden = rows.length <= 8;
  more.textContent = speedAll ? t("table.less") : t("table.more", { n: rows.length });
  $("speed-empty").hidden = rows.length > 0;
  $("speed-empty").textContent = v.focus && v.period === "day" ? t("speedRank.hourNote") : t("speedRank.empty");
  $("speed-sub").textContent = rows.length ? t("speedRank.sub") : "";
}

// --- activity ---------------------------------------------------------------
// The last 53 weeks, a square a day, darker for more tokens.

let activity = null;
let activityAt = 0;
async function refreshActivity(force) {
  if (!force && Date.now() - activityAt < 60000) return;
  activityAt = Date.now();
  try {
    activity = await (await fetch("/api/activity", { cache: "no-store" })).json();
    renderActivity();
  } catch { activityAt = 0; }
}

function renderActivity() {
  const a = activity;
  if (!a) return;
  const days = a.days || [];
  const box = $("heatmap");
  box.replaceChildren();
  if (!days.length) return;
  const ns = (tag, attrs) => {
    const e = document.createElementNS("http://www.w3.org/2000/svg", tag);
    for (const [k, val] of Object.entries(attrs)) e.setAttribute(k, val);
    return e;
  };
  const offset = new Date(days[0].date + "T12:00:00").getDay(); // weeks start on Sunday
  const cols = Math.ceil((days.length + offset) / 7);
  const W = 360, padT = 12, cell = W / cols, H = padT + cell * 7;
  const svg = ns("svg", { viewBox: `0 0 ${W} ${H.toFixed(1)}`, role: "img",
    "aria-label": t("activity.aria", { n: a.activeDays, c: a.current, l: a.longest }) });
  // four shades split at the quartiles of the active days
  const vals = days.map((d) => d.tokens).filter((x) => x > 0).sort((x, y) => x - y);
  const q = (f) => vals[Math.min(vals.length - 1, Math.floor(f * vals.length))] || 0;
  const cuts = [q(0.25), q(0.5), q(0.75)];
  const level = (x) => (x <= 0 ? 0 : x <= cuts[0] ? 1 : x <= cuts[1] ? 2 : x <= cuts[2] ? 3 : 4);
  // a month's name over the first week that starts in it
  let lastMonth = "", lastLabelCol = -9;
  days.forEach((d, i) => {
    const k = i + offset, col = Math.floor(k / 7), row = k % 7;
    const x = col * cell, y = padT + row * cell;
    const r = ns("rect", { x: (x + 0.6).toFixed(1), y: (y + 0.6).toFixed(1), width: (cell - 1.2).toFixed(1), height: (cell - 1.2).toFixed(1), rx: 1.5, class: "hm l" + level(d.tokens) });
    tipText(r, `${dayLabel(d.date, { weekday: "short", year: "numeric", month: "short", day: "numeric" })}\n` +
      (d.requests ? `${tokens(d.tokens)} tokens · ${usd(d.costMicro)} · ${d.requests.toLocaleString()} ${t("requests")}` : t("activity.none")));
    svg.append(r);
    const month = d.date.slice(0, 7);
    if (month !== lastMonth) {
      lastMonth = month;
      const at = row === 0 ? col : col + 1; // the first full week of the month
      if (at - lastLabelCol >= 3 && at < cols - 1) {
        const tx = ns("text", { x: (at * cell).toFixed(1), y: 9, class: "axis" });
        tx.textContent = dayLabel(month + "-01", { month: "short" });
        svg.append(tx);
        lastLabelCol = at;
      }
    }
  });
  box.append(svg);
  $("a-current").textContent = t("activity.daysN", { n: a.current });
  $("a-longest").textContent = t("activity.daysN", { n: a.longest });
  tipText($("a-longest").parentElement, a.longestTo ? t("activity.endedOn", { d: a.longestTo }) : "");
  $("a-days").textContent = String(a.activeDays);
  $("a-best").textContent = a.best && a.best.tokens ? tokens(a.best.tokens) : "—";
  tipText($("a-best").parentElement, a.best && a.best.date ? `${dayLabel(a.best.date, { year: "numeric", month: "short", day: "numeric" })} · ${usd(a.best.costMicro)}` : "");
  $("activity-sub").textContent = a.totalTokens ? t("activity.sub", { t: tokens(a.totalTokens) }) : "";
}

// --- settings -----------------------------------------------------------------

async function refreshSettings() {
  try {
    const r = await fetch("/api/settings", { cache: "no-store" });
    settingsView = await r.json();
    renderSettings();
  } catch { /* the next open tries again */ }
}

function renderSettings() {
  const sv = settingsView;
  if (!sv) return;
  $("set-language").value = sv.settings.language;
  $("set-menubar").value = sv.settings.menuBar;
  $("set-theme").value = sv.settings.theme;
  $("set-numbers").value = sv.settings.numbers || "metric";
  renderSettingsExtras(sv);
  $("set-datadir").textContent = sv.dataDir;
  const cq = $("set-claude-quota");
  cq.checked = !!sv.settings.claudeQuota;
  cq.disabled = !sv.canBridge && !sv.settings.claudeQuota;
  $("bridge-status").textContent = !sv.canBridge && !sv.settings.claudeQuota ? t("bridge.unavailable") : t("bridge." + sv.claudeBridge);
  $("plugin-dir").textContent = sv.pluginDir;
  const pf = $("prices-file");
  pf.replaceChildren(el("span", "mono", sv.pricesPath), document.createElement("br"),
    el("small", sv.pricesError ? "" : "muted", sv.pricesError || (sv.pricesCount ? t("prices.count", { n: sv.pricesCount }) : t("prices.none"))));
  if (sv.pricesError) pf.lastChild.style.color = "var(--red)";
  const pl = $("plugin-list");
  pl.replaceChildren();
  if (!(sv.plugins || []).length) pl.append(el("li", "muted small", t("plugins.none")));
  for (const p of sv.plugins || []) {
    const li = el("li");
    const meta = el("div", "meta");
    const name = el("span");
    if (!p.error) name.append(dot("plugin." + p.id), document.createTextNode(" "));
    name.append(document.createTextNode(p.label || p.id));
    const small = el("small", "", p.error ? t("plugins.error", { e: p.error }) : `${p.id} · ${t("plugins.kind." + p.type)}`);
    if (p.error) small.style.color = "var(--red)";
    meta.append(name, small);
    li.append(meta);
    pl.append(li);
  }
  const subs = $("set-subs");
  subs.replaceChildren();
  const fees = (desired || sv.settings).subscriptions || {};
  for (const src of sv.sources.filter((x) => (x.enabled && x.detected) || fees[x.id] > 0)) {
    const lab = el("label");
    const input = el("input");
    input.type = "number";
    input.min = "0";
    input.step = "1";
    input.placeholder = "0";
    input.value = fees[src.id] > 0 ? String(fees[src.id]) : "";
    input.setAttribute("aria-label", `${src.label} $/month`);
    input.addEventListener("change", () => {
      const next = { ...((desired || settingsView.settings).subscriptions || {}) };
      const v = Number(input.value);
      if (v > 0) next[src.id] = v;
      else delete next[src.id];
      changeSetting({ subscriptions: next });
    });
    lab.append(dot(src.id), el("span", "", src.label), el("span", "muted", "$"), input);
    subs.append(lab);
  }
  $("set-version").textContent = sv.version;
  if (snap) $("set-prices").textContent = `v${snap.pricesVersion} (${snap.pricesUpdated})`;
  const ul = $("set-sources");
  ul.replaceChildren();
  const ordered = byOrder(sv.sources, (x) => x.id);
  const st = desired || sv.settings;
  ordered.forEach((s, i) => {
    sourceInfo[s.id] = { label: s.label, color: s.color };
    const li = el("li");
    const meta = el("div", "meta");
    const name = el("span");
    name.append(dot(s.id), document.createTextNode(" " + s.label));
    if (s.online) name.append(el("span", "badge", t("source.online")));
    if (s.plugin) name.append(el("span", "badge", t("source.plugin")));
    const note = s.runsCode ? t("source.runsCode") : s.online ? t("source.onlineNote") : t(s.detected ? "source.detected" : "source.missing");
    meta.append(name, el("small", "", note));
    const sw = el("input", "switch");
    sw.type = "checkbox";
    sw.setAttribute("role", "switch");
    sw.setAttribute("aria-label", s.label);
    sw.checked = s.enabled;
    sw.addEventListener("change", () => {
      const sources = { ...((desired || sv.settings).sources || {}) };
      sources[s.id] = sw.checked;
      changeSetting({ sources });
    });
    // move up or down; hide from the overview
    const tools = el("span", "mini-tools");
    const move = (d) => {
      const ids = ordered.map((x) => x.id);
      const j = i + d;
      if (j < 0 || j >= ids.length) return;
      [ids[i], ids[j]] = [ids[j], ids[i]];
      changeSetting({ order: ids });
    };
    for (const [d, glyph, key] of [[-1, "↑", "order.up"], [1, "↓", "order.down"]]) {
      const b = el("button", "mini", glyph);
      b.type = "button";
      b.disabled = i + d < 0 || i + d >= ordered.length;
      b.setAttribute("aria-label", t(key, { a: s.label }));
      tipText(b, t(key, { a: s.label }));
      b.addEventListener("click", () => move(d));
      tools.append(b);
    }
    const hidden = (st.hidden || []).includes(s.id);
    const eye = el("button", "mini" + (hidden ? " off" : ""), hidden ? "◌" : "◉");
    eye.type = "button";
    eye.setAttribute("aria-pressed", String(!hidden));
    eye.setAttribute("aria-label", t("order.showIn", { a: s.label }));
    tipText(eye, t(hidden ? "order.hidden" : "order.shown"));
    eye.addEventListener("click", () => {
      const cur = new Set((desired || settingsView.settings).hidden || []);
      if (cur.has(s.id)) cur.delete(s.id);
      else cur.add(s.id);
      changeSetting({ hidden: [...cur] });
    });
    tools.append(eye);
    li.append(meta, tools, sw);
    ul.append(li);
  });
  if ((st.order || []).length) {
    const reset = el("button", "link small-link", t("order.reset"));
    reset.type = "button";
    reset.addEventListener("click", () => changeSetting({ order: [] }));
    ul.append(reset);
  }
  renderPlaces(sv);
}

// --- where logs are read ----------------------------------------------------

const openPlaces = new Set();
function lines(text) { return text.split(/\r?\n/).map((x) => x.trim()).filter(Boolean); }

function renderPlaces(sv) {
  const st = desired || sv.settings;
  $("wsl-field").hidden = $("wsl-note").hidden = !sv.wsl;
  $("set-wsl").checked = !!st.wsl;
  const homes = $("set-homes");
  if (document.activeElement !== homes) homes.value = (st.homes || []).join("\n");
  homes.placeholder = t("places.homesPh");
  const box = $("set-paths");
  if (box.contains(document.activeElement)) return; // typing: keep it
  box.replaceChildren();
  for (const src of sv.sources.filter((x) => x.logs && (x.enabled || ((st.paths || {})[x.id] || []).length))) {
    const extra = (st.paths || {})[src.id] || [];
    const d = el("details", "place");
    d.open = openPlaces.has(src.id);
    d.addEventListener("toggle", () => (d.open ? openPlaces.add(src.id) : openPlaces.delete(src.id)));
    const sum = el("summary");
    sum.append(dot(src.id), el("span", "", src.label), el("small", "muted", t("places.count", { n: (src.roots || []).length })));
    d.append(sum);
    const ul = el("ul", "roots mono");
    for (const r of src.roots || []) ul.append(el("li", "", r));
    if (!(src.roots || []).length) ul.append(el("li", "muted", t("places.noneYet")));
    const ta = el("textarea", "paths");
    ta.rows = 2;
    ta.spellcheck = false;
    ta.placeholder = t("places.pathsPh");
    ta.value = extra.join("\n");
    ta.setAttribute("aria-label", t("places.pathsFor", { a: src.label }));
    ta.addEventListener("change", () => {
      const paths = { ...((desired || settingsView.settings).paths || {}) };
      const list = lines(ta.value);
      if (list.length) paths[src.id] = list;
      else delete paths[src.id];
      changeSetting({ paths });
    });
    d.append(ul, ta);
    box.append(d);
  }
}

// desired is the settings with every change made so far, saved in order:
// two quick changes never save one over the other with a stale copy
let desired = null;
let saving = Promise.resolve();
function changeSetting(patch) {
  if (!settingsView) return;
  desired = { ...(desired || settingsView.settings), ...patch };
  const next = desired;
  saving = saving.then(() => saveSettings(next));
}

function renderSettingsExtras(sv) {
  const st = (desired || sv.settings);
  const al = st.alerts || {};
  if (document.activeElement !== $("set-alert-quota")) $("set-alert-quota").value = (al.quota || []).join(", ");
  $("set-alert-pace").checked = !!al.pace;
  $("set-alert-budget").checked = !!al.budget;
  $("set-alert-peak").checked = !!al.peak;

  const cur = $("set-currency");
  if (!cur.options.length) {
    for (const code of Object.keys(sv.currencies || { USD: "$" }).sort((a, b) => (a === "USD" ? -1 : b === "USD" ? 1 : a.localeCompare(b)))) {
      const o = el("option", "", `${code} ${sv.currencies[code]}`);
      o.value = code;
      cur.append(o);
    }
  }
  const c = st.currency || { code: "USD", rate: 1 };
  cur.value = c.code;
  const rate = $("set-rate");
  rate.disabled = c.code === "USD";
  if (document.activeElement !== rate) rate.value = c.rate;
  $("rate-note").textContent = c.code === "USD" ? "" : t("currency.rate", { r: c.rate, c: c.code });
  for (const e of document.querySelectorAll(".cur-sym")) e.textContent = CUR.sym;
  const b = st.budget || {};
  for (const [id, v] of [["set-budget-daily", b.daily], ["set-budget-monthly", b.monthly]]) {
    if (document.activeElement !== $(id)) $(id).value = v > 0 ? +(v * c.rate).toFixed(2) : "";
  }

  const login = $("set-login");
  login.checked = !!st.launchAtLogin;
  login.disabled = !sv.canBridge && !st.launchAtLogin;
  const sc = $("set-shortcut");
  if (st.shortcut && ![...sc.options].some((o) => o.value === st.shortcut)) {
    const o = el("option", "", st.shortcut);
    o.value = st.shortcut;
    sc.append(o);
  }
  sc.value = st.shortcut || "";
  sc.disabled = !sv.canBridge;
  $("set-updates").checked = !!st.updateCheck;
  $("set-orb").checked = !!(st.orb && st.orb.on);
  $("set-auto-update").checked = !!st.autoUpdate;
  $("set-auto-update").disabled = !st.updateCheck;
  const ust = sv.updateState || {};
  $("update-install").hidden = !(sv.latest && sv.latest.newer && ust.canInstall) || !!ust.step;
  $("set-price-sync").checked = !!st.priceSync;
  renderPriceSync(sv);
  const us = $("update-status");
  const get = $("update-get");
  get.hidden = !(sv.latest && sv.latest.newer);
  us.textContent = ust.step ? t("update." + ust.step) : ust.error ? t("update.failed", { e: ust.error })
    : !st.updateCheck ? "" : sv.updateError ? t("system.updateError", { e: sv.updateError })
    : sv.latest ? (sv.latest.newer ? t("system.newer", { v: sv.latest.version }) + (ust.reason ? " · " + t("update.manual") : "") : t("system.latest", { v: sv.version })) : "";
  if (ust.reason) tipText(us, ust.reason);
  else delete us.dataset.tip;
  renderStatusPick();
}

function renderPriceSync(sv, note) {
  const pt = sv.priceTable || {};
  const sync = pt.sync || {};
  const parts = [t(pt.version > pt.bundled ? "priceSync.synced" : "priceSync.bundled", { v: pt.version, d: pt.updated })];
  if (note) parts.push(note);
  else if (sync.lastError && sync.lastAttemptMs > (sync.lastSuccessMs || 0)) parts.push(t("priceSync.failed"));
  else if (sync.lastSuccessMs) parts.push(t("priceSync.checked", { t: clock(sync.lastSuccessMs, true) }));
  $("price-sync-status").textContent = parts.join(" · ");
  if (sync.lastError) tipText($("price-sync-status"), sync.lastError);
  else delete $("price-sync-status").dataset.tip;
}

// patch merges into a nested setting (alerts, budget, currency)
function patchNested(key, patch) {
  const cur = (desired || settingsView.settings)[key] || {};
  changeSetting({ [key]: { ...cur, ...patch } });
}

async function post(url, body) {
  const r = await fetch(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body || {}) });
  if (!r.ok) throw new Error(await r.text());
  return r.status === 204 ? null : r.json();
}

// the custom prices editor works on the file's own document, so fields it
// doesn't show (tiers, periods, providers' multipliers) are kept
let priceDoc = null;
async function loadPrices() {
  try {
    priceDoc = await (await fetch("/api/prices", { cache: "no-store" })).json();
    priceDoc.models ||= [];
    priceDoc.multipliers ||= {};
    priceDoc.multipliers.sources ||= {};
    renderPrices();
  } catch (e) {
    $("prices-status").textContent = String(e);
  }
}

function renderPrices() {
  const table = $("price-rows");
  table.replaceChildren();
  const head = el("tr");
  for (const k of ["prices.model", "prices.in", "prices.cached", "prices.write", "prices.out", ""]) head.append(el("th", "", k ? t(k) : ""));
  table.append(head);
  priceDoc.models.forEach((m, i) => {
    m.prices ||= {};
    const tr = el("tr");
    const cell = (value, onInput, type) => {
      const td = el("td");
      const input = el("input");
      input.type = type || "number";
      if (input.type === "number") { input.min = "0"; input.step = "any"; }
      input.value = value ?? "";
      input.addEventListener("input", () => onInput(input.value));
      td.append(input);
      return td;
    };
    const num = (k) => cell(m.prices[k], (v) => { if (v === "") delete m.prices[k]; else m.prices[k] = Number(v); });
    tr.append(cell(m.id, (v) => (m.id = v.trim()), "text"), num("input"), num("cachedInput"), num("cacheWrite"), num("output"));
    const td = el("td");
    const del = el("button", "del", "×");
    del.type = "button";
    del.addEventListener("click", () => { priceDoc.models.splice(i, 1); renderPrices(); });
    td.append(del);
    tr.append(td);
    table.append(tr);
  });
  const mult = $("mult-rows");
  mult.replaceChildren();
  for (const src of (settingsView ? settingsView.sources : []).filter((x) => (x.enabled && x.detected) || priceDoc.multipliers.sources[x.id] != null)) {
    const lab = el("label");
    const input = el("input");
    input.type = "number";
    input.min = "0";
    input.step = "0.05";
    input.placeholder = "1";
    input.value = priceDoc.multipliers.sources[src.id] ?? "";
    input.addEventListener("input", () => {
      if (input.value === "") delete priceDoc.multipliers.sources[src.id];
      else priceDoc.multipliers.sources[src.id] = Number(input.value);
    });
    lab.append(dot(src.id), el("span", "", src.label), el("span", "muted", "×"), input);
    mult.append(lab);
  }
}

async function saveSettings(next) {
  try {
    const r = await fetch("/api/settings", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(next) });
    if (!r.ok) throw new Error(await r.text());
    settingsView = await r.json();
    setLang(settingsView.settings.language);
    applyTheme(settingsView.settings.theme);
    NUMBERS = settingsView.settings.numbers || "metric";
    setCurrency(settingsView);
    applyI18n();
    renderSettings();
    if (snap) render(snap);
    renderStats();
    renderActivity();
    if (openSessionId && lastDetail) renderSessionDetail(lastDetail);
    $("saved").textContent = t("settings.saved");
    setTimeout(() => ($("saved").textContent = ""), 1500);
  } catch (e) {
    desired = null;
    $("saved").textContent = String(e).replace(/^Error: /, "");
    refreshSettings();
  }
}

$("set-claude-quota").addEventListener("change", (e) => changeSetting({ claudeQuota: e.target.checked }));

$("set-alert-quota").addEventListener("change", (e) => {
  const q = e.target.value.split(/[,，\s]+/).map(Number).filter((x) => x > 0 && x <= 100);
  patchNested("alerts", { quota: q });
});
for (const [id, key] of [["set-alert-pace", "pace"], ["set-alert-budget", "budget"], ["set-alert-peak", "peak"]]) {
  $(id).addEventListener("change", (e) => patchNested("alerts", { [key]: e.target.checked }));
}
$("set-currency").addEventListener("change", (e) => {
  const code = e.target.value;
  const old = (desired || settingsView.settings).currency || {};
  patchNested("currency", { code, rate: code === "USD" ? 1 : old.code === code ? old.rate : 1 });
});
$("set-rate").addEventListener("change", (e) => {
  const r = Number(e.target.value);
  if (r > 0) patchNested("currency", { rate: r });
});
for (const [id, key] of [["set-budget-daily", "daily"], ["set-budget-monthly", "monthly"]]) {
  $(id).addEventListener("change", (e) => {
    const rate = ((desired || settingsView.settings).currency || {}).rate || 1;
    const v = Number(e.target.value);
    patchNested("budget", { [key]: v > 0 ? +(v / rate).toFixed(6) : 0 });
  });
}
$("set-login").addEventListener("change", (e) => changeSetting({ launchAtLogin: e.target.checked }));
$("set-shortcut").addEventListener("change", (e) => changeSetting({ shortcut: e.target.value }));
$("set-updates").addEventListener("change", async (e) => {
  changeSetting({ updateCheck: e.target.checked });
  if (e.target.checked) {
    await saving;
    try { settingsView = await post("/api/update"); renderSettings(); } catch { /* shown on the next open */ }
  }
});
$("set-orb").addEventListener("change", (e) => patchNested("orb", { on: e.target.checked }));
$("set-auto-update").addEventListener("change", (e) => changeSetting({ autoUpdate: e.target.checked }));
$("update-install").addEventListener("click", async (e) => {
  e.target.disabled = true;
  try { settingsView = await post("/api/update/install"); renderSettings(); } catch (err) { $("update-status").textContent = String(err).replace(/^Error: /, ""); }
  e.target.disabled = false;
  // while it downloads, keep the step shown; the app restarts when done
  const poll = setInterval(async () => {
    try {
      settingsView = await (await fetch("/api/settings", { cache: "no-store" })).json();
      renderSettings();
      if (!(settingsView.updateState || {}).step) clearInterval(poll);
    } catch { clearInterval(poll); }
  }, 1000);
});
$("status-refresh").addEventListener("click", async (e) => {
  e.target.disabled = true;
  $("status-sub").textContent = t("status.checking");
  await refreshStatus(true);
  e.target.disabled = false;
});
$("set-status-check").addEventListener("change", async (e) => {
  changeSetting({ statusCheck: e.target.checked });
  await saving;
  refreshStatus(e.target.checked);
});
$("set-status-auto").addEventListener("change", async (e) => {
  const ids = ((statusView && statusView.providers) || []).map((p) => p.id);
  changeSetting(e.target.checked ? { statusCustom: false } : { statusCustom: true, statusProviders: ids });
  await saving;
  refreshStatus();
});
$("set-price-sync").addEventListener("change", (e) => changeSetting({ priceSync: e.target.checked }));
$("price-sync-now").addEventListener("click", async (e) => {
  e.target.disabled = true;
  $("price-sync-status").textContent = t("priceSync.checking");
  try {
    const r = await post("/api/prices/sync");
    settingsView = { ...settingsView, ...r };
    renderPriceSync(settingsView, r.syncError ? t("priceSync.failed") : r.changed ? t("priceSync.updated") : t("priceSync.upToDate"));
  } catch (err) {
    $("price-sync-status").textContent = String(err).replace(/^Error: /, "");
  }
  e.target.disabled = false;
});
$("set-wsl").addEventListener("change", (e) => changeSetting({ wsl: e.target.checked }));
$("set-homes").addEventListener("change", (e) => changeSetting({ homes: lines(e.target.value) }));
$("update-get").addEventListener("click", () => post("/api/open", { target: "releases" }).catch(() => {}));
$("open-plugins").addEventListener("click", () => post("/api/open", { target: "plugins" }).catch(() => {}));
$("open-data").addEventListener("click", () => post("/api/open", { target: "data" }).catch(() => {}));
$("prices-editor").addEventListener("toggle", (e) => { if (e.target.open) loadPrices(); });
$("price-add").addEventListener("click", () => { priceDoc.models.push({ id: "", prices: {} }); renderPrices(); });
$("prices-save").addEventListener("click", async () => {
  priceDoc.models = priceDoc.models.filter((m) => m.id);
  try {
    priceDoc = await post("/api/prices", priceDoc);
    priceDoc.models ||= [];
    priceDoc.multipliers ||= {};
    priceDoc.multipliers.sources ||= {};
    renderPrices();
    $("prices-status").textContent = t("prices.saved");
    refreshSettings();
  } catch (e) {
    $("prices-status").textContent = String(e).replace(/^Error: /, "");
  }
});
for (const fmtName of ["csv", "json"]) {
  $("export-" + fmtName).addEventListener("click", async () => {
    const st = $("export-status");
    try {
      const r = await post("/api/export", { period: prefs.period, format: fmtName });
      st.replaceChildren(document.createTextNode(t("export.saved", { p: r.path }) + " "));
      const show = el("button", "link", t("export.reveal"));
      show.type = "button";
      show.addEventListener("click", () => post("/api/open", { target: "export" }).catch(() => {}));
      st.append(show);
    } catch (e) {
      st.textContent = String(e).replace(/^Error: /, "");
    }
  });
}

$("reload").addEventListener("click", async () => {
  try {
    const r = await fetch("/api/reload", { method: "POST" });
    if (!r.ok) throw new Error(await r.text());
    settingsView = await r.json();
    renderSettings();
    $("saved").textContent = t("plugins.reloaded");
    setTimeout(() => ($("saved").textContent = ""), 1500);
  } catch (e) {
    $("saved").textContent = String(e);
  }
});

for (const [id, key] of [["set-language", "language"], ["set-menubar", "menuBar"], ["set-theme", "theme"], ["set-numbers", "numbers"]]) {
  $(id).addEventListener("change", (e) => changeSetting({ [key]: e.target.value }));
}

// --- loop -----------------------------------------------------------------------

function render(s) {
  for (const src of s.sources || []) sourceInfo[src.id] = { label: src.label, color: src.color };
  $("loading").hidden = s.ready;
  if (!s.ready) {
    $("loading-text").textContent = t("loading", { done: s.seedDone, total: s.seedTotal || "…" });
    return;
  }
  renderOverview(s);
  renderSessions(s);
  retip();
}

let ticks = 0;
async function tick() {
  try {
    const r = await fetch("/api/snapshot", { cache: "no-store" });
    snap = await r.json();
    render(snap);
    if (prefs.tab === "stats" && ticks % 5 === 0) { refreshPeriod(); refreshActivity(); }
    if (prefs.tab === "overview" && ticks % 10 === 0) refreshAlerts();
    if (ticks % 30 === 0) refreshStatus();
    if (prefs.tab === "sessions" && ticks % 5 === 0) refreshSessions();
    ticks++;
  } catch (e) {
    $("loading").hidden = false;
    $("loading-text").textContent = t("err.backend", { e });
  }
}

async function start() {
  setLang("auto");
  try {
    const r = await fetch("/api/settings", { cache: "no-store" });
    settingsView = await r.json();
    setLang(settingsView.settings.language);
    applyTheme(settingsView.settings.theme);
    NUMBERS = settingsView.settings.numbers || "metric";
    setCurrency(settingsView);
  } catch { /* defaults */ }
  applyI18n();
  fetch("/api/locale", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ lang: navigator.language || "" }) }).catch(() => {});
  seg("period", prefs.period, (v) => { prefs.period = v; focus = ""; save("prefs", prefs); refreshPeriod(true); });
  seg("sessions-range", sessionsRange, (v) => { sessionsRange = v; refreshSessions(); });
  $("dim-filter").addEventListener("input", (e) => { dimFilter = e.target.value.trim().toLowerCase(); renderStats(); });
  $("dim-more").addEventListener("click", () => { dimAll = !dimAll; renderStats(); });
  $("speed-more").addEventListener("click", () => { speedAll = !speedAll; renderStats(); });
  $("sd-more").addEventListener("click", () => { sdAll = !sdAll; renderRequestRows(); });
  $("session-back").addEventListener("click", closeSession);
  seg("chart-by", prefs.chartBy || "source", (v) => { prefs.chartBy = v; save("prefs", prefs); renderStats(); });
  seg("today-by", prefs.todayBy || "source", (v) => { prefs.todayBy = v; save("prefs", prefs); if (snap) renderTodayRows(snap.today); });
  seg("metric", prefs.metric, (v) => { prefs.metric = v; save("prefs", prefs); renderStats(); });
  seg("dim", prefs.dim, (v) => { prefs.dim = v; dimAll = false; save("prefs", prefs); renderStats(); });
  $("to-sessions").addEventListener("click", () => showTab("sessions"));
  $("quit").addEventListener("click", () => fetch("/api/quit", { method: "POST" }));
  showTab(prefs.tab || "overview");
  // the app may have changed settings while the panel was hidden (the
  // tray's orb item, the orb's position): read them again when shown
  document.addEventListener("visibilitychange", async () => {
    if (document.hidden) return;
    await saving; // every change made here is saved by then
    desired = null;
    refreshSettings();
  });
  refreshAlerts();
  renderSettings();
  tick();
  setInterval(tick, 1000);
}
start();
