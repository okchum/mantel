/* Widgets for generic sources: stat, gauge, charts, lists, text, images, buttons. */
import { clamp01, color, fmtNum, fmtPart, h, now, s, syncList, tickers, token, tween, ui, valueEl, windowText } from "../core.js";
import { actionButton, canAct, itemTap } from "../actions.js";

const W = {};

export function sparkline(line, area, arr, W = 100, H = 30) {
  if (!arr || arr.length < 2) { line.setAttribute("d", ""); area.setAttribute("d", ""); return; }
  const lo = Math.min(...arr), hi = Math.max(...arr), rng = hi - lo || 1;
  const pts = arr.map((v, k) => `${k ? "L" : "M"}${((k / (arr.length - 1)) * W).toFixed(2)},${(H - 2 - ((v - lo) / rng) * (H - 6)).toFixed(2)}`).join("");
  line.setAttribute("d", pts);
  area.setAttribute("d", `${pts}L${W},${H}L0,${H}Z`);
}

W.stat = {
  init(i) {
    i.val = valueEl(i.w);
    const big = h("div", "big");
    big.append(i.val.el);
    i.sub = h("div", "sub");
    i.spark = s("svg", { class: "spark", viewBox: "0 0 100 30", preserveAspectRatio: "none" });
    i.sa = s("path", { class: "a" });
    i.sl = s("path", { class: "l" });
    i.spark.append(i.sa, i.sl);
    i.body.append(big, i.sub);
    i.el.append(i.spark);
  },
  update(i, d) {
    i.el.dataset.state = d.state || "";
    i.val.set(d.value);
    if (d.delta != null) {
      const flat = Math.abs(d.delta) < 1e-9;
      i.sub.textContent = flat ? `${windowText(i.w.window)}内持平` : `${d.delta > 0 ? "▲" : "▼"} ${fmtNum(Math.abs(d.delta), i.w)}，${windowText(i.w.window)}内`;
    } else i.sub.textContent = "";
    sparkline(i.sl, i.sa, d.spark);
  },
};

// 220° arc, open at the bottom
export const ARC = "M12.41 63.68 A40 40 0 1 1 87.59 63.68";
W.gauge = {
  init(i) {
    const svg = s("svg", { class: "arc", viewBox: "0 0 100 76" });
    svg.append(s("path", { class: "track", d: ARC, pathLength: 100 }));
    i.fill = s("path", { class: "fill", d: ARC, pathLength: 100 });
    svg.append(i.fill);
    i.svg = svg;
    i.val = valueEl(i.w);
    const c = h("div", "center");
    i.cap = h("div", "cap");
    c.append(i.val.el, i.cap);
    i.body.append(svg, c);
  },
  update(i, d) {
    const max = d.max || 100;
    i.fill.style.strokeDashoffset = (100 - clamp01((d.value ?? 0) / max) * 100).toFixed(2);
    i.el.dataset.state = d.state || "";
    i.val.set(d.value);
    i.cap.textContent = i.w.unit === "%" && max === 100 ? "" : `上限 ${fmtNum(max, i.w)}`;
    if (!i.marked) {
      i.marked = true;
      for (const [line, cls] of [[i.w.warn, "warn"], [i.w.crit, "crit"]]) {
        if (line == null) continue;
        const a = ((200 - 220 * clamp01(line / max)) * Math.PI) / 180;
        const p = (r) => [50 + r * Math.cos(a), 50 - r * Math.sin(a)];
        const [x1, y1] = p(46), [x2, y2] = p(49.5);
        i.svg.append(s("line", { class: "mark " + cls, x1, y1, x2, y2 }));
      }
    }
  },
};

