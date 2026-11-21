/* Pages of widgets on a grid. The server sends the layout once (versioned)
   and widget data every poll; this file only draws. */
import { $, POLL_MS, ago, apiHeaders, color, dayKey, fmtPart, h, hourOf, minuteOfDay, now, qs, setTap, tickers, timeoutSignal, ui } from "./js/core.js";
import { canAct, closeSheet, openSheet, sheet } from "./js/actions.js";
import time from "./js/widgets/time.js";
import infra from "./js/widgets/infra.js";
import generic from "./js/widgets/generic.js";

// Widget types: init(inst) builds DOM once per layout; update(inst, data)
// runs every poll.
const W = { ...time, ...infra, ...generic };

const POLL_TIMEOUT_MS = 8000; // a half-open socket must not stall polling forever
const FULL_EVERY = 30; // polls between fetches of every page (others: current page only)
const OFFLINE_AFTER = 3; // failed polls before the screen says it is offline

let pollTimer = null, inflight = false, again = false;

/* ───────── pages, swipe, dots ───────── */
let layout = null, version = "", pageIdx = 0;
const insts = new Map();
const stage = $("stage"), track = $("track"), dots = $("dots");
let lastTurn = Date.now();

/* ───────── look: text size tier and colour theme ───────── */
// Names must match internal/config/dash.go (TextSizes, Themes). The URL wins
// over the config so one config can serve a Hub and a Hub Max differently.
const SIZES = { small: 0.9, medium: 1, large: 1.15, xlarge: 1.3 };
const THEMES = ["petrol", "graphite", "midnight", "forest", "ember", "contrast", "paper"];
const pick = (url, conf, ok) => (ok(url) ? url : ok(conf) ? conf : "");
const isTheme = (t) => THEMES.includes(t);
function applyLook() {
  const l = layout || {};
  const size = pick(qs.get("size"), l.text_size, (s) => Object.hasOwn(SIZES, s)) || "medium";
  document.documentElement.style.setProperty("--scale", SIZES[size]);
  const day = pick(qs.get("theme"), l.theme, isTheme) || "petrol";
  const night = pick(qs.get("theme_night"), l.theme_night, isTheme) || day;
  const theme = document.body.dataset.phase === "night" ? night : day;
  const b = document.body;
  if (b.dataset.theme === theme) return;
  // The body fades its ground over a minute (time-of-day tint), but a new
  // theme swaps text colours at once: fading only the ground would leave
  // dark text on a dark ground (or light on light) for that minute. So a
  // theme change lands instantly; tints within one theme still fade.
  b.classList.add("theme-swap");
  b.dataset.theme = theme;
  void getComputedStyle(b).backgroundColor; // commit the new ground untransitioned
  requestAnimationFrame(() => b.classList.remove("theme-swap"));
}
applyLook();

