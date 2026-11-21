/* Action buttons, the action sheet and toasts. */
import { $, ago, apiHeaders, h, setTap, timeoutSignal, ui } from "./core.js";

const ACTION_TIMEOUT_MS = 60000; // actions run synchronously on the server
const HOLD_MS = 900; // press-and-hold confirmation

export function itemTap(inst, li) {
  const acts = inst.w.item_actions;
  if (!acts || !acts.length || !canAct()) return;
  setTap(li, true);
  sheetRow(li, () => {
    const it = li._it;
    return it && it.param && { title: it.label, sub: inst.w.title, actions: acts, param: it.param };
  });
}

// sheetRow opens the action sheet when a list row is tapped (Enter/Space too,
// via the global key handler); sheet returns its options, or a falsy value
// when the row has nothing to offer. A row that opens its sheet keeps the
// click (the key handler's synthetic one too) from reaching the widget,
// whose own sheet (config `actions`) would otherwise replace the row's.
export function sheetRow(li, sheet) {
  li.addEventListener("click", (e) => {
    const o = sheet();
    if (o) {
      e.stopPropagation();
      openSheet(o);
    }
  });
}

/* ───────── actions ───────── */
function lastText(key) {
  const r = ui.actions[key];
  if (!r) return "";
  if (r.running) return "执行中…";
  return `${ago(r.at)}${r.ok ? "" : "失败"}：${r.message || (r.ok ? "完成" : "")}`;
}
// actionButton returns a button that asks the server to run an action, with the
// confirmation the action wants: hold (press 0.9s), tap (tap twice) or none.
export function actionButton(name, param, label, variant = "") {
  const meta = (ui.layout && ui.layout.actions && ui.layout.actions[name]) || { title: name, confirm: "hold" };
  const confirm = meta.confirm || "hold";
  const el = h("button", `btn ${variant} c-${confirm}`);
  el.type = "button";
  const title = label || meta.title || name;
  const txt = h("span", "bt", title), sub = h("small", "bs");
  el.append(h("i", "fill"), txt);
  if (variant === "big" || variant === "wide") {
    el.append(sub, h("b", "bh", confirm === "hold" ? "按住" : confirm === "tap" ? "点两下" : ""));
  }
  const key = param ? `${name}:${param}` : name;
  let timer = null, armed = null, fired = false, hint = null;
  const say = (t, ms) => {
    clearTimeout(hint);
    txt.textContent = t;
    if (ms) hint = setTimeout(() => { txt.textContent = title; }, ms);
  };
  const fire = async () => {
    fired = true;
    el.classList.remove("holding", "armed");
    el.classList.add("busy");
    say(title);
    let r = { ok: false };
    try { r = await runAction(name, param); } finally {
      el.classList.remove("busy");
      el.classList.add(r.ok ? "done" : "failed");
      setTimeout(() => el.classList.remove("done", "failed"), 1800);
    }
  };
  const cancel = () => {
    if (timer) { clearTimeout(timer); timer = null; }
    el.classList.remove("holding");
  };
  const press = () => {
    ui.lastTouch = Date.now();
    fired = false;
    if (confirm !== "hold" || el.classList.contains("busy")) return;
    el.classList.add("holding");
    timer = setTimeout(() => { timer = null; fire(); }, HOLD_MS);
  };
  el.addEventListener("pointerdown", (e) => { e.stopPropagation(); press(); });
  for (const ev of ["pointerup", "pointercancel", "pointerleave", "blur"]) el.addEventListener(ev, cancel);
  el.addEventListener("contextmenu", (e) => e.preventDefault()); // long-press menu
  // Keyboard: holding Enter or Space is the same press-and-hold; a short press
  // is the same as a tap. The native key→click is suppressed so it counts once.
  el.addEventListener("keydown", (e) => {
    if (e.key !== "Enter" && e.key !== " ") return;
    e.preventDefault();
    e.stopPropagation();
    if (e.repeat) return;
    press();
  });
  el.addEventListener("keyup", (e) => {
    if (e.key !== "Enter" && e.key !== " ") return;
    e.preventDefault();
    e.stopPropagation();
    cancel();
    activate();
  });
  el.addEventListener("click", (e) => { e.stopPropagation(); activate(); });
  function activate() {
    if (el.classList.contains("busy") || fired) return;
    if (confirm === "hold") { say("按住确认", 1600); return; }
    if (confirm === "tap" && !el.classList.contains("armed")) {
      el.classList.add("armed");
      say("再点一次确认");
      armed = setTimeout(() => { el.classList.remove("armed"); say(title); }, 3000);
      return;
    }
    clearTimeout(armed);
    fire();
  }
  el._refresh = () => {
    sub.textContent = lastText(key);
    const r = ui.actions[key];
    if (r && r.running) el.classList.add("busy");
  };
  el._refresh();
  return { el };
}