W.timeseries = {
  init(i) {
    i.legend = h("div", "legend");
    i.chart = h("div", "chart");
    i.svg = s("svg", { viewBox: "0 0 400 100", preserveAspectRatio: "none" });
    i.hi = h("span", "yl hi num");
    i.lo = h("span", "yl lo num");
    i.empty = h("span", "chart-empty", "正在收集数据");
    i.chart.append(i.svg, i.hi, i.lo, i.empty);
    i.body.append(i.legend, i.chart);
    i.lines = [];
  },
  update(i, d) {
    const ser = d.series || [];
    let tmax = -Infinity, vmin = Infinity, vmax = -Infinity;
    for (const x of ser) for (const [t] of x.points || []) tmax = Math.max(tmax, t);
    const tmin = tmax - (d.window || 3600) * 1000;
    for (const x of ser) for (const [t, v] of x.points || []) if (t >= tmin) { vmin = Math.min(vmin, v); vmax = Math.max(vmax, v); }
    const has = ser.some((x) => (x.points || []).filter(([t]) => t >= tmin).length > 1);
    // A series whose points path failed says so on its legend entry; it is
    // not "collecting" and never will be until the config or source changes.
    i.empty.hidden = has || (ser.length > 0 && ser.every((x) => x.error));
    const rng = vmax - vmin || Math.abs(vmax) || 1;
    const lo = vmin - rng * 0.12, hi = vmax + rng * 0.12;
    const X = (t) => ((t - tmin) / (tmax - tmin || 1)) * 400;
    const Y = (v) => 100 - ((v - lo) / (hi - lo)) * 100;
    i.hi.textContent = has ? fmtNum(vmax, i.w) + (i.w.unit || "") : "";
    i.lo.textContent = has ? fmtNum(vmin, i.w) + (i.w.unit || "") : "";
    ser.forEach((x, k) => {
      let L = i.lines[k];
      if (!L) {
        const g = s("g");
        const area = s("path", { class: "area" }), line = s("path", { class: "line" });
        g.append(area, line);
        i.svg.append(g);
        const dot = h("i", "hd");
        i.chart.append(dot);
        const lg = h("span", "lg");
        const val = h("span", "num");
        const err = h("span", "note");
        lg.append(h("i"), h("span", null, x.label), val, err);
        i.legend.append(lg);
        L = i.lines[k] = { g, area, line, dot, lg, val, err, drawn: false };
      }
      L.lg.classList.toggle("err", !!x.error);
      L.err.textContent = x.error || "";
      L.err.title = x.error || "";
      const c = color(x.color, k);
      L.g.style.color = c;
      L.dot.style.setProperty("--c", c);
      L.lg.style.setProperty("--c", c);
      const pts = (x.points || []).filter(([t]) => t >= tmin);
      if (pts.length < 2) { L.line.setAttribute("d", ""); L.area.setAttribute("d", ""); L.dot.hidden = true; L.val.textContent = ""; return; }
      const path = pts.map(([t, v], j) => `${j ? "L" : "M"}${X(t).toFixed(1)},${Y(v).toFixed(2)}`).join("");
      L.line.setAttribute("d", path);
      L.area.setAttribute("d", k === 0 ? `${path}L${X(pts[pts.length - 1][0]).toFixed(1)},100L${X(pts[0][0]).toFixed(1)},100Z` : "");
      if (!L.drawn) { L.drawn = true; i.svg.classList.add("draw"); }
      const [tl, vl] = pts[pts.length - 1];
      L.dot.hidden = false;
      // The dot sits at the top-left of a chart-sized box; translating the box
      // by percentages of itself moves it without layout.
      L.dot.style.transform = `translate(${(X(tl) / 4).toFixed(2)}%, ${Y(vl).toFixed(2)}%)`;
      tween(L.val, x.last ?? vl, (v) => fmtNum(v, i.w) + (i.w.unit || ""));
    });
  },
};

W.bars = {
  init(i) {
    i.wrap = h("div", "bars");
    i.body.append(i.wrap);
  },
  update(i, d) {
    const items = d.items || [];
    const max = Math.max(1e-9, ...items.map((x) => x.num ?? 0));
    syncList(i.wrap, items, (x, k) => x.label + "#" + k, () => {
      const col = h("div", "col");
      const bt = h("div", "bt");
      bt.append(h("i"));
      col.append(h("span", "bv num"), bt, h("span", "bl"));
      return col;
    }, (col, x, k) => {
      const [bv, bt, bl] = col.children;
      tween(bv, x.num, (v) => fmtNum(v, i.w), 900);
      bt.firstChild.style.transform = `scaleY(${clamp01((x.num ?? 0) / max).toFixed(3)})`;
      bl.textContent = x.label;
      col.classList.toggle("last", k === items.length - 1);
      col.dataset.state = x.state || "";
    });
  },
};

