/* Clock, weather and agenda. */
import { dayKey, fmtPart, h, hm, now, s, span, tickers, tween, ui } from "../core.js";

const W = {};

W.clock = {
  init(i) {
    i.clock = h("div", "clock");
    i.date = h("div", "date");
    i.body.append(i.clock, i.date);
    i.last = "";
    const tick = () => {
      const d = now();
      const t = hm(d);
      if (t === i.last) return;
      if (!i.clock.children.length) {
        for (const ch of t) {
          const c = h("span", ch === ":" ? "colon" : "digit");
          c.append(h("span", null, ch));
          i.clock.append(c);
        }
      } else {
        [...t].forEach((ch, k) => {
          const c = i.clock.children[k];
          if (c.classList.contains("colon") || c.lastChild.textContent === ch) return;
          const old = h("span", "old", c.lastChild.textContent);
          c.replaceChildren(old, h("span", null, ch));
          c.classList.remove("roll");
          void c.offsetWidth;
          c.classList.add("roll");
          setTimeout(() => old.remove(), 800);
        });
      }
      i.last = t;
      i.date.textContent = fmtPart(d, { month: "long", day: "numeric", weekday: "long" });
    };
    tick();
    tickers.add(tick);
  },
  update() {},
};

export const WMO = [
  [[0], "晴", "sun"], [[1], "大致晴朗", "sun"], [[2], "多云间晴", "sunny-cloud"], [[3], "阴", "cloud"],
  [[45, 48], "雾", "cloud"], [[51, 53, 55, 56, 57], "毛毛雨", "rain"], [[61, 63, 65, 66, 67, 80, 81, 82], "雨", "rain"],
  [[71, 73, 75, 77, 85, 86], "雪", "snow"], [[95, 96, 99], "雷雨", "storm"],
];
export function wmo(code) {
  for (const [codes, text, kind] of WMO) if (codes.includes(code)) return { text, kind };
  return { text: "—", kind: "cloud" };
}
export function wxGlyph(el, kind, isDay) {
  el.replaceChildren();
  el.className = el.classList.contains("mini") ? "wx-icon mini" : "wx-icon";
  const add = (cls, style) => { const x = h("i", cls); if (style) x.style.cssText = style; el.append(x); };
  if (kind === "sun") { add(isDay ? "sun" : "moon"); return; }
  if (kind === "sunny-cloud") { el.classList.add("sunny-cloud"); add(isDay ? "sun" : "moon"); add("cloud"); return; }
  add("cloud");
  if (kind === "rain" || kind === "storm") for (let k = 0; k < 3; k++) add("drop", `left:${26 + k * 18}%;animation-delay:${k * 0.37}s`);
  if (kind === "snow") for (let k = 0; k < 3; k++) add("flake", `left:${24 + k * 20}%;animation-delay:${k * 0.8}s`);
  if (kind === "storm") add("bolt");
}
export const weekdayUTC = new Intl.DateTimeFormat("zh-CN", { weekday: "short", timeZone: "UTC" });
W.weather = {
  init(i) {
    const nowRow = h("div", "wx-now");
    i.icon = h("div", "wx-icon");
    i.temp = h("div", "wx-temp num");
    const desc = h("div", "wx-desc");
    i.text = h("span");
    i.meta = h("span", "dim");
    desc.append(i.text, i.meta);
    nowRow.append(i.icon, i.temp, desc);
    i.hours = s("svg", { class: "wx-hours", viewBox: "0 0 400 68", preserveAspectRatio: "none" });
    i.days = h("ol", "wx-days");
    i.box = h("div", "weather");
    i.box.append(nowRow, i.hours, i.days);
    i.body.append(i.box);
    i.key = "";
  },
  update(i, w) {
    if (w.waiting || w.code == null) { i.text.textContent = "等待天气数据"; return; }
    const info = wmo(w.code);
    tween(i.temp, w.temp, (v) => Math.round(v) + "°");
    i.text.textContent = `${w.name ? w.name + "，" : ""}${info.text}`;
    i.meta.textContent = w.error && !w.updated ? w.error : `体感 ${Math.round(w.feels)}°，湿度 ${Math.round(w.humidity)}%，风 ${Math.round(w.wind)} km/h`;
    const key = JSON.stringify([w.code, w.is_day, w.daily, w.hourly]);
    if (key === i.key) return; // glyphs keep animating; don't restart them
    i.key = key;
    wxGlyph(i.icon, info.kind, w.is_day);
    const svg = i.hours;
    svg.replaceChildren();
    const hrs = w.hourly || [];
    if (hrs.length > 1) {
      const ts = hrs.map((x) => x.temp), lo = Math.min(...ts), hi = Math.max(...ts), rng = Math.max(1, hi - lo);
      const X = (k) => (k / (hrs.length - 1)) * 400, Y = (t) => 30 - ((t - lo) / rng) * 26;
      hrs.forEach((x, k) => { if (x.pop > 5) svg.append(s("rect", { class: "p", x: X(k) - 6, y: 50 - x.pop * 0.14, width: 12, height: x.pop * 0.14, rx: 2 })); });
      svg.append(s("path", { class: "t", d: hrs.map((x, k) => `${k ? "L" : "M"}${X(k).toFixed(1)},${Y(x.temp).toFixed(1)}`).join("") }));
      for (const k of [0, 6, 12, 18]) if (hrs[k]) {
        const lab = s("text", { x: X(k), y: 64 });
        lab.textContent = hrs[k].time.slice(11, 13) + "时";
        svg.append(lab);
      }
    }
    i.days.replaceChildren();
    (w.daily || []).slice(1, 4).forEach((d) => {
      const li = h("li");
      const g = h("div", "wx-icon mini");
      wxGlyph(g, wmo(d.code).kind, true);
      const wd = weekdayUTC.format(new Date(d.date + "T12:00:00Z"));
      li.append(g, h("span", null, wd), h("span", "hi num", Math.round(d.max) + "°"), h("span", "num", Math.round(d.min) + "°"));
      i.days.append(li);
    });
  },
};

