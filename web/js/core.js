/* Shared helpers and state: DOM building, the clock, number tweening,
   formatting and list syncing. */

export const POLL_MS = 2000; // the page asks for fresh data this often
export const qs = new URLSearchParams(location.search);
export const token = qs.get("k");
// The server also sets a cookie from ?k=, but receivers and embedded web views
// do not always keep cookies, so the token rides along as a header too.
export const apiHeaders = token ? { Authorization: `Bearer ${token}` } : {};

export const $ = (id) => document.getElementById(id);
export const h = (tag, cls, text) => {
  const el = document.createElement(tag);
  if (cls) el.className = cls;
  if (text != null) el.textContent = text;
  return el;
};
const SVGNS = "http://www.w3.org/2000/svg";
export const s = (tag, attrs) => {
  const el = document.createElementNS(SVGNS, tag);
  for (const k in attrs || {}) el.setAttribute(k, attrs[k]);
  return el;
};
export const clamp01 = (v) => Math.max(0, Math.min(1, v));
// AbortSignal.timeout where the runtime has it, else the same by hand.
export function timeoutSignal(ms) {
  if (AbortSignal.timeout) return AbortSignal.timeout(ms);
  const c = new AbortController();
  setTimeout(() => c.abort(new DOMException("timed out", "TimeoutError")), ms);
  return c.signal;
}
// Rows that open a sheet act as buttons for keyboards and screen readers too.
export function setTap(el, on) {
  el.classList.toggle("tappable", on);
  if (on) { el.setAttribute("role", "button"); el.tabIndex = 0; }
  else { el.removeAttribute("role"); el.removeAttribute("tabindex"); }
}

// ui is the state the modules share; app.js owns and updates it.
export const ui = {
  tz: undefined, // IANA zone from the server; device zone as fallback
  skew: 0, // server clock minus device clock, ms
  tweenOff: false, // set while updating widgets nobody can see
  layout: null, // the current layout from the server
  pageIdx: 0, // the page on screen
  offline: false, // the screen says the server is unreachable
  actions: {}, // action states (running, last result) from the last poll
  lastTouch: Date.now(), // last touch, key or click, for idle return
  poll: () => {}, // asks the server for fresh data now
};
export const now = () => new Date(Date.now() + ui.skew);

/* ───────── number tweening (one shared rAF loop) ───────── */
const tweens = new Map();
let rafOn = false;
export function tween(el, to, fmt, ms = 700) {
  if (to == null || !isFinite(to)) { tweens.delete(el); el._v = undefined; el.textContent = "–"; return; }
  const from = el._v ?? to;
  el._v = to;
  if (from === to || !isFinite(from) || ui.tweenOff || !el.isConnected) { tweens.delete(el); el.textContent = fmt(to); return; }
  tweens.set(el, { from, to, fmt, t0: performance.now(), ms });
  if (!rafOn) { rafOn = true; requestAnimationFrame(step); }
}
function step(t) {
  for (const [el, tw] of tweens) {
    const p = Math.min(1, (t - tw.t0) / tw.ms);
    const e = 1 - Math.pow(1 - p, 3);
    el.textContent = tw.fmt(tw.from + (tw.to - tw.from) * e);
    if (p >= 1) tweens.delete(el);
  }
  if (tweens.size) requestAnimationFrame(step); else rafOn = false;
}