W.status = {
  init(i) {
    i.ol = h("ol", "cells");
    i.body.append(i.ol);
  },
  update(i, d) {
    // Problems first, so a long list that does not fit still shows them.
    const rank = { crit: 0, warn: 1 };
    const items = (d.items || []).map((x, k) => [x, k]).sort((a, b) => (rank[a[0].state] ?? 2) - (rank[b[0].state] ?? 2) || a[1] - b[1]).map((p) => p[0]);
    const bad = items.filter((x) => x.state === "crit").length;
    if (i.note && !i.note.textContent && items.length) i.note.textContent = bad ? `${bad} 个异常，共 ${items.length}` : `共 ${items.length}`;
    syncList(i.ol, items, (x) => x.label, () => {
      const li = h("li", "cell");
      li.append(h("i", "dot"), h("span", "lb"), h("span", "dt"));
      itemTap(i, li);
      return li;
    }, (li, x) => {
      li._it = x;
      li.dataset.state = x.state || "";
      li.children[1].textContent = x.label;
      li.children[2].textContent = x.detail || (x.value != null && typeof x.value !== "boolean" ? String(x.value) : "");
    });
    // syncList keeps order of existing rows; re-append to follow the sort.
    const live = [...i.ol.children].filter((e) => !e.classList.contains("leave"));
    if (live.some((e, k) => e._it !== items[k])) for (const x of items) { const li = live.find((e) => e._it === x); if (li) i.ol.append(li); }
    i.ol.classList.toggle("over", i.ol.scrollHeight > i.ol.clientHeight + 2);
  },
};

W.list = {
  init(i) {
    i.ol = h("ol", "rows");
    i.body.append(i.ol);
  },
  update(i, d) {
    const items = d.items || [];
    syncList(i.ol, items, (x) => x.label, () => {
      const li = h("li", "row");
      const lb = h("div", "lb");
      lb.append(h("span", "x"), h("small"));
      li.append(h("i", "st"), lb, h("span", "badge"), h("span", "rv num"));
      itemTap(i, li);
      return li;
    }, (li, x) => {
      li._it = x;
      li.dataset.state = x.state || "";
      const [, lb, badge, rv] = li.children;
      lb.firstChild.textContent = x.label;
      lb.lastChild.textContent = x.detail || "";
      badge.textContent = x.badge || "";
      badge.hidden = !x.badge;
      if (x.num != null) tween(rv, x.num, (v) => fmtNum(v, i.w) + (i.w.unit || ""));
      else rv.textContent = x.value == null || typeof x.value === "object" ? "" : String(x.value);
    });
    i.ol.classList.toggle("is-empty", !items.length);
    if (!items.length && !d.error && !d.waiting) i.ol.dataset.empty = "全部完成";
  },
};

W.progress = {
  init(i) {
    i.ol = h("ol", "prog");
    i.body.append(i.ol);
  },
  update(i, d) {
    syncList(i.ol, d.items || [], (x) => x.label, () => {
      const li = h("li", "prow");
      const top = h("div", "top");
      top.append(h("span", "x"), h("span", "pd"), h("span", "pc num"));
      const bar = h("div", "pbar");
      bar.append(h("i"));
      li.append(top, bar);
      itemTap(i, li);
      return li;
    }, (li, x) => {
      li._it = x;
      let f = x.num ?? 0;
      if (f > 1) f /= 100;
      f = clamp01(f);
      const st = x.state || (f >= 1 ? "ok" : "run");
      li.dataset.state = st;
      const [top, bar] = li.children;
      top.children[0].textContent = x.label;
      top.children[1].textContent = x.detail || "";
      tween(top.children[2], f * 100, (v) => Math.floor(v) + "%", 900);
      bar.firstChild.style.transform = `scaleX(${f.toFixed(4)})`;
    });
  },
};

