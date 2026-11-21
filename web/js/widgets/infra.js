/* Servers, probes, AI quotas and subscription plans. */
import { POLL_MS, bytes, clamp01, h, money, now, s, setTap, span, syncList, tickers, tween } from "../core.js";
import { actionButton, canAct, sheetRow } from "../actions.js";

export const VISIBLE = 40; // server trace intervals visible across a row
const W = {};

/* servers: a live trace per machine that never stops moving */
export function serverRow(i, name) {
  let r = i.rows.get(name);
  if (r) return r;
  const li = h("li", "srv");
  const who = h("div", "who");
  const nm = h("div", "name", name), meta = h("div", "meta");
  who.append(nm, meta);
  const trace = h("div", "trace");
  const rail = h("div", "rail");
  rail.style.width = ((VISIBLE + 1) / VISIBLE) * 100 + "%";
  const svg = s("svg", { viewBox: `0 0 ${VISIBLE + 1} 100`, preserveAspectRatio: "none" });
  const area = s("path", { class: "area" }), net = s("path", { class: "net" }), line = s("path", { class: "line" });
  svg.append(area, net, line);
  rail.append(svg);
  // Full-height strip with the dot at its top: translateY(%) of the strip is a
  // fraction of the trace's height, so the dot moves on the compositor.
  const head = h("i", "head");
  trace.append(h("div", "grid"), rail, head);
  const cpu = h("div", "cpu");
  const cpuN = h("span", "num", "–");
  const cpuSub = h("small");
  cpu.append(cpuN, h("span", "unit", "%"), cpuSub);
  const stats = h("div", "stats");
  const mk = (label) => {
    const row = h("div", "stat"), m = h("div", "meter"), bar = h("i"), v = h("span", "v");
    m.append(bar);
    row.append(h("span", null, label), m, v);
    stats.append(row);
    return { m, bar, v };
  };
  const mem = mk("内存"), disk = mk("磁盘");
  const netRow = h("div", "stat net"), netV = h("span", "v");
  netRow.append(h("span", null, "网络"), netV);
  stats.append(netRow);
  li.append(who, trace, cpu, stats);
  sheetRow(li, () => {
    const sv = li._sv;
    return sv && sv.actions && { title: sv.name, sub: sv.up ? `CPU ${Math.round(sv.cpu * 100)}%` : "离线", actions: sv.actions, param: "" };
  });
  r = { li, nm, meta, rail, area, net, line, head, cpuN, cpuSub, mem, disk, netV, buf: null, nbuf: null, last: 0, dur: 5000 };
  i.rows.set(name, r);
  return r;
}
export function setMeter(m, v) {
  m.bar.style.transform = `scaleX(${clamp01(v).toFixed(3)})`;
  m.m.className = "meter" + (v > 0.92 ? " crit" : v > 0.8 ? " warn" : "");
  m.v.textContent = Math.round(v * 100) + "%";
}
export function drawTrace(r) {
  const n = VISIBLE + 2;
  const b = r.buf.slice(-n), nb = r.nbuf.slice(-n);
  const off = n - b.length; // right-align short histories
  const y = (v) => (100 - clamp01(v) * 92 - 4).toFixed(2);
  const pts = b.map((v, k) => `${k ? "L" : "M"}${k + off},${y(v)}`).join("");
  r.line.setAttribute("d", pts);
  r.area.setAttribute("d", b.length > 1 ? `${pts}L${n - 1},100L${off},100Z` : "");
  const nmax = Math.max(1, ...nb);
  r.net.setAttribute("d", nb.map((v, k) => `${k ? "L" : "M"}${k + n - nb.length},${y((v / nmax) * 0.9)}`).join(""));
  // Slide left by one step over the expected interval so the newest point
  // reaches the right edge just as the next one arrives: transform-only work,
  // restarted through the Web Animations API (no forced style/layout flush).
  if (r.anim) r.anim.cancel();
  if (r.rail.animate) {
    r.anim = r.rail.animate(
      [{ transform: "translateX(0)" }, { transform: `translateX(${(-100 / (VISIBLE + 1)).toFixed(4)}%)` }],
      { duration: r.dur, easing: "linear", fill: "forwards" },
    );
  }
  r.head.style.transitionDuration = `${r.dur}ms`;
  r.head.style.transform = `translateY(${y(b[b.length - 1] ?? 0)}%)`;
}
W.servers = {
  init(i) {
    i.ol = h("ol", "servers");
    i.body.append(i.ol);
    i.rows = new Map();
  },
  update(i, d) {
    const list = d.servers || [];
    const ol = i.ol;
    const seen = new Set();
    list.forEach((sv, k) => {
      const r = serverRow(i, sv.name);
      seen.add(sv.name);
      r.li._sv = sv;
      setTap(r.li, !!sv.actions && canAct());
      if (ol.children[k] !== r.li) ol.insertBefore(r.li, ol.children[k] || null);
      const updated = Date.parse(sv.updated) || 0;
      r.li.classList.toggle("down", !sv.up);
      r.li.classList.toggle("hot", sv.up && sv.cpu > 0.85);
      if (!r.buf) { r.buf = (sv.cpu_hist || []).slice(); r.nbuf = (sv.net_hist || []).slice(); }
      let fresh = false;
      if (!sv.up) {
        r.buf.push(0); r.nbuf.push(0); fresh = true; r.dur = POLL_MS; // flatline keeps scrolling
      } else if (updated && r.last && updated - r.last > 30000 && (sv.cpu_hist || []).length) {
        // Back after a gap (this page was not being fetched, or the link was
        // down): take the server's history instead of one long slow step.
        r.buf = sv.cpu_hist.slice(); r.nbuf = (sv.net_hist || []).slice(); fresh = true; r.dur = POLL_MS;
      } else if (updated && updated !== r.last) {
        if (r.last) r.dur = Math.max(800, Math.min(15000, updated - r.last));
        r.buf.push(sv.cpu); r.nbuf.push(sv.net_rx + sv.net_tx); fresh = true;
      }
      if (updated) r.last = updated;
      if (r.buf.length > 80) { r.buf = r.buf.slice(-60); r.nbuf = r.nbuf.slice(-60); }
      if (fresh) drawTrace(r);
      if (!sv.up) {
        r.meta.replaceChildren(h("span", "down-msg", sv.error ? `离线：${sv.error}` : "离线"));
        r.cpuSub.textContent = "";
        return;
      }
      r.meta.textContent = `${sv.cores ? sv.cores + " 核，" : ""}运行 ${span(sv.uptime)}`;
      tween(r.cpuN, sv.cpu * 100, (v) => Math.round(v));
      r.cpuSub.textContent = sv.temp != null ? `${Math.round(sv.temp)}°C，负载 ${sv.load1.toFixed(2)}` : `负载 ${sv.load1.toFixed(2)}`;
      setMeter(r.mem, sv.mem);
      setMeter(r.disk, sv.disk);
      r.netV.textContent = `↓${bytes(sv.net_rx)}/s  ↑${bytes(sv.net_tx)}/s`;
    });
    for (const [name, r] of i.rows) if (!seen.has(name)) { r.li.remove(); i.rows.delete(name); }
    if (!list.length && !ol.querySelector(".empty")) ol.append(h("li", "empty", "还没有配置服务器：在 config.json 的 servers 里加上 node_exporter 地址"));
    if (list.length) ol.querySelector(".empty")?.remove();
  },
};