function build(l) {
  const keep = layout ? layout.pages[pageIdx]?.name : qs.get("page");
  layout = ui.layout = l;
  ui.tz = l.timezone || undefined;
  document.title = l.title || "Dashboard";
  insts.clear();
  tickers.clear();
  track.replaceChildren();
  document.documentElement.style.setProperty("--cols", l.grid[0]);
  document.documentElement.style.setProperty("--rows", l.grid[1]);
  applyLook();
  l.pages.forEach((p, pi) => {
    const pg = h("div", "page");
    pg.dataset.name = p.name;
    // One slide of the carousel (WAI carousel pattern): a labelled group, so
    // the page name is announced; a role-less div would drop the label.
    pg.setAttribute("role", "group");
    pg.setAttribute("aria-roledescription", "页面");
    pg.setAttribute("aria-label", p.title || p.name);
    p.widgets.forEach((w, wi) => {
      const el = h("section", `w w-${w.type}${w.layout ? ` layout-${w.layout}` : ""}`);
      el.dataset.id = w.id;
      const [c, r, cw, rh] = w.at;
      el.style.gridArea = `${r + 1} / ${c + 1} / span ${rh} / span ${cw}`;
      el.style.setProperty("--i", wi);
      if (w.accent) el.style.setProperty("--accent", color(w.accent));
      const inst = { w, el, page: pi };
      if (w.title && w.type !== "clock") {
        const hd = h("header", "wt");
        inst.note = h("span", "note");
        hd.append(h("span", "t", w.title), inst.note);
        el.append(hd);
      }
      inst.body = h("div", "wb");
      el.append(inst.body);
      // A widget that throws while building must not take the rest of the
      // dashboard (other tiles, pages, dots) down with it.
      inst.impl = W[w.type] || W.unknown;
      try { inst.impl.init(inst); } catch (e) {
        console.error(w.id, e);
        inst.impl = W.unknown;
        inst.body.replaceChildren(h("p", "note", `组件加载失败：${w.type}`));
        el.classList.add("errored");
        inst.broken = true; // stays errored whatever data arrives
      }
      if (w.actions && w.actions.length && w.type !== "buttons" && canAct()) {
        setTap(el, true);
        el.addEventListener("click", () => openSheet({ title: w.title || "", actions: w.actions, param: "" }));
      }
      insts.set(w.id, inst);
      pg.append(el);
    });
    track.append(pg);
  });
  dots.replaceChildren(...l.pages.map((p, k) => {
    const b = h("button", "dot");
    b.type = "button";
    b.setAttribute("aria-label", p.title || p.name);
    b.addEventListener("click", (e) => { e.stopPropagation(); ui.lastTouch = Date.now(); go(k); });
    return b;
  }));
  dots.hidden = l.pages.length < 2;
  let k = l.pages.findIndex((p) => p.name === keep);
  if (k < 0) k = Math.max(0, l.pages.findIndex((p) => p.name === l.home));
  go(k, false);
  // Last: a build that threw part-way leaves the old version, so the next
  // poll asks for the layout again instead of keeping a half-built page.
  version = l.version;
}

function go(k, animate = true) {
  if (!layout) return;
  const dir = Math.sign(k - pageIdx);
  const turned = k !== pageIdx;
  pageIdx = ui.pageIdx = k;
  lastTurn = Date.now();
  track.classList.toggle("instant", !animate);
  track.style.transform = `translateX(${-k * 100}%)`;
  [...dots.children].forEach((d, j) => {
    d.classList.toggle("on", j === k);
    if (j === k) d.setAttribute("aria-current", "page");
    else d.removeAttribute("aria-current");
  });
  [...track.children].forEach((pg, j) => {
    pg.setAttribute("aria-hidden", j === k ? "false" : "true");
    pg.inert = j !== k; // offscreen tiles take no focus or taps
    if (j === k && animate && dir) {
      pg.style.setProperty("--dir", dir);
      if (pg._enterDone) pg._enterDone(); // a turn still animating
      pg.classList.remove("enter");
      void pg.offsetWidth;
      pg.classList.add("enter");
      // Widgets trail in one after another (--i × 35ms); drop the class when
      // the last one has finished, however many the page has.
      const ws = pg.querySelectorAll(".w"), last = ws[ws.length - 1];
      const onEnd = (e) => { if (e.target === last && e.animationName === "trail") done(); };
      const done = () => {
        clearTimeout(pg._enterT);
        if (last) last.removeEventListener("animationend", onEnd);
        pg._enterDone = null;
        pg.classList.remove("enter");
      };
      pg._enterDone = done;
      if (last) last.addEventListener("animationend", onEnd);
      pg._enterT = setTimeout(done, 900 + ws.length * 35 + 200); // no animation (reduced motion)
    }
  });
  const pn = $("pn");
  pn.textContent = layout.pages.length > 1 ? layout.pages[k].title || layout.pages[k].name : "";
  pn.classList.remove("flash");
  void pn.offsetWidth;
  if (animate) pn.classList.add("flash");
  // Only the visible page is polled: fetch the arriving one now (it keeps
  // showing its last data until the answer lands).
  if (turned && animate) poll();
}