W.text = {
  init(i) {
    i.p = h("div", "text size-" + (i.w.size || "m"));
    i.body.append(i.p);
  },
  update(i, d) {
    const t = d.text ?? "";
    if (t === i.p.textContent) return;
    i.p.classList.remove("swap");
    void i.p.offsetWidth;
    i.p.classList.add("swap");
    i.p.textContent = t;
  },
};

W.countdown = {
  init(i) {
    i.big = h("div", "cd-big num");
    i.n = h("span", "n");
    i.u = h("span", "u");
    i.big.append(i.n, i.u);
    i.rest = h("div", "cd-rest num");
    i.when = h("div", "cd-when");
    i.body.append(i.big, i.rest, i.when);
    tickers.add(() => this.tick(i));
  },
  update(i, d) {
    i.to = d.to ? new Date(d.to) : null;
    i.allDay = !!d.all_day;
    i.when.textContent = d.error || (i.to ? fmtPart(i.to, i.allDay ? { month: "long", day: "numeric", weekday: "short" } : { month: "long", day: "numeric", weekday: "short", hour: "2-digit", minute: "2-digit", hourCycle: "h23" }) : "");
    this.tick(i);
  },
  tick(i) {
    if (!i.to) return;
    let sec = (i.to - now()) / 1000;
    const past = sec < 0;
    i.el.classList.toggle("past", past);
    sec = Math.abs(sec);
    const dd = Math.floor(sec / 86400), hh = Math.floor((sec % 86400) / 3600), mm = Math.floor((sec % 3600) / 60), ss = Math.floor(sec % 60);
    const p2 = (x) => String(x).padStart(2, "0");
    if (dd >= 1) {
      i.n.textContent = dd;
      i.u.textContent = past ? " 天前" : " 天";
      i.rest.textContent = `${p2(hh)}:${p2(mm)}:${p2(ss)}`;
    } else {
      i.n.textContent = `${p2(hh)}:${p2(mm)}`;
      i.u.textContent = past ? " 前" : "";
      i.rest.textContent = `${p2(ss)} 秒`;
    }
  },
};

W.image = {
  init(i) {
    i.a = h("img");
    i.b = h("img");
    i.a.alt = i.b.alt = i.w.title || "";
    i.body.append(i.a, i.b);
    i.n = 0;
    i.t = 0;
    const every = i.w.refresh || 60;
    i.load = () => {
      const next = i.a.classList.contains("on") ? i.b : i.a;
      const cur = next === i.a ? i.b : i.a;
      next.onload = () => { next.classList.add("on"); cur.classList.remove("on"); i.el.classList.remove("errored"); i.bad = false; if (i.note) i.note.textContent = ""; };
      next.onerror = () => { i.el.classList.add("errored"); i.bad = true; if (i.note) i.note.textContent = "图片加载失败"; };
      next.src = `api/v1/image/${encodeURIComponent(i.w.id)}?n=${++i.n}${token ? `&k=${encodeURIComponent(token)}` : ""}`;
    };
    i.load();
    // Fetch only while someone can see it: on the current page, tab visible,
    // server reachable. The count keeps running, so an overdue image
    // reloads on the first tick after it is shown again.
    tickers.add(() => {
      if (++i.t < every || i.page !== ui.pageIdx || document.hidden || ui.offline) return;
      i.t = 0;
      i.load();
    });
  },
  update(i) {
    if (i.bad && i.note) i.note.textContent = "图片加载失败";
    i.el.classList.toggle("errored", !!i.bad);
  },
};

W.buttons = {
  init(i) {
    i.grid = h("div", "btns");
    if (!canAct()) {
      // A view-only token: the server would refuse (403), so offer nothing.
      i.el.classList.add("locked");
      i.grid.append(h("p", "empty", "这个访问令牌只能查看，不能执行操作"));
    } else for (const a of i.w.actions || []) i.grid.append(actionButton(a, "", null, "big").el);
    i.body.append(i.grid);
  },
  update() {},
};

W.unknown = {
  init(i) { i.body.append(h("p", "note", `不认识的组件类型 ${i.w.type}`)); },
  update() {},
};

export default W;