/* ───────── formatting ───────── */
// Building an Intl formatter is far dearer than using one, and the clock,
// tint and agenda ask for the same few every second.
const fmtCache = new Map();
export function fmtPart(d, opts) {
  const k = (ui.tz || "") + "|" + JSON.stringify(opts);
  let f = fmtCache.get(k);
  if (!f) { f = new Intl.DateTimeFormat("zh-CN", { timeZone: ui.tz, ...opts }); fmtCache.set(k, f); }
  return f.format(d);
}
let compactFmt = null;
export const hm = (d) => fmtPart(d, { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
export const dayKey = (d) => fmtPart(d, { year: "numeric", month: "2-digit", day: "2-digit" });
// zh-CN renders a lone hour as "11时" (and a lone minute may carry "分"), so
// "+fmtPart(d, {hour})" was NaN: read both off the plain "HH:MM" instead.
export const minuteOfDay = (d) => {
  const [hh, mm] = hm(d).split(":");
  return (+hh % 24) * 60 + +mm;
};
export const hourOf = (d) => Math.floor(minuteOfDay(d) / 60);
export function bytes(n) {
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1000 && i < u.length - 1) { n /= 1024; i++; }
  return (n >= 100 || i === 0 ? n.toFixed(0) : n.toFixed(1)) + " " + u[i];
}
export function span(sec) {
  sec = Math.max(0, sec);
  if (sec < 60) return `${Math.round(sec)} 秒`;
  if (sec < 3600) return `${Math.floor(sec / 60)} 分钟`;
  if (sec < 86400) {
    const hh = Math.floor(sec / 3600), mm = Math.floor((sec % 3600) / 60);
    return mm ? `${hh} 小时 ${mm} 分` : `${hh} 小时`;
  }
  return Math.floor(sec / 86400) + " 天";
}
export const money = (v) => (Math.abs(v) >= 100 ? v.toFixed(0) : v.toFixed(2));
const PREFIX_UNIT = /^(?:[¥$€£₩₹]|[A-Z]{2,3}\$)$/;

export function fmtNum(v, w = {}) {
  if (v == null || !isFinite(v)) return "–";
  if (w.format === "bytes") return bytes(v);
  if (w.format === "duration") return span(v);
  if (w.format === "compact") return (compactFmt ||= new Intl.NumberFormat("zh-CN", { notation: "compact", maximumFractionDigits: 1 })).format(v);
  let d = w.decimals;
  if (w.format === "int") d = 0;
  else if (w.format === "money") d = Math.abs(v) >= 1000 ? 0 : 2;
  if (d == null) {
    const a = Math.abs(v);
    d = a >= 100 || a === 0 ? 0 : a >= 10 ? 1 : a >= 1 ? 2 : 3;
  }
  return v.toLocaleString("en-US", { minimumFractionDigits: d, maximumFractionDigits: d });
}
// A number with its unit: currency symbols in front, everything else after.
export function valueEl(w) {
  const el = h("span", "val num");
  const pre = h("span", "u"), n = h("span", "n"), post = h("span", "u");
  const unit = w.unit || "";
  if (PREFIX_UNIT.test(unit)) pre.textContent = unit; else post.textContent = unit;
  el.append(pre, n, post);
  return { el, set: (v) => tween(n, v, (x) => fmtNum(x, w), 800) };
}
const COLORS = { sodium: "var(--sodium)", seaglass: "var(--seaglass)", flare: "var(--flare)", sky: "var(--sky)", bone: "var(--bone)", lichen: "var(--lichen)", plum: "var(--plum)" };
const SERIES = ["seaglass", "sodium", "sky", "plum", "bone"];
export const color = (c, i = 0) => COLORS[c] || c || COLORS[SERIES[i % SERIES.length]];
export function windowText(sec) {
  if (!sec) return "";
  if (sec % 86400 === 0) return `${sec / 86400} 天`;
  if (sec % 3600 === 0) return `${sec / 3600} 小时`;
  return `${Math.round(sec / 60)} 分钟`;
}
export function ago(t) {
  const sec = (now() - new Date(t)) / 1000;
  return sec < 60 ? "刚刚" : `${span(sec)}前`;
}

/* ───────── shared list syncing: rows keep their DOM so they can animate ───────── */
export function syncList(parent, items, key, make, update) {
  const old = new Map();
  for (const el of [...parent.children]) if (el._key != null && !el.classList.contains("leave")) old.set(el._key, el);
  let prev = null;
  const count = new Map(); // two rows with the same label still get a row each
  items.forEach((it, i) => {
    let k = key(it, i);
    const n = count.get(k) || 0;
    count.set(k, n + 1);
    if (n) k = `${k}\u0000${n}`;
    let el = old.get(k);
    if (el) old.delete(k);
    else {
      el = make(it);
      el._key = k;
      if (parent._seen) el.classList.add("enter");
    }
    const want = prev ? prev.nextSibling : parent.firstChild;
    if (want !== el) parent.insertBefore(el, want);
    update(el, it, i);
    prev = el;
  });
  for (const el of old.values()) {
    el.classList.add("leave");
    setTimeout(() => el.remove(), 450);
  }
  parent._seen = true;
}

export const tickers = new Set(); // called once a second while the layout lives