let drag = null, moved = false;
stage.addEventListener("pointerdown", (e) => {
  ui.lastTouch = Date.now();
  moved = false;
  if (!layout || e.target.closest(".btn")) return;
  drag = { x: e.clientX, y: e.clientY, t: performance.now(), id: e.pointerId, axis: null, dx: 0 };
});
addEventListener("pointermove", (e) => {
  if (!drag || e.pointerId !== drag.id) return;
  const dx = e.clientX - drag.x, dy = e.clientY - drag.y;
  if (!drag.axis) {
    if (Math.abs(dx) < 10 && Math.abs(dy) < 10) return;
    drag.axis = Math.abs(dx) > Math.abs(dy) ? "x" : "y";
    if (drag.axis === "x") track.classList.add("dragging");
  }
  moved = true;
  if (drag.axis !== "x") return;
  const last = layout.pages.length - 1;
  let d = dx;
  if ((pageIdx === 0 && d > 0) || (pageIdx === last && d < 0)) d *= 0.28; // rubber band at the ends
  drag.dx = d;
  track.style.transform = `translateX(calc(${-pageIdx * 100}% + ${d.toFixed(1)}px))`;
});
function endDrag(e) {
  if (!drag || e.pointerId !== drag.id) return;
  const d = drag;
  drag = null;
  if (d.axis !== "x") return;
  track.classList.remove("dragging");
  const v = d.dx / Math.max(1, performance.now() - d.t); // px/ms
  const w = stage.clientWidth;
  let k = pageIdx;
  if (d.dx < -w * 0.18 || v < -0.45) k++;
  else if (d.dx > w * 0.18 || v > 0.45) k--;
  go(Math.max(0, Math.min(layout.pages.length - 1, k)));
}
addEventListener("pointerup", endDrag);
addEventListener("pointercancel", endDrag);
// A drag must not also count as a tap on whatever it started on.
stage.addEventListener("click", (e) => { if (moved) { e.stopPropagation(); e.preventDefault(); moved = false; } }, true);
addEventListener("keydown", (e) => {
  if (!layout) return;
  ui.lastTouch = Date.now();
  if (e.key === "ArrowRight") go(Math.min(layout.pages.length - 1, pageIdx + 1));
  if (e.key === "ArrowLeft") go(Math.max(0, pageIdx - 1));
  if (e.key === "Escape") closeSheet();
  // Rows and tiles that open a sheet are role="button": activate them by key.
  const t = e.target;
  if ((e.key === "Enter" || e.key === " ") && t instanceof Element && t.matches('.tappable[role="button"]')) {
    e.preventDefault();
    t.click();
  }
});

// win comes from the server already normalized to "HH:MM-HH:MM"; an end of
// "24:00" (1440) means midnight, so "22:00-24:00" and "00:00-24:00" work.
function inQuiet(win) {
  const m = /^(\d\d):(\d\d)-(\d\d):(\d\d)$/.exec(win || "");
  if (!m) return false;
  const a = +m[1] * 60 + +m[2], b = +m[3] * 60 + +m[4], t = minuteOfDay(now());
  return a <= b ? t >= a && t < b : t >= a || t < b;
}

// Once a second: widget tickers, time-of-day tint, idle return / rotation.
setInterval(() => {
  for (const f of tickers) { try { f(); } catch (e) { console.error(e); } }
  const hr = hourOf(now());
  const phase = hr < 5 || hr >= 22 ? "night" : hr < 8 ? "dawn" : hr >= 18 ? "dusk" : "day";
  if (document.body.dataset.phase !== phase) { document.body.dataset.phase = phase; applyLook(); }
  if (!layout || drag) return;
  const idle = (Date.now() - ui.lastTouch) / 1000;
  if (!sheet.hidden && idle > 45) closeSheet();
  if (!sheet.hidden) return;
  const n = layout.pages.length;
  const home = Math.max(0, layout.pages.findIndex((p) => p.name === layout.home));
  if (layout.rotate > 0 && n > 1 && !inQuiet(layout.rotate_quiet)) {
    if (idle > layout.rotate && (Date.now() - lastTurn) / 1000 >= layout.rotate) go((pageIdx + 1) % n);
  } else if (layout.idle_return > 0 && pageIdx !== home && idle > layout.idle_return) go(home);
}, 1000);