async function runAction(name, param) {
  try {
    const signal = timeoutSignal(ACTION_TIMEOUT_MS);
    const res = await fetch(`api/v1/actions/${encodeURIComponent(name)}`, {
      method: "POST",
      headers: { ...apiHeaders, "Content-Type": "application/json" },
      body: JSON.stringify({ param }),
      signal,
    });
    const j = await res.json().catch(() => ({}));
    if (!res.ok) { toast(j.error || `失败（HTTP ${res.status}）`, "fail"); return { ok: false }; }
    toast(j.message || (j.ok ? "完成" : "失败"), j.ok ? "ok" : "fail");
    ui.poll();
    return { ok: !!j.ok };
  } catch (e) {
    toast(e && e.name === "TimeoutError" ? "操作一分钟内没有返回，请留意执行结果" : "连不上 dashboard 服务", "fail");
    ui.poll(); // the action may still finish server-side; show its state when it does
    return { ok: false };
  }
}

function toast(msg, kind = "") {
  const t = h("div", `toast ${kind}`, msg);
  $("toasts").append(t);
  requestAnimationFrame(() => requestAnimationFrame(() => t.classList.add("in")));
  setTimeout(() => { t.classList.remove("in"); setTimeout(() => t.remove(), 450); }, 3600);
}

/* ───────── detail sheet ───────── */
export const sheet = $("sheet");
// A view-only token (layout.can_act false) gets no action UI at all: the
// server would answer 403. Before the layout arrives nothing is drawn anyway.
export const canAct = () => !ui.layout || ui.layout.can_act !== false;
export function openSheet({ title, sub, actions, param }) {
  if (!canAct()) return;
  ui.lastTouch = Date.now();
  sheet._back = document.activeElement;
  sheet.querySelector("h3").textContent = title || "";
  sheet.querySelector(".sub").textContent = sub || "";
  sheet.querySelector(".acts").replaceChildren(...actions.map((a) => actionButton(a, param, null, "wide").el));
  cancelHide();
  sheet.hidden = false;
  requestAnimationFrame(() => requestAnimationFrame(() => {
    sheet.classList.add("open");
    (sheet.querySelector(".acts .btn") || sheet.querySelector(".close")).focus({ preventScroll: true });
  }));
}
export function closeSheet() {
  if (sheet.hidden) return;
  sheet.classList.remove("open");
  const back = sheet._back;
  sheet._back = null;
  if (back && back.isConnected && sheet.contains(document.activeElement)) back.focus({ preventScroll: true });
  // Hide once the panel has slid out: on its transitionend, or after the
  // CSS duration if that never fires (reduced motion, tab in background).
  const panel = sheet.querySelector(".panel");
  const ms = (parseFloat(getComputedStyle(panel).transitionDuration) || 0) * 1000;
  panel.addEventListener("transitionend", onPanelEnd);
  hideTimer = setTimeout(hideClosed, ms + 100);
}
let hideTimer = 0;
function onPanelEnd(e) {
  if (e.target === e.currentTarget && e.propertyName === "transform") hideClosed();
}
function cancelHide() {
  clearTimeout(hideTimer);
  sheet.querySelector(".panel").removeEventListener("transitionend", onPanelEnd);
}
function hideClosed() {
  cancelHide();
  if (!sheet.classList.contains("open")) sheet.hidden = true;
}
sheet.querySelector(".scrim").addEventListener("click", closeSheet);
sheet.querySelector(".close").addEventListener("click", closeSheet);