W.probes = {
  init(i) {
    i.ol = h("ol", "probes");
    i.body.append(i.ol);
  },
  update(i, d) {
    syncList(i.ol, d.probes || [], (p) => p.name, (p) => {
      const li = h("li", "probe");
      li.append(h("i", "dot"), h("span", "n", p.name), h("span", "ms"));
      sheetRow(li, () => {
        const x = li._p;
        return x && x.actions && { title: x.name, sub: x.error || x.url, actions: x.actions, param: "" };
      });
      return li;
    }, (li, p) => {
      li._p = p;
      li.className = "probe " + (!p.checked ? "pending" : !p.up ? "down" : p.latency_ms > 800 ? "slow" : "");
      setTap(li, !!p.actions && canAct());
      let ms;
      if (!p.checked) ms = "…";
      else if (!p.up) ms = `断开 ${span((now() - new Date(p.since)) / 1000)}`;
      else ms = Math.round(p.latency_ms) + " ms";
      li.lastChild.textContent = ms;
      li.title = p.error || p.url;
    });
  },
};

W.quotas = {
  init(i) {
    i.ol = h("ol", "quotas");
    i.body.append(i.ol);
  },
  update(i, d) {
    syncList(i.ol, d.quotas || [], (q) => q.name, (q) => {
      const li = h("li", "quota");
      const n = h("div", "n"), v = h("div", "v"), svg = s("svg", { viewBox: "0 0 100 30", preserveAspectRatio: "none" });
      svg.append(s("path"));
      v.append(h("span", "u"), h("span"));
      li.append(n, v, svg);
      sheetRow(li, () => {
        const x = li._q;
        return x && x.actions && { title: x.name, sub: `余额 ${x.unit || ""}${money(x.value)}`, actions: x.actions, param: "" };
      });
      return li;
    }, (li, q) => {
      li._q = q;
      const [n, v, svg] = li.children;
      const used = q.mode === "used";
      let sub = "";
      if (q.error) sub = q.error;
      else if (q.spent_24h != null && q.spent_24h > 0) {
        sub = `日耗 ${q.unit}${money(q.spent_24h)}`;
        if (!used) sub += `，约够 ${span((q.value / q.spent_24h) * 86400)}`;
      } else if (q.limit) sub = used ? `上限 ${q.unit}${money(q.limit)}` : `共 ${q.unit}${money(q.limit)}`;
      else sub = used ? "已用" : "余额";
      n.replaceChildren(q.name, h("small", null, sub));
      const low = !used && ((q.limit && q.value / q.limit < 0.15) || (q.spent_24h > 0 && q.value / q.spent_24h < 3));
      const acts = q.actions && canAct();
      li.className = "quota" + (q.error && !q.updated ? " err" : low ? " low" : "");
      setTap(li, !!acts);
      v.firstChild.textContent = q.unit || "";
      tween(v.lastChild, q.value, money, 900);
      // When it runs low the fix sits right on the row.
      const chip = li.querySelector(".btn");
      if (low && acts && !chip) li.append(actionButton(q.actions[0], "", null, "chip").el);
      else if (!low && chip && !chip.classList.contains("busy")) chip.remove();
      li.classList.toggle("has-chip", !!li.querySelector(".btn"));
      const hs = q.hist || [];
      if (hs.length > 1) {
        const lo = Math.min(...hs), hi = Math.max(...hs), rng = hi - lo || 1;
        svg.firstChild.setAttribute("d", hs.map((x, j) => `${j ? "L" : "M"}${(j / (hs.length - 1)) * 100},${27 - ((x - lo) / rng) * 24}`).join(""));
      }
    });
  },
};