/* ───────── polling ───────── */
let failures = 0, authFail = false, lastOk = 0;
// badData: the last answer arrived but could not be parsed or rendered, so
// the screen is not current even though the connection is fine.
let badData = false;
let polls = 0, needAll = true; // first answer fills every page; later ones the visible page
function renderStatus() {
  const upd = $("upd");
  upd.classList.toggle("bad", failures > 0 || badData);
  upd.textContent = authFail ? "访问令牌无效" : failures ? "与 dashboard 服务的连接中断，正在重试"
    : badData ? (lastOk ? `数据异常，显示的是${ago(lastOk)}的数据` : "数据异常，正在重试") : `更新于 ${fmtPart(now(), { hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" })}`;
  // A few misses in a row: say so on the screen itself, not just in the footer.
  const off = ui.offline = failures >= OFFLINE_AFTER;
  document.body.classList.toggle("offline", off);
  const banner = $("offline");
  banner.hidden = !off;
  if (off) banner.textContent = authFail ? "访问令牌无效，数据没有更新" : lastOk ? `连不上 dashboard 服务，显示的是${ago(lastOk)}的数据` : "连不上 dashboard 服务";
}
function apply(r) {
  ui.actions = r.actions || {};
  for (const [id, inst] of insts) {
    const d = r.data[id];
    if (d === undefined) continue;
    const el = inst.el;
    el.classList.toggle("stale", !!d.stale);
    el.classList.toggle("waiting", !!d.waiting);
    el.classList.toggle("errored", !!inst.broken || (!!d.error && !d.updated));
    if (inst.note) {
      let t = "";
      if (d.error) t = d.updated ? `更新失败，显示 ${ago(d.updated)}的数据` : d.error;
      else if (d.waiting) t = "等待数据";
      else if (d.stale && d.updated) t = `${ago(d.updated)}的数据`;
      inst.note.textContent = t;
      inst.note.title = d.error || "";
    }
    ui.tweenOff = inst.page !== pageIdx; // offscreen: jump straight to the value
    try { (inst.impl || W.unknown).update(inst, d); } catch (e) { console.error(inst.w.id, e); }
    ui.tweenOff = false;
    // When some rows carry a "›", pad every row so the value columns line up.
    el.classList.toggle("has-tap", !!inst.body.querySelector(".tappable"));
  }
  for (const b of document.querySelectorAll(".btn")) b._refresh?.();
}
async function poll() {
  if (inflight) { again = true; return; }
  inflight = true;
  clearTimeout(pollTimer);
  try {
    // Only the visible page needs fresh data; every FULL_EVERY polls (and
    // after a new layout) all pages are fetched so a swipe never lands on
    // an empty page. A swipe or rotation also polls at once (see go).
    const page = !needAll && layout ? layout.pages[pageIdx]?.name || "" : "";
    // Only the network leg (fetch, HTTP status, reading the body, timeout)
    // counts as a failed poll; a parse or render bug below is logged but
    // must not back off or claim the connection is down.
    let body;
    try {
      const res = await fetch(`api/v1/dashboard?v=${encodeURIComponent(version)}${page ? `&page=${encodeURIComponent(page)}` : ""}`, {
        cache: "no-store",
        headers: apiHeaders,
        signal: timeoutSignal(POLL_TIMEOUT_MS),
      });
      authFail = res.status === 401;
      if (!res.ok) throw new Error(res.status);
      body = await res.text();
    } catch {
      failures++;
      return;
    }
    failures = 0;
    badData = true; // until this answer is parsed and drawn
    const r = JSON.parse(body);
    ui.skew = Date.parse(r.now) - Date.now();
    if (!page) needAll = false;
    if (r.layout && r.layout.version !== version) {
      build(r.layout);
      if (page) needAll = true; // the new layout's other pages have nothing yet
    }
    if (++polls % FULL_EVERY === 0) needAll = true;
    apply(r);
    badData = false;
    lastOk = Date.now() + ui.skew; // the time of the data now on screen
    if (document.body.classList.contains("booting")) {
      document.fonts.ready.then(() => requestAnimationFrame(() => document.body.classList.remove("booting")));
    }
  } catch (e) {
    console.error("dashboard render", e);
  } finally {
    inflight = false;
    try { renderStatus(); } catch (e) { console.error(e); }
    if (again) { again = false; poll(); }
    else pollTimer = setTimeout(poll, failures ? Math.min(30000, POLL_MS * 2 ** failures) : POLL_MS);
  }
}