W.agenda = {
  init(i) {
    i.box = h("div", "agenda");
    i.next = h("div", "next");
    i.list = h("ol", "events");
    i.err = h("p", "note");
    i.err.hidden = true;
    i.box.append(i.next, i.list, i.err);
    i.body.append(i.box);
    i.events = [];
    i.evKey = "[]";
    i.drawn = "";
    tickers.add(() => this.draw(i)); // cheap: redraws only when the minute turns
  },
  update(i, d) {
    i.events = d.events || [];
    i.evKey = JSON.stringify(i.events);
    i.err.hidden = !d.error;
    i.err.textContent = d.error ? `日程同步失败：${d.error}` : "";
    this.draw(i);
  },
  draw(i) {
    const t = now();
    // Everything drawn depends only on the events and the current minute.
    const key = `${Math.floor(t / 60000)}|${ui.tz || ""}|${i.evKey}`;
    if (key === i.drawn) return;
    i.drawn = key;
    const today = dayKey(t);
    const tomorrow = dayKey(new Date(t.getTime() + 86400000));
    const list = i.events.map((e) => ({ ...e, s: new Date(e.start), e: new Date(e.end) }))
      .sort((a, b) => a.s - b.s || (b.all_day - a.all_day));
    // Next: an event in progress, else the next one to start (timed events only).
    const timed = list.filter((e) => !e.all_day && e.e > t);
    const cur = timed.find((e) => e.s <= t);
    const nxt = cur || timed[0];
    const box = i.next;
    box.replaceChildren();
    box.className = "next";
    if (nxt) {
      box.style.setProperty("--cal", nxt.color || "");
      const secs = (nxt.s - t) / 1000;
      const when = h("div", "when");
      if (cur) {
        box.classList.add("now");
        when.append(`进行中，还剩 ${span((nxt.e - t) / 1000)}`);
      } else {
        if (secs < 15 * 60) box.classList.add("imminent");
        const big = h("span", "num");
        if (secs < 3600) { big.textContent = Math.max(1, Math.ceil(secs / 60)); when.append(big, " 分钟后"); }
        else if (dayKey(nxt.s) === today) { big.textContent = hm(nxt.s); when.append("今天 ", big); }
        else { big.textContent = hm(nxt.s); when.append(dayKey(nxt.s) === tomorrow ? "明天 " : fmtPart(nxt.s, { weekday: "short" }) + " ", big); }
      }
      box.append(when, h("div", "what", nxt.title));
      if (nxt.location) box.append(h("div", "where", nxt.location));
    }
    const ol = i.list;
    ol.replaceChildren();
    let lastDay = "", rows = 0;
    for (const e of list) {
      if (e === nxt) continue;
      const k = dayKey(e.s < t && e.all_day ? t : e.s);
      if (k !== lastDay) {
        if (rows >= 9) break;
        lastDay = k;
        const label = k === today ? "今天" : k === tomorrow ? "明天" : fmtPart(e.s, { month: "numeric", day: "numeric", weekday: "short" });
        ol.append(h("li", "dayhead", label));
      }
      const li = h("li", "ev" + (e.all_day ? " allday" : "") + (!e.all_day && e.e <= t ? " past" : ""));
      li.style.setProperty("--cal", e.color || "");
      li.append(h("span", "t", e.all_day ? "全天" : hm(e.s)), h("span", "bar"), h("span", "x", e.title));
      ol.append(li);
      rows++;
    }
    if (!list.length) ol.append(h("li", "empty", "接下来一周没有安排"));
  },
};

export default W;