// Subscription windows (Claude Pro/Max, ChatGPT/Codex) with their saved resets.
// One block per account: a header, then a meter per window. The reset key sits
// on the header the moment a reset can actually be spent.
export const planLevel = (u) => (u >= 100 ? "crit" : u >= 90 ? "hot" : u >= 70 ? "warn" : "ok");
export function resetsText(r) {
  if (!r || !r.available) return "";
  let t = `${r.available} 次重置`;
  if (r.next_expiry) {
    const sec = (new Date(r.next_expiry) - now()) / 1000;
    if (sec > 0) t += `，${span(sec)}后过期`;
  }
  return t;
}
W.plans = {
  init(i) {
    i.ol = h("ol", "plans");
    i.body.append(i.ol);
    tickers.add(() => {
      for (const t of i.ol.querySelectorAll("[data-at]")) {
        const sec = (new Date(t.dataset.at) - now()) / 1000;
        t.textContent = sec > 0 ? `${span(sec)}后恢复` : "即将恢复";
      }
    });
  },
  update(i, d) {
    const reset = canAct() ? d.reset : null;
    syncList(i.ol, d.accounts || [], (a) => a.key, () => {
      const li = h("li", "plan");
      const hd = h("div", "ph"), who = h("div", "who"), rs = h("div", "rs");
      who.append(h("b"), h("span", "tier"), h("small"));
      hd.append(who, rs);
      li.append(hd, h("div", "pmeters"));
      sheetRow(li, () => {
        const a = li._a;
        if (!a || !reset) return null;
        const r = a.resets;
        const sub = a.error || (r && r.available ? (r.usable ? resetsText(r) : `${resetsText(r)}。${r.blocker || ""}`) : r?.blocker || "没有可用的重置次数");
        return { title: `${a.label} ${a.plan || ""}`.trim(), sub: `${a.email || ""}${a.email ? " · " : ""}${sub}`, actions: r && r.available ? [reset] : [], param: a.key };
      });
      return li;
    }, (li, a) => {
      li._a = a;
      const [hd, meters] = li.children;
      const [who, rs] = hd.children;
      who.children[0].textContent = a.label;
      who.children[1].textContent = a.plan || "";
      who.children[1].hidden = !a.plan;
      who.children[2].textContent = a.error ? a.error : a.email || "";
      who.children[2].classList.toggle("bad", !!a.error);
      const r = a.resets;
      const top = Math.max(0, ...(a.windows || []).map((w) => w.used));
      li.dataset.state = a.error && !(a.windows || []).length ? "off" : planLevel(top);
      setTap(li, !!reset);
      // Header right side: the reset key when it can be spent, else how many are saved.
      const chip = rs.querySelector(".btn");
      const canReset = reset && r && r.usable && r.available > 0;
      if (canReset && !chip) rs.replaceChildren(actionButton(reset, a.key, "重置", "chip").el);
      else if (!canReset && !(chip && chip.classList.contains("busy"))) {
        const t = resetsText(r);
        if (rs.textContent !== t || chip) rs.replaceChildren(t ? h("span", "saved", t) : "");
      }
      syncList(meters, a.windows || [], (w) => w.id, () => {
        const m = h("div", "pm");
        const top = h("div", "mt");
        top.append(h("span", "ml"), h("span", "mp num"));
        const bar = h("div", "mbar");
        bar.append(h("i"));
        m.append(top, bar, h("small", "mr"));
        return m;
      }, (m, w) => {
        const [top, bar, mr] = m.children;
        m.dataset.state = planLevel(w.used);
        top.children[0].textContent = w.label;
        tween(top.children[1], w.used, (v) => Math.round(v) + "%", 900);
        bar.firstChild.style.transform = `scaleX(${clamp01(w.used / 100).toFixed(4)})`;
        if (w.resets_at) {
          if (mr.dataset.at !== w.resets_at) {
            mr.dataset.at = w.resets_at;
            const sec = (new Date(w.resets_at) - now()) / 1000;
            mr.textContent = sec > 0 ? `${span(sec)}后恢复` : "即将恢复";
          }
        } else {
          delete mr.dataset.at;
          mr.textContent = "";
        }
      });
    });
    if (!(d.accounts || []).length && !d.waiting) {
      if (!i.ol.querySelector(".empty")) i.ol.append(h("li", "empty", d.error ? "" : "CLIProxyAPI 里没有 Claude 或 Codex 登录"));
    } else i.ol.querySelector(".empty")?.remove();
  },
};

export default W;