ui.poll = poll;

// Custom Receiver mode (?receiver=1): register with the Cast framework so the
// session never idles out and the Hub forwards touch input to the page.
if (qs.has("receiver")) {
  const sdk = document.createElement("script");
  sdk.src = "https://www.gstatic.com/cast/sdk/libs/caf_receiver/v3/cast_receiver_framework.js";
  sdk.onload = () => {
    const opts = new cast.framework.CastReceiverOptions();
    opts.disableIdleTimeout = true;
    opts.touchScreenOptimizedApp = true;
    opts.skipPlayersLoad = true;
    cast.framework.CastReceiverContext.getInstance().start(opts);
  };
  document.head.append(sdk);
}

// ?preview=themes: every theme side by side, to choose display.theme.
// Static: makes no API calls.
function renderPreview() {
  document.title = "Themes";
  document.body.classList.remove("booting");
  document.body.classList.add("preview");
  const grid = h("div", "pv-grid");
  for (const t of THEMES) {
    const tile = h("section", "pv");
    tile.dataset.theme = t;
    const head = h("div", "pv-head");
    head.append(h("b", "", t), h("span", "pv-faint", t === "petrol" ? "默认" : ""));
    const nums = h("div", "pv-nums");
    nums.append(h("span", "pv-big", "21°"), h("span", "pv-sub", "主文字 / 次要 / 弱化"));
    const lv = h("div", "pv-levels");
    lv.append(h("span", "pv-bone", "Aa 主"), h("span", "pv-lichen", "Aa 次"), h("span", "pv-faint", "Aa 弱"));
    const st = h("div", "pv-states");
    for (const [c, label] of [["seaglass", "正常"], ["sodium", "注意"], ["flare", "异常"], ["sky", "信息"]]) {
      const s = h("span", "pv-state", label);
      s.style.color = `var(--${c})`;
      st.append(s);
    }
    const meter = h("div", "pv-meter");
    const fill = h("i");
    fill.style.width = "62%";
    meter.append(fill);
    const bars = h("div", "pv-bars");
    for (const [v, c] of [[40, "seaglass"], [70, "sodium"], [90, "flare"], [55, "sky"], [30, "plum"]]) {
      const b = h("i");
      b.style.height = `${v}%`;
      b.style.background = `var(--${c})`;
      bars.append(b);
    }
    const btn = h("button", "btn pv-btn");
    btn.type = "button";
    btn.append(h("span", "bt", "按钮"));
    tile.append(head, nums, lv, st, meter, bars, btn);
    grid.append(tile);
  }
  stage.replaceChildren(grid);
}

if (qs.get("preview") === "themes") renderPreview();
else poll();

// A long-lived page on a device nobody refreshes: reload daily at 04:00
// (dashboard time zone) to shed any slow leaks and pick up new versions of
// this page. Reloading while the server or LAN is down would strand the
// device on the browser's error page with no script left to recover (and the
// LAN keeper sees its app still running, so it never recasts): the reload
// waits, retrying every minute, until the server answers its health check.
const RELOAD_AT = 4 * 60; // minute of the day
let reloadDue = false, lastCheck = null;
setInterval(async () => {
  const d = now();
  const m = minuteOfDay(d), day = dayKey(d);
  if (lastCheck && m >= RELOAD_AT && (lastCheck.m < RELOAD_AT || lastCheck.day !== day)) reloadDue = true;
  lastCheck = { m, day };
  // Not under someone's finger, and not with a sheet open.
  if (!reloadDue || Date.now() - ui.lastTouch < 60000 || !sheet.hidden) return;
  try {
    const res = await fetch("healthz", { cache: "no-store", signal: timeoutSignal(5000) });
    if (res.ok) location.reload();
  } catch { /* still down: try again next minute */ }
}, 60000);
