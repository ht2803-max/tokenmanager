/* TokenManager dashboard. Every number was computed by TokenManager in Go;
   this script only filters, sums and draws the embedded aggregates. It makes
   no network requests, and all text is inserted with textContent. */
(function () {
  'use strict';

  var D = JSON.parse(document.getElementById('tm-data').textContent);
  var M = D.meta;
  var SVGNS = 'http://www.w3.org/2000/svg';

  // Fact columns (see web.Fact).
  var F = { bucket: 0, agent: 1, project: 2, model: 3, req: 4, input: 5, output: 6, cr: 7, cw: 8, cost: 9, unpriced: 10, est: 11 };
  // Token categories, in the stacking order the palette was validated for.
  var CATS = [
    { key: 'input', label: 'Input', color: 'var(--s1)' },
    { key: 'output', label: 'Output', color: 'var(--s2)' },
    { key: 'cr', label: 'Cache read', color: 'var(--s3)' },
    { key: 'cw', label: 'Cache write', color: 'var(--s4)' }
  ];
  var AGENT_ORDER = ['claude-code', 'codex', 'gemini-cli', 'aider', 'continue', 'cursor'];

  var state = {
    range: '30d', agent: -1, project: -1, model: -1, tab: 'overview',
    sessQuery: '', sessShown: 50, open: {},
    sort: {
      sessions: { key: 'end', dir: -1 },
      projects: { key: 'cost', dir: -1 },
      models: { key: 'cost', dir: -1 },
      tools: { key: 'cost', dir: -1 }
    }
  };

  // ------------------------------------------------------------ helpers

  function el(tag, attrs) {
    var n = document.createElement(tag);
    setAttrs(n, attrs);
    for (var i = 2; i < arguments.length; i++) append(n, arguments[i]);
    return n;
  }
  function svg(tag, attrs) {
    var n = document.createElementNS(SVGNS, tag);
    setAttrs(n, attrs);
    for (var i = 2; i < arguments.length; i++) append(n, arguments[i]);
    return n;
  }
  function setAttrs(n, attrs) {
    if (!attrs) return;
    Object.keys(attrs).forEach(function (k) {
      var v = attrs[k];
      if (v === null || v === undefined || v === false) return;
      if (k === 'class') n.setAttribute('class', v);
      else if (k === 'style' && typeof v === 'object') Object.keys(v).forEach(function (p) { n.style.setProperty(p, v[p]); });
      else if (k.slice(0, 2) === 'on') n.addEventListener(k.slice(2), v);
      else n.setAttribute(k, v === true ? '' : String(v));
    });
  }
  function append(n, c) {
    if (c === null || c === undefined || c === false) return;
    if (Array.isArray(c)) { c.forEach(function (x) { append(n, x); }); return; }
    n.appendChild(typeof c === 'object' ? c : document.createTextNode(String(c)));
  }
  function clear(n) { while (n.firstChild) n.removeChild(n.firstChild); return n; }

  // Formatting mirrors internal/format so the page reads like the TUI.
  function thousands(n) { return Math.round(n).toString().replace(/\B(?=(\d{3})+(?!\d))/g, ','); }
  function money(v) {
    if (v < 0) return '-' + money(-v);
    if (v >= 10000) return M.symbol + thousands(v);
    return M.symbol + v.toFixed(2);
  }
  function costText(cost, unpriced) {
    if (unpriced > 0 && cost === 0) return 'n/a';
    return money(cost) + (unpriced > 0 ? '*' : '');
  }
  function tokens(n) {
    if (n >= 1e9) return (n / 1e9).toFixed(2) + 'B';
    if (n >= 1e7) return (n / 1e6).toFixed(1) + 'M';
    if (n >= 1e6) return (n / 1e6).toFixed(2) + 'M';
    if (n >= 1e4) return (n / 1e3).toFixed(0) + 'k';
    if (n >= 1e3) return (n / 1e3).toFixed(1) + 'k';
    return String(Math.round(n));
  }
  function pct(f, digits) { return (f * 100).toFixed(digits === undefined ? 1 : digits) + '%'; }
  function duration(ms) {
    var s = Math.max(0, Math.round(ms / 1000));
    if (s < 60) return s + 's';
    var m = Math.floor(s / 60);
    if (m < 60) return m + 'm';
    var h = Math.floor(m / 60);
    if (h < 24) return h + 'h' + String(m % 60).padStart(2, '0') + 'm';
    return Math.floor(h / 24) + 'd' + String(h % 24).padStart(2, '0') + 'h';
  }
  var MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  function dayLabel(day) { var p = day.split('-'); return MONTHS[+p[1] - 1] + ' ' + (+p[2]); }
  function dayLabelLong(day) {
    var d = new Date(Date.UTC(+day.slice(0, 4), +day.slice(5, 7) - 1, +day.slice(8, 10)));
    return ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'][d.getUTCDay()] + ', ' + dayLabel(day) + ' ' + day.slice(0, 4);
  }
  function stamp(ms) {
    var d = new Date(ms);
    return MONTHS[d.getMonth()] + ' ' + d.getDate() + ', ' + String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0');
  }
  function addDays(day, n) {
    var d = new Date(Date.UTC(+day.slice(0, 4), +day.slice(5, 7) - 1, +day.slice(8, 10)) + n * 864e5);
    return d.toISOString().slice(0, 10);
  }
  function shortID(id) { return id.length > 8 ? id.slice(0, 8) : id; }

  var agentInfo = {};
  D.agents.forEach(function (a) { agentInfo[a.name] = a; });
  function agentName(id) { return agentInfo[id] ? agentInfo[id].display : id; }
  function agentColor(id) {
    var i = AGENT_ORDER.indexOf(id);
    if (i < 0) i = AGENT_ORDER.length + (D.dims.agents.indexOf(id) % 2);
    return 'var(--s' + (i + 1) + ')';
  }
  var modelInfo = {};
  D.models.forEach(function (m) { modelInfo[m.name] = m; });

  // ------------------------------------------------------------ filtering

  function rangeStart(range) {
    return { today: M.today, '7d': M.last7_start, '30d': M.last30_start }[range] || '';
  }
  function rangeLabel() {
    return { today: 'Today', '7d': 'Last 7 days', '30d': 'Last 30 days', all: 'All time' }[state.range];
  }
  function dimsMatch(agent, project, models) {
    if (state.agent >= 0 && agent !== state.agent) return false;
    if (state.project >= 0 && project !== state.project) return false;
    if (state.model >= 0) {
      if (Array.isArray(models)) { if (models.indexOf(state.model) < 0) return false; }
      else if (models !== state.model) return false;
    }
    return true;
  }
  function hasDimFilter() { return state.agent >= 0 || state.project >= 0 || state.model >= 0; }

  function zero() { return { req: 0, input: 0, output: 0, cr: 0, cw: 0, cost: 0, unpriced: 0, est: 0 }; }
  function addFact(t, f) {
    t.req += f[F.req]; t.input += f[F.input]; t.output += f[F.output]; t.cr += f[F.cr]; t.cw += f[F.cw];
    t.cost += f[F.cost]; t.unpriced += f[F.unpriced]; t.est += f[F.est];
  }
  function tokTotal(t) { return t.input + t.output + t.cr + t.cw; }

  // Facts passing the dimension filters and starting on or after `from`.
  function facts(from) {
    var days = D.dims.days;
    return D.facts.filter(function (f) {
      return (!from || days[f[F.bucket]] >= from) && dimsMatch(f[F.agent], f[F.project], f[F.model]);
    });
  }
  function selected() { return facts(rangeStart(state.range)); }
  function sumBy(list, col) {
    var m = new Map();
    list.forEach(function (f) {
      var k = f[col], t = m.get(k);
      if (!t) { t = zero(); m.set(k, t); }
      addFact(t, f);
    });
    return m;
  }
  function total(list) { var t = zero(); list.forEach(function (f) { addFact(t, f); }); return t; }
  function selectedSessions() {
    var from = rangeStart(state.range);
    return D.sessions.filter(function (s) {
      return (!from || s.day >= from) && dimsMatch(s.agent, s.project, s.models);
    });
  }
  // Time buckets for the selected range: hours for today, otherwise days.
  function series() {
    if (state.range === 'today') {
      var hours = [];
      for (var h = 0; h < 24; h++) hours.push({ label: String(h).padStart(2, '0') + ':00', long: 'Today ' + String(h).padStart(2, '0') + ':00', t: zero() });
      D.hourly.forEach(function (f) {
        if (dimsMatch(f[F.agent], f[F.project], f[F.model])) addFact(hours[f[F.bucket]].t, f);
      });
      return hours;
    }
    var from = rangeStart(state.range);
    if (!from) {
      from = M.today;
      D.dims.days.forEach(function (d) { if (d < from) from = d; });
    }
    var pts = [], idx = {};
    for (var day = from; day <= M.today && pts.length < 5000; day = addDays(day, 1)) {
      idx[day] = pts.length;
      pts.push({ label: dayLabel(day), long: dayLabelLong(day), t: zero() });
    }
    facts(from).forEach(function (f) {
      var i = idx[D.dims.days[f[F.bucket]]];
      if (i !== undefined) addFact(pts[i].t, f);
    });
    return pts;
  }

  // ------------------------------------------------------------ tooltip

  var tip = document.getElementById('tooltip');
  function showTip(evt, title, rows, totalRow) {
    clear(tip);
    tip.appendChild(el('div', { class: 'tt-title' }, title));
    rows.forEach(function (r) {
      tip.appendChild(el('div', { class: 'tt-row' },
        el('span', null, r.color ? el('i', { class: 'dot', style: { background: r.color } }) : null, r.label),
        el('b', null, r.value)));
    });
    if (totalRow) tip.appendChild(el('div', { class: 'tt-row tt-total' }, el('span', null, totalRow.label), el('b', null, totalRow.value)));
    tip.hidden = false;
    moveTip(evt);
  }
  function moveTip(evt) {
    var w = tip.offsetWidth, h = tip.offsetHeight, pad = 14;
    var x = evt.clientX + pad, y = evt.clientY + pad;
    if (x + w > window.innerWidth - 8) x = evt.clientX - w - pad;
    if (y + h > window.innerHeight - 8) y = evt.clientY - h - pad;
    tip.style.left = Math.max(8, x) + 'px';
    tip.style.top = Math.max(8, y) + 'px';
  }
  function hideTip() { tip.hidden = true; }

  // ------------------------------------------------------------ charts

  function niceMax(v) {
    if (v <= 0) return 1;
    var p = Math.pow(10, Math.floor(Math.log10(v))), n = v / p;
    return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 5 ? 5 : 10) * p;
  }
  function chartFrame(container, height) {
    var w = Math.max(container.clientWidth, 280);
    return { w: w, h: height, l: 52, r: 8, t: 8, b: 26 };
  }
  function axes(g, fr, max, fmt) {
    var ticks = 4;
    for (var i = 0; i <= ticks; i++) {
      var v = max * i / ticks, y = fr.t + (fr.h - fr.t - fr.b) * (1 - i / ticks);
      g.appendChild(svg('line', { class: i === 0 ? 'baseline' : 'gridline', x1: fr.l, x2: fr.w - fr.r, y1: y, y2: y }));
      g.appendChild(svg('text', { class: 'tick', x: fr.l - 8, y: y + 4, 'text-anchor': 'end' }, fmt(v)));
    }
  }
  function xLabels(g, fr, pts, xOf) {
    var n = pts.length, every = Math.max(1, Math.ceil(n / Math.max(2, Math.floor((fr.w - fr.l) / 76))));
    for (var i = 0; i < n; i += every) {
      g.appendChild(svg('text', { class: 'tick', x: xOf(i), y: fr.h - 6, 'text-anchor': 'middle' }, pts[i].label));
    }
  }
  function emptyChart(container, msg) {
    clear(container).appendChild(el('div', { class: 'empty' }, msg || 'No usage in this range.'));
  }
  // Path for a bar with rounded top corners, anchored to the baseline.
  function topRounded(x, y, w, h, r) {
    r = Math.min(r, w / 2, h);
    return 'M' + x + ',' + (y + h) + 'V' + (y + r) + 'Q' + x + ',' + y + ' ' + (x + r) + ',' + y +
      'H' + (x + w - r) + 'Q' + (x + w) + ',' + y + ' ' + (x + w) + ',' + (y + r) + 'V' + (y + h) + 'Z';
  }

  function stackedBars(container, pts) {
    var max = 0;
    pts.forEach(function (p) { max = Math.max(max, tokTotal(p.t)); });
    if (max === 0) return emptyChart(container);
    var fr = chartFrame(container, 260);
    max = niceMax(max);
    var s = svg('svg', { viewBox: '0 0 ' + fr.w + ' ' + fr.h, height: fr.h, role: 'img', 'aria-label': 'Token usage over time, stacked by input, output, cache read and cache write' });
    var g = svg('g');
    s.appendChild(g);
    axes(g, fr, max, tokens);
    var plotW = fr.w - fr.l - fr.r, plotH = fr.h - fr.t - fr.b, slot = plotW / pts.length;
    var bw = Math.max(1, Math.min(36, slot * (pts.length > 60 ? 0.8 : 0.66)));
    var xOf = function (i) { return fr.l + slot * i + slot / 2; };
    pts.forEach(function (p, i) {
      var col = svg('g', { class: 'col' });
      col.appendChild(svg('rect', { class: 'wash', x: fr.l + slot * i, y: fr.t, width: slot, height: plotH }));
      var y = fr.t + plotH, x = xOf(i) - bw / 2, segs = [];
      CATS.forEach(function (c) { if (p.t[c.key] > 0) segs.push(c); });
      segs.forEach(function (c, j) {
        var h = p.t[c.key] / max * plotH;
        var gap = j < segs.length - 1 && h > 3 ? 2 : 0;
        y -= h;
        var attrs = { class: 'seg-bar', style: { fill: c.color, 'animation-delay': Math.min(i * 6, 300) + 'ms' } };
        if (j === segs.length - 1) { attrs.d = topRounded(x, y, bw, h, 4); col.appendChild(svg('path', attrs)); }
        else { attrs.x = x; attrs.y = y + gap; attrs.width = bw; attrs.height = Math.max(0, h - gap); col.appendChild(svg('rect', attrs)); }
      });
      var hit = svg('rect', { class: 'hit', x: fr.l + slot * i, y: fr.t, width: slot, height: plotH });
      var show = function (e) {
        showTip(e, p.long, CATS.map(function (c) { return { label: c.label, value: tokens(p.t[c.key]), color: c.color }; }),
          { label: 'Total · ' + costText(p.t.cost, p.t.unpriced), value: tokens(tokTotal(p.t)) });
      };
      hit.addEventListener('mouseenter', show);
      hit.addEventListener('mousemove', moveTip);
      hit.addEventListener('mouseleave', hideTip);
      col.appendChild(hit);
      g.appendChild(col);
    });
    xLabels(g, fr, pts, xOf);
    clear(container).appendChild(s);
  }

  function areaChart(container, pts, value, fmt, color, label) {
    var max = 0;
    pts.forEach(function (p) { max = Math.max(max, value(p)); });
    if (max === 0) return emptyChart(container, 'No priced usage in this range.');
    var fr = chartFrame(container, 260);
    max = niceMax(max);
    var plotW = fr.w - fr.l - fr.r, plotH = fr.h - fr.t - fr.b;
    var n = pts.length;
    var xOf = function (i) { return n === 1 ? fr.l + plotW / 2 : fr.l + plotW * i / (n - 1); };
    var yOf = function (v) { return fr.t + plotH * (1 - v / max); };
    var s = svg('svg', { viewBox: '0 0 ' + fr.w + ' ' + fr.h, height: fr.h, role: 'img', 'aria-label': label });
    var g = svg('g');
    s.appendChild(g);
    axes(g, fr, max, fmt);
    var line = '', area = '';
    pts.forEach(function (p, i) {
      var x = xOf(i), y = yOf(value(p));
      line += (i ? 'L' : 'M') + x.toFixed(1) + ',' + y.toFixed(1);
    });
    area = line + 'L' + xOf(n - 1).toFixed(1) + ',' + yOf(0) + 'L' + xOf(0).toFixed(1) + ',' + yOf(0) + 'Z';
    g.appendChild(svg('path', { class: 'area', d: area, style: { fill: color } }));
    g.appendChild(svg('path', { class: 'line', d: line, style: { stroke: color } }));
    if (n === 1) g.appendChild(svg('circle', { class: 'marker', cx: xOf(0), cy: yOf(value(pts[0])), r: 4, style: { fill: color } }));
    var cross = svg('line', { class: 'crosshair', y1: fr.t, y2: fr.t + plotH, visibility: 'hidden' });
    var dot = svg('circle', { class: 'marker', r: 5, visibility: 'hidden', style: { fill: color } });
    g.appendChild(cross);
    g.appendChild(dot);
    xLabels(g, fr, pts, xOf);
    var hit = svg('rect', { class: 'hit', x: fr.l, y: fr.t, width: plotW, height: plotH });
    hit.addEventListener('mousemove', function (e) {
      var box = s.getBoundingClientRect(), scale = fr.w / box.width;
      var px = (e.clientX - box.left) * scale;
      var i = n === 1 ? 0 : Math.round((px - fr.l) / plotW * (n - 1));
      i = Math.max(0, Math.min(n - 1, i));
      var x = xOf(i), y = yOf(value(pts[i]));
      cross.setAttribute('x1', x); cross.setAttribute('x2', x); cross.setAttribute('visibility', 'visible');
      dot.setAttribute('cx', x); dot.setAttribute('cy', y); dot.setAttribute('visibility', 'visible');
      var p = pts[i];
      showTip(e, p.long, [
        { label: 'Spend', value: costText(p.t.cost, p.t.unpriced), color: color },
        { label: 'Tokens', value: tokens(tokTotal(p.t)) },
        { label: 'Requests', value: thousands(p.t.req) }
      ]);
    });
    hit.addEventListener('mouseleave', function () {
      cross.setAttribute('visibility', 'hidden'); dot.setAttribute('visibility', 'hidden'); hideTip();
    });
    g.appendChild(hit);
    clear(container).appendChild(s);
  }

  function donut(container, t) {
    var tot = tokTotal(t);
    if (tot === 0) return emptyChart(container);
    var size = 180, r = 78, inner = 54, cx = size / 2, cy = size / 2;
    var s = svg('svg', { viewBox: '0 0 ' + size + ' ' + size, width: size, height: size, role: 'img', 'aria-label': 'Token mix' });
    var a0 = -Math.PI / 2;
    CATS.forEach(function (c) {
      var v = t[c.key];
      if (v <= 0) return;
      var frac = v / tot, a1 = a0 + frac * Math.PI * 2;
      var seg;
      if (frac >= 0.9999) {
        seg = svg('path', { class: 'donut-seg', d: 'M' + cx + ',' + (cy - r) + 'A' + r + ',' + r + ' 0 1 1 ' + (cx - 0.01) + ',' + (cy - r) + 'L' + (cx - 0.01) + ',' + (cy - inner) + 'A' + inner + ',' + inner + ' 0 1 0 ' + cx + ',' + (cy - inner) + 'Z', style: { fill: c.color } });
      } else {
        var large = a1 - a0 > Math.PI ? 1 : 0;
        var p = function (rad, a) { return (cx + rad * Math.cos(a)).toFixed(2) + ',' + (cy + rad * Math.sin(a)).toFixed(2); };
        seg = svg('path', { class: 'donut-seg', d: 'M' + p(r, a0) + 'A' + r + ',' + r + ' 0 ' + large + ' 1 ' + p(r, a1) + 'L' + p(inner, a1) + 'A' + inner + ',' + inner + ' 0 ' + large + ' 0 ' + p(inner, a0) + 'Z', style: { fill: c.color } });
      }
      seg.addEventListener('mouseenter', function (e) { showTip(e, c.label, [{ label: 'Tokens', value: tokens(v), color: c.color }, { label: 'Share', value: pct(frac) }]); });
      seg.addEventListener('mousemove', moveTip);
      seg.addEventListener('mouseleave', hideTip);
      s.appendChild(seg);
      a0 = a1;
    });
    s.appendChild(svg('text', { class: 'center-v', x: cx, y: cy + 4, 'text-anchor': 'middle' }, tokens(tot)));
    s.appendChild(svg('text', { class: 'center-l', x: cx, y: cy + 22, 'text-anchor': 'middle' }, 'tokens'));
    var rows = el('div', { class: 'mix-rows' });
    CATS.forEach(function (c) {
      rows.appendChild(el('div', { class: 'mix-row' },
        el('i', { class: 'dot', style: { background: c.color } }),
        el('span', { class: 'ink2' }, c.label),
        el('span', null, el('span', { class: 'v' }, tokens(t[c.key])), el('span', { class: 'p' }, pct(t[c.key] / tot, 0)))));
    });
    var hit = t.cr + t.input + t.cw > 0 ? t.cr / (t.cr + t.input + t.cw) : 0;
    rows.appendChild(el('div', { class: 'footnote' }, 'Cache hit rate ', el('b', { class: 'ink2' }, pct(hit)), ' of prompt tokens'));
    clear(container).appendChild(el('div', { class: 'mix' }, el('div', { class: 'chart' }, s), rows));
  }

  // hbars draws horizontal bars scaled to the largest item. The small figure
  // is each item's share of the items shown unless absolute is set (the
  // item's text already carries the figure that matters).
  function hbars(container, items, absolute) {
    var max = 0, tot = 0;
    items.forEach(function (it) { max = Math.max(max, it.value); tot += it.value; });
    if (!items.length || max === 0) return emptyChart(container, 'No priced usage in this range.');
    var wrap = el('div', { class: 'hbars' });
    items.forEach(function (it) {
      var fill = el('i', { style: { background: it.color } });
      var row = el('div', { class: 'hbar' },
        el('div', { class: 'hbar-top' },
          el('span', { class: 'name' }, el('i', { class: 'dot', style: { background: it.color } }), it.label),
          el('span', { class: 'v' }, it.text, absolute ? null : el('small', null, pct(it.value / tot, 0)))),
        el('div', { class: 'bar' }, fill));
      row.addEventListener('mouseenter', function (e) { showTip(e, it.label, it.tip); });
      row.addEventListener('mousemove', moveTip);
      row.addEventListener('mouseleave', hideTip);
      wrap.appendChild(row);
      requestAnimationFrame(function () { fill.style.width = (it.value / max * 100) + '%'; });
    });
    clear(container).appendChild(wrap);
  }

  function progress(frac, cls, thin) {
    var fill = el('i');
    var bar = el('div', { class: 'bar ' + (cls || '') + (thin ? ' thin' : ''), role: 'progressbar', 'aria-valuemin': 0, 'aria-valuemax': 100, 'aria-valuenow': Math.round(frac * 100) }, fill);
    requestAnimationFrame(function () { fill.style.width = Math.min(100, Math.max(0, frac * 100)) + '%'; });
    return bar;
  }
  function statusOf(frac) { return frac >= 1 ? 'bad' : frac >= 0.8 ? 'warn' : 'good'; }
  var ICONS = {
    good: 'M3.5 8.5l3 3 6-7',
    warn: 'M8 4v5M8 11.5v.5',
    bad: 'M4.5 4.5l7 7M11.5 4.5l-7 7',
    info: 'M8 7v5M8 4.5v.5'
  };
  function pill(kind, text) {
    var icon = ICONS[kind] ? svg('svg', { viewBox: '0 0 16 16', 'aria-hidden': 'true' },
      svg('path', { d: ICONS[kind], fill: 'none', stroke: 'currentColor', 'stroke-width': 2, 'stroke-linecap': 'round', 'stroke-linejoin': 'round' })) : null;
    return el('span', { class: 'pill ' + kind }, icon, text);
  }

  // ------------------------------------------------------------ tables

  // columns: {key, label, num, sort(row) -> value, cell(row) -> node|string, cls}
  function table(name, columns, rows, opts) {
    opts = opts || {};
    var st = state.sort[name];
    var col = columns.filter(function (c) { return c.key === st.key; })[0] || columns[0];
    rows = rows.slice().sort(function (a, b) {
      var x = col.sort(a), y = col.sort(b);
      if (x < y) return -st.dir;
      if (x > y) return st.dir;
      return 0;
    });
    var thead = el('tr');
    columns.forEach(function (c) {
      var th = el('th', { class: c.num ? 'num' : null, scope: 'col' });
      if (c.sort) {
        if (c.key === st.key) th.setAttribute('aria-sort', st.dir > 0 ? 'ascending' : 'descending');
        th.appendChild(el('button', {
          type: 'button', onclick: function () {
            if (st.key === c.key) st.dir = -st.dir;
            else { st.key = c.key; st.dir = c.num ? -1 : 1; }
            render();
          }
        }, c.label));
      } else th.appendChild(document.createTextNode(c.label));
      thead.appendChild(th);
    });
    var tbody = el('tbody');
    var shown = opts.limit ? rows.slice(0, opts.limit) : rows;
    shown.forEach(function (r) {
      var tr = el('tr', { class: 'row' });
      columns.forEach(function (c) { tr.appendChild(el('td', { class: [c.num ? 'num' : '', c.cls || ''].join(' ').trim() || null }, c.cell(r))); });
      tbody.appendChild(tr);
      if (opts.expand) opts.expand(r, tr, tbody, columns.length);
    });
    if (!rows.length) tbody.appendChild(el('tr', null, el('td', { colspan: columns.length }, el('div', { class: 'empty' }, opts.empty || 'Nothing matches the current filters.'))));
    return { node: el('div', { class: 'table-scroll' }, el('table', null, el('thead', null, thead), tbody)), total: rows.length };
  }
  function shareCell(frac) {
    return el('div', { class: 'share' }, el('span', null, pct(frac)), progress(frac, '', true));
  }

  // ------------------------------------------------------------ sections

  function sectionHead(title, sub, extra) {
    return el('div', { class: 'section-head' }, el('div', null, el('h2', null, title), sub ? el('div', { class: 'sub' }, sub) : null), extra || null);
  }
  function kpi(label, value, meta, extra) {
    return el('div', { class: 'card kpi' }, el('div', { class: 'kpi-label' }, label), el('div', { class: 'kpi-value' }, value),
      meta ? el('div', { class: 'kpi-meta' }, meta) : null, extra || null);
  }
  function chartCard(span, title, sub, legend) {
    var body = el('div', { class: 'chart' });
    var card = el('div', { class: 'card ' + span },
      el('div', { class: 'card-head' }, el('div', null, el('h3', null, title), sub ? el('div', { class: 'card-sub' }, sub) : null), legend || null),
      body);
    return { card: card, body: body };
  }
  function catLegend() {
    return el('div', { class: 'legend' }, CATS.map(function (c) { return el('span', null, el('i', { class: 'dot', style: { background: c.color } }), c.label); }));
  }
  function globalMonthly() {
    return D.budgets.filter(function (b) { return b.scope === 'global' && b.period === 'monthly'; })[0];
  }

  function renderOverview(root) {
    // Calendar KPIs. Unfiltered values come straight from Engine.Overview;
    // with an agent/project/model filter the same totals are re-summed from
    // the Go-priced facts.
    var o = D.overview, today, week, month, projected;
    if (!hasDimFilter()) {
      var conv = function (t) { return { req: t.requests, input: t.input, output: t.output, cr: t.cache_read, cw: t.cache_write, cost: t.cost, unpriced: t.unpriced }; };
      today = conv(o.today); week = conv(o.week); month = conv(o.month); projected = o.projected_month;
    } else {
      today = total(facts(M.today)); week = total(facts(M.week_start)); month = total(facts(M.month_start));
      projected = month.cost * M.projection_factor;
    }
    var budget = globalMonthly();
    var budgetTile;
    if (budget) {
      var st = statusOf(budget.fraction);
      budgetTile = kpi('Monthly budget', money(budget.limit),
        [budget.remaining >= 0 ? el('b', null, money(budget.remaining)) : el('b', { style: { color: 'var(--bad-ink)' } }, money(-budget.remaining) + ' over'),
          budget.remaining >= 0 ? ' remaining · ' : ' · ', pct(budget.fraction, 0) + ' used'],
        progress(budget.fraction, st));
    } else {
      budgetTile = kpi('Monthly budget', 'Not set', el('span', null, 'Set one with ', el('code', null, 'tokenmanager budget set monthly 100')));
    }
    var projMeta = [el('b', null, money(month.cost)), ' so far in ' + M.month_label];
    if (budget && budget.limit > 0) projMeta.push(' · ', el('span', { style: { color: projected > budget.limit ? 'var(--bad-ink)' : 'inherit' } }, pct(projected / budget.limit, 0) + ' of budget'));
    root.appendChild(sectionHead('Overview', hasDimFilter() ? 'Calendar periods · filtered by agent / project / model' : 'Calendar periods · all agents'));
    root.appendChild(el('div', { class: 'grid kpis' },
      kpi("Today's spend", costText(today.cost, today.unpriced), [el('b', null, tokens(tokTotal(today))), ' tokens · ' + thousands(today.req) + ' requests']),
      kpi('This week', costText(week.cost, week.unpriced), [el('b', null, tokens(tokTotal(week))), ' tokens since ' + dayLabel(M.week_start)]),
      kpi('This month', costText(month.cost, month.unpriced), [el('b', null, tokens(tokTotal(month))), ' tokens · ' + M.month_label]),
      kpi('Projected month', money(projected), projMeta),
      budgetTile));
    if (D.limits.length) renderLimits(root);

    // Selected range.
    var sel = selected(), t = total(sel), sess = selectedSessions();
    var prompt = t.input + t.cr + t.cw;
    root.appendChild(sectionHead(rangeLabel(), 'Responds to every filter'));
    var sessCount = thousands(sess.length) + (M.sessions_shown < M.sessions_total && state.range === 'all' ? '+' : '');
    root.appendChild(el('div', { class: 'card strip' },
      el('div', null, el('div', { class: 'kpi-label' }, 'Spend'), el('div', { class: 'kpi-value' }, costText(t.cost, t.unpriced))),
      el('div', null, el('div', { class: 'kpi-label' }, 'Tokens'), el('div', { class: 'kpi-value' }, tokens(tokTotal(t)))),
      el('div', null, el('div', { class: 'kpi-label' }, 'Cached tokens'), el('div', { class: 'kpi-value' }, tokens(t.cr + t.cw)),
        el('div', { class: 'kpi-meta' }, pct(prompt ? t.cr / prompt : 0, 0) + ' cache hit')),
      el('div', null, el('div', { class: 'kpi-label' }, 'Requests'), el('div', { class: 'kpi-value' }, thousands(t.req))),
      el('div', null, el('div', { class: 'kpi-label' }, 'Sessions'), el('div', { class: 'kpi-value' }, sessCount))));

    var pts = series();
    var unit = state.range === 'today' ? 'Hourly' : 'Daily';
    var grid = el('div', { class: 'grid charts', style: { 'margin-top': '14px' } });
    var c1 = chartCard('span-12', unit + ' token usage', 'Stacked by token type', catLegend());
    var c2 = chartCard('span-7', unit + ' spending', 'Estimated at API list prices');
    var c3 = chartCard('span-5', 'Input vs output vs cache', 'Share of all tokens');
    var c4 = chartCard('span-12', 'Spending by agent', rangeLabel());
    [c1, c2, c3, c4].forEach(function (c) { grid.appendChild(c.card); });
    root.appendChild(grid);
    stackedBars(c1.body, pts);
    areaChart(c2.body, pts, function (p) { return p.t.cost; }, function (v) { return money(v); }, 'var(--s7)', 'Spending over time');
    donut(c3.body, t);
    var byAgent = sumBy(sel, F.agent), items = [];
    byAgent.forEach(function (v, k) {
      var id = D.dims.agents[k];
      items.push({ label: agentName(id), value: v.cost, text: costText(v.cost, v.unpriced), color: agentColor(id),
        tip: [{ label: 'Spend', value: costText(v.cost, v.unpriced) }, { label: 'Tokens', value: tokens(tokTotal(v)) }, { label: 'Requests', value: thousands(v.req) }] });
    });
    items.sort(function (a, b) { return b.value - a.value; });
    hbars(c4.body, items);
    if (t.unpriced > 0) root.appendChild(unpricedNote(sel));
  }

  // ------------------------------------------------------------ subscription windows

  // The page is a snapshot: remaining times were computed at export and are
  // always printed next to the generation time they are relative to.
  function clock(ms) {
    var d = new Date(ms);
    return String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0');
  }
  function windowRows(w) {
    var known = w.source !== 'none', st = statusOf(w.fraction);
    return [
      el('div', { class: 'budget-amounts' }, el('b', null, tokens(w.counted)), el('span', null, w.limit > 0 ? 'of ' + tokens(w.limit) + ' counted tokens' : 'counted tokens')),
      known ? progress(w.fraction, st) : null,
      el('div', { class: 'budget-foot' }, el('span', null, w.source_label), known ? el('span', null, pct(w.fraction, 0) + ' used') : null),
      el('div', { class: 'hint' }, w.outlook + ' · ' + tokens(w.burn_per_min) + ' tok/min · ' + money(w.cost_per_hour) + '/h (' + w.burn_basis + ')')
    ];
  }
  function limitCard(p, asOf) {
    var s = p.session, title = p.family + (p.plan ? ' · ' + p.plan : ''), kids = [];
    if (s) {
      var kind = s.source === 'none' ? '' : s.hits_before_reset ? 'warn' : 'good';
      kids.push(el('div', { class: 'budget-top' },
        el('div', null, el('div', { class: 'budget-name' }, 'Current block · ' + title),
          el('div', { class: 'budget-period' }, clock(s.start) + '–' + clock(s.end) + (s.reported ? ' · reported by provider' : ' · estimated from request times'))),
        pill(kind, duration(s.remaining_ms) + ' left as of ' + asOf)));
      kids = kids.concat(windowRows(s));
      kids.push(el('div', { class: 'hint' }, 'Projected ' + tokens(s.projected) + ' counted tokens by ' + clock(s.end)));
    } else {
      kids.push(el('div', { class: 'budget-top' },
        el('div', null, el('div', { class: 'budget-name' }, 'Current block · ' + title), el('div', { class: 'budget-period' }, 'No active block as of ' + asOf)),
        pill('', 'Idle')));
    }
    var w = p.weekly;
    kids.push(el('div', { class: 'budget-period' }, 'Weekly · ' + (w.rolling ? 'rolling 7 days' :
      'resets ' + stamp(w.end) + ' (' + duration(w.remaining_ms) + ' after ' + asOf + ')') + (w.reported ? ' · reported by provider' : '')));
    kids = kids.concat(windowRows(w));
    if (p.reported_at) kids.push(el('div', { class: 'hint' }, 'Provider report from ' + stamp(p.reported_at)));
    return el('div', { class: 'card budget' }, kids);
  }
  function blockBars(container, plans) {
    var pts = [], max = 0;
    plans.forEach(function (p, k) {
      p.history.forEach(function (b) { pts.push({ b: b, fam: p.family, color: k ? 'var(--s2)' : 'var(--s1)', label: stamp(b.start).split(',')[0] }); });
    });
    pts.sort(function (x, y) { return x.b.start - y.b.start; });
    pts.forEach(function (p) { max = Math.max(max, p.b.counted); });
    if (max === 0) return emptyChart(container, 'No blocks in the last 30 days.');
    var fr = chartFrame(container, 220);
    max = niceMax(max);
    var s = svg('svg', { viewBox: '0 0 ' + fr.w + ' ' + fr.h, height: fr.h, role: 'img', 'aria-label': 'Counted tokens per 5 hour block' });
    var g = svg('g');
    s.appendChild(g);
    axes(g, fr, max, tokens);
    var plotW = fr.w - fr.l - fr.r, plotH = fr.h - fr.t - fr.b, slot = plotW / pts.length;
    var bw = Math.max(1, Math.min(28, slot * 0.66));
    var xOf = function (i) { return fr.l + slot * i + slot / 2; };
    pts.forEach(function (p, i) {
      var col = svg('g', { class: 'col' }), h = p.b.counted / max * plotH;
      col.appendChild(svg('rect', { class: 'wash', x: fr.l + slot * i, y: fr.t, width: slot, height: plotH }));
      if (h > 0) col.appendChild(svg('path', { class: 'seg-bar', d: topRounded(xOf(i) - bw / 2, fr.t + plotH - h, bw, h, 3), style: { fill: p.color } }));
      var hit = svg('rect', { class: 'hit', x: fr.l + slot * i, y: fr.t, width: slot, height: plotH });
      hit.addEventListener('mouseenter', function (e) {
        showTip(e, p.fam + ' · ' + stamp(p.b.start) + '–' + clock(p.b.end) + (p.b.active ? ' (active)' : ''), [
          { label: 'Counted', value: tokens(p.b.counted), color: p.color }, { label: 'All tokens', value: tokens(p.b.tokens) },
          { label: 'Requests', value: thousands(p.b.requests) }, { label: 'API-equivalent', value: money(p.b.cost) }]);
      });
      hit.addEventListener('mousemove', moveTip);
      hit.addEventListener('mouseleave', hideTip);
      col.appendChild(hit);
      g.appendChild(col);
    });
    xLabels(g, fr, pts, xOf);
    clear(container).appendChild(s);
  }
  function renderLimits(root) {
    var asOf = stamp(Date.parse(M.generated_at));
    root.appendChild(sectionHead('Subscription windows', 'Snapshot as of ' + asOf + ' · monitoring only · not affected by filters'));
    var grid = el('div', { class: 'grid budgets-grid' });
    D.limits.forEach(function (p) { grid.appendChild(limitCard(p, asOf)); });
    root.appendChild(grid);
    var legend = el('div', { class: 'legend' }, D.limits.map(function (p, k) {
      return el('span', null, el('i', { class: 'dot', style: { background: k ? 'var(--s2)' : 'var(--s1)' } }), p.family);
    }));
    var c = chartCard('span-12', 'Block history', 'Counted tokens per 5 hour block', legend);
    root.appendChild(el('div', { class: 'grid charts', style: { 'margin-top': '14px' } }, c.card));
    blockBars(c.body, D.limits);
  }

  function unpricedNote(list) {
    var names = {};
    list.forEach(function (f) { if (f[F.unpriced] > 0) names[D.dims.models[f[F.model]]] = true; });
    return el('p', { class: 'footnote' }, '* Some tokens have no known price (' + Object.keys(names).join(', ') + '). Add them under ',
      el('code', null, 'pricing:'), ' in config.yaml. Costs are estimates at API list prices; subscription plans are billed differently.');
  }

  function renderAgents(root) {
    var sel = selected(), byAgent = sumBy(sel, F.agent);
    root.appendChild(sectionHead('Agents', rangeLabel() + ' · detection status and data accuracy'));
    var grid = el('div', { class: 'grid agents-grid' });
    var ids = AGENT_ORDER.slice();
    D.dims.agents.forEach(function (a) { if (ids.indexOf(a) < 0) ids.push(a); });
    ids.forEach(function (id) {
      var info = agentInfo[id] || { name: id, display: id, enabled: true, installed: true, availability: 'actual', note: '' };
      var k = D.dims.agents.indexOf(id), t = k >= 0 ? byAgent.get(k) : null;
      var status = !info.enabled ? pill('', 'Disabled') : info.installed ? pill('good', 'Detected') : pill('', 'Not found');
      var acc = { actual: pill('accent', 'Actual counts'), estimated: pill('warn', 'Estimated counts'), unavailable: pill('', 'No local data') }[info.availability];
      var body;
      if (info.availability === 'unavailable') {
        body = el('div', { class: 'agent-empty' }, 'Usage is not available from local files, so nothing is shown.');
      } else if (!t) {
        body = el('div', { class: 'agent-empty' }, 'No usage recorded in this range' + (hasDimFilter() ? ' for the current filters.' : '.'));
      } else {
        var stat = function (label, v, big) { return el('div', { class: big ? 'big' : null }, el('span', null, label), el('b', null, v)); };
        body = el('div', { class: 'agent-stats' },
          stat('Total tokens', tokens(tokTotal(t)), true), stat('Estimated cost', costText(t.cost, t.unpriced), true),
          stat('Requests', thousands(t.req)), stat('Input', tokens(t.input)),
          stat('Output', tokens(t.output)), stat('Cache read', tokens(t.cr)),
          stat('Cache write', tokens(t.cw)), stat('Estimated requests', t.est ? thousands(t.est) : 'none'));
      }
      grid.appendChild(el('div', { class: 'card agent-card' },
        el('div', { class: 'agent-title' }, el('i', { class: 'dot', style: { background: agentColor(id), width: '12px', height: '12px' } }), el('h3', null, info.display)),
        el('div', { class: 'agent-badges' }, status, acc, info.files ? pill('', thousands(info.files) + ' files') : null),
        body,
        info.note ? el('p', { class: 'agent-note' }, info.note) : null));
    });
    root.appendChild(grid);
  }

  function renderSessions(root) {
    var list = selectedSessions();
    var q = state.sessQuery.trim().toLowerCase();
    if (q) {
      list = list.filter(function (s) {
        var hay = [s.id, agentName(D.dims.agents[s.agent]), D.dims.projects[s.project]].concat(s.models.map(function (m) { return D.dims.models[m]; })).join(' ').toLowerCase();
        return hay.indexOf(q) >= 0;
      });
    }
    var sub = rangeLabel();
    if (M.sessions_shown < M.sessions_total) sub += ' · export includes the ' + thousands(M.sessions_shown) + ' most recent of ' + thousands(M.sessions_total) + ' sessions';
    root.appendChild(sectionHead('Sessions', sub));
    var fill = function (s) { return s.context_window ? s.last_prompt / s.context_window : -1; };
    var tok = function (s) { return s.input + s.output + s.cache_read + s.cache_write; };
    var cols = [
      { key: 'id', label: 'Session', sort: function (s) { return s.id; }, cell: function (s) { return [el('span', { class: 'chev', 'aria-hidden': 'true' }, '›'), el('span', { class: 'mono' }, shortID(s.id))]; } },
      { key: 'agent', label: 'Agent', sort: function (s) { return agentName(D.dims.agents[s.agent]); }, cell: function (s) { var id = D.dims.agents[s.agent]; return [el('i', { class: 'dot', style: { background: agentColor(id), 'margin-right': '7px' } }), agentName(id)]; } },
      { key: 'project', label: 'Project', cls: 'truncate', sort: function (s) { return D.dims.projects[s.project]; }, cell: function (s) { return D.dims.projects[s.project]; } },
      { key: 'model', label: 'Model', sort: function (s) { return s.models.length ? D.dims.models[s.models[0]] : ''; }, cell: function (s) { return s.models.length ? D.dims.models[s.models[0]] + (s.models.length > 1 ? ' +' + (s.models.length - 1) : '') : '—'; } },
      { key: 'start', label: 'Started', sort: function (s) { return s.start; }, cell: function (s) { return stamp(s.start); } },
      { key: 'duration', label: 'Duration', num: true, sort: function (s) { return s.end - s.start; }, cell: function (s) { return duration(s.end - s.start); } },
      { key: 'tokens', label: 'Tokens', num: true, sort: tok, cell: function (s) { return tokens(tok(s)); } },
      { key: 'cost', label: 'Cost', num: true, sort: function (s) { return s.cost; }, cell: function (s) { return costText(s.cost, s.unpriced); } },
      { key: 'ctx', label: 'Context', num: true, sort: fill, cell: function (s) {
        var f = fill(s);
        if (f < 0) return el('span', { class: 'muted' }, s.last_prompt ? tokens(s.last_prompt) : '—');
        return el('div', { class: 'ctx' }, el('span', null, pct(f, 0)), progress(f, f >= 0.8 ? 'warn' : '', true));
      } },
      { key: 'end', label: 'Last active', sort: function (s) { return s.end; }, cell: function (s) { return stamp(s.end); } }
    ];
    var tbl = table('sessions', cols, list, {
      limit: state.sessShown,
      empty: q ? 'No sessions match “' + state.sessQuery + '”.' : 'No sessions match the current filters.',
      expand: function (s, tr, tbody, span) {
        var key = s.agent + '/' + s.id;
        tr.classList.add('expandable');
        tr.tabIndex = 0;
        tr.setAttribute('aria-expanded', state.open[key] ? 'true' : 'false');
        var toggle = function () { state.open[key] = !state.open[key]; render(); };
        tr.addEventListener('click', toggle);
        tr.addEventListener('keydown', function (e) { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggle(); } });
        if (!state.open[key]) return;
        tr.classList.add('open');
        var prompt = s.input + s.cache_read + s.cache_write;
        var item = function (label, v) { return el('div', null, el('span', null, label), el('b', null, v)); };
        tbody.appendChild(el('tr', { class: 'detail' }, el('td', { colspan: span }, el('div', { class: 'detail-grid' },
          item('Session ID', s.id),
          item('Project', D.dims.projects[s.project]),
          item('Models', s.models.map(function (m) { return D.dims.models[m]; }).join(', ') || '—'),
          item('First request', stamp(s.start)),
          item('Last request', stamp(s.end)),
          item('Requests', thousands(s.requests)),
          item('Input', tokens(s.input)),
          item('Output', tokens(s.output)),
          item('Cache read', tokens(s.cache_read)),
          item('Cache write', tokens(s.cache_write)),
          item('Cache hit rate', pct(prompt ? s.cache_read / prompt : 0)),
          item('Peak prompt', tokens(s.max_prompt)),
          item('Current context', s.context_window ? tokens(s.last_prompt) + ' of ' + tokens(s.context_window) : tokens(s.last_prompt)),
          item('Estimated cost', costText(s.cost, s.unpriced) + (s.requests ? ' · ' + money(s.cost / s.requests) + '/req' : '')),
          s.estimated ? item('Estimated counts', thousands(s.estimated) + ' requests') : null))));
      }
    });
    var search = el('input', { class: 'search', type: 'search', placeholder: 'Search session, project, model…', 'aria-label': 'Search sessions', value: state.sessQuery });
    search.addEventListener('input', function () {
      state.sessQuery = search.value; state.sessShown = 50;
      var pos = search.selectionStart;
      render();
      var again = document.querySelector('#tab-sessions .search');
      if (again) { again.focus(); try { again.setSelectionRange(pos, pos); } catch (e) { /* ignore */ } }
    });
    var foot = el('div', { class: 'table-foot' }, el('span', null, 'Showing ' + thousands(Math.min(state.sessShown, tbl.total)) + ' of ' + thousands(tbl.total) + ' sessions · click a row for details'));
    if (tbl.total > state.sessShown) {
      foot.appendChild(el('button', { class: 'btn', type: 'button', onclick: function () { state.sessShown += 100; render(); } }, 'Show more'));
    }
    root.appendChild(el('div', { class: 'card table-card' }, el('div', { class: 'table-tools' }, search, el('span', { class: 'hint' }, 'Context = last prompt size vs. model context window')), tbl.node, foot));
  }

  function groupRows(col, dict) {
    var sel = selected(), m = sumBy(sel, col), all = 0, rows = [];
    m.forEach(function (v) { all += v.cost; });
    m.forEach(function (v, k) { v.name = dict[k]; v.share = all > 0 ? v.cost / all : 0; v.tokens = tokTotal(v); rows.push(v); });
    return { rows: rows, sel: sel };
  }
  function numCol(key, label, fmt, field) {
    return { key: key, label: label, num: true, sort: function (r) { return r[field || key]; }, cell: function (r) { return fmt(r[field || key]); } };
  }

  function renderProjects(root) {
    var g = groupRows(F.project, D.dims.projects);
    var sub = rangeLabel() + (M.hide_project_paths ? ' · project paths hidden (privacy.hide_project_paths)' : '');
    root.appendChild(sectionHead('Projects', sub));
    var tbl = table('projects', [
      { key: 'name', label: 'Project', cls: 'truncate', sort: function (r) { return r.name; }, cell: function (r) { return r.name; } },
      numCol('req', 'Requests', thousands),
      numCol('input', 'Input', tokens), numCol('output', 'Output', tokens),
      numCol('tokens', 'Total tokens', tokens),
      { key: 'cost', label: 'Cost', num: true, sort: function (r) { return r.cost; }, cell: function (r) { return costText(r.cost, r.unpriced); } },
      { key: 'share', label: 'Share of spend', num: true, sort: function (r) { return r.share; }, cell: function (r) { return shareCell(r.share); } }
    ], g.rows);
    root.appendChild(el('div', { class: 'card table-card' }, tbl.node));
    if (g.rows.some(function (r) { return r.unpriced > 0; })) root.appendChild(unpricedNote(g.sel));
  }

  function renderModels(root) {
    var g = groupRows(F.model, D.dims.models);
    g.rows.forEach(function (r) { var mi = modelInfo[r.name] || {}; r.provider = mi.provider || '—'; r.info = mi; r.cached = r.cr + r.cw; });
    root.appendChild(sectionHead('Models', rangeLabel()));
    var tbl = table('models', [
      { key: 'name', label: 'Model', sort: function (r) { return r.name; }, cell: function (r) { return [r.name, r.info.priced ? null : [' ', pill('warn', 'unpriced')]]; } },
      { key: 'provider', label: 'Provider', sort: function (r) { return r.provider; }, cell: function (r) { return r.provider; } },
      numCol('input', 'Input', tokens), numCol('output', 'Output', tokens),
      { key: 'cached', label: 'Cached', num: true, sort: function (r) { return r.cached; }, cell: function (r) { return el('span', { title: 'Cache read ' + tokens(r.cr) + ' · cache write ' + tokens(r.cw) }, tokens(r.cached)); } },
      numCol('req', 'Requests', thousands),
      { key: 'cost', label: 'Cost', num: true, sort: function (r) { return r.cost; }, cell: function (r) { return costText(r.cost, r.unpriced); } },
      { key: 'price', label: 'List $/M in · out', num: true, sort: function (r) { return r.info.priced ? r.info.input_price : -1; }, cell: function (r) { return r.info.priced ? r.info.input_price + ' · ' + r.info.output_price : el('span', { class: 'muted' }, '—'); } },
      { key: 'share', label: 'Share', num: true, sort: function (r) { return r.share; }, cell: function (r) { return shareCell(r.share); } }
    ], g.rows);
    root.appendChild(el('div', { class: 'card table-card' }, tbl.node));
    root.appendChild(el('p', { class: 'footnote' }, 'List prices are USD per million tokens from TokenManager’s price table (plus config.yaml overrides). Cached = cache read + cache write.'));
    if (g.rows.some(function (r) { return r.unpriced > 0; })) root.appendChild(unpricedNote(g.sel));
  }

  // Tools are attributed in Go over the last 30 days (like Optimization), so
  // the range and dimension filters do not apply. Only call counts are
  // actual; tokens and costs are estimates.
  function renderTools(root) {
    var ts = D.tools || [];
    root.appendChild(sectionHead('Tools', 'Last 30 days · not affected by the filters above · calls actual, tokens and cost estimated' + (M.filter ? ' (export filtered: ' + M.filter + ')' : '')));
    if (!ts.length) {
      root.appendChild(el('div', { class: 'card' }, el('div', { class: 'empty' }, 'No tool calls recorded in the last 30 days.')));
      return;
    }
    var c = chartCard('span-12', 'Share of input by tool', 'Estimated first-time + carried result tokens over all prompt tokens · top 10');
    root.appendChild(el('div', { class: 'grid' }, c.card));
    hbars(c.body, ts.slice().sort(function (a, b) { return b.share - a.share; }).slice(0, 10).filter(function (t) { return t.share > 0; }).map(function (t) {
      return {
        label: t.key, value: t.share, color: 'var(--s1)', text: pct(t.share),
        tip: [{ label: 'Calls', value: thousands(t.calls) }, { label: 'First-time (est.)', value: tokens(t.first_time_input_tokens) },
          { label: 'Carried (est.)', value: tokens(t.carried_tokens) }, { label: 'Cost (est.)', value: money(t.estimated_cost) }]
      };
    }), true);
    var tbl = table('tools', [
      { key: 'key', label: 'Tool', sort: function (t) { return t.key; }, cell: function (t) { return t.key; } },
      numCol('calls', 'Calls', thousands),
      numCol('avg', 'Avg result', tokens, 'avg_result_tokens'),
      numCol('p95', 'P95 result', tokens, 'p95_result_tokens'),
      numCol('first', 'Est. result tok', tokens, 'first_time_input_tokens'),
      numCol('carried', 'Est. carried tok', tokens, 'carried_tokens'),
      { key: 'share', label: 'Share of input', num: true, sort: function (t) { return t.share; }, cell: function (t) { return shareCell(t.share); } },
      { key: 'cost', label: 'Est. cost', num: true, sort: function (t) { return t.estimated_cost; }, cell: function (t) { return money(t.estimated_cost); } }
    ], ts);
    root.appendChild(el('div', { class: 'card table-card', style: { 'margin-top': '14px' } }, tbl.node));
    root.appendChild(el('p', { class: 'footnote' }, 'Result tokens are result bytes ÷ 4 charged to the next request in the session; carried tokens are the same result re-read from cache by later requests until the session ends or is compacted; each request’s output is split across its tool calls by input size. Only tool names and byte counts are stored. MCP tools are shown as server/tool.'));
  }

  var SEV = { high: ['bad', 'High'], medium: ['warn', 'Medium'], low: ['accent', 'Low'], info: ['', 'Info'] };
  function renderOptimization(root) {
    var fs = D.findings, totalSave = 0, counts = {};
    fs.forEach(function (f) { totalSave += f.estimated_monthly_savings; counts[f.severity] = (counts[f.severity] || 0) + 1; });
    root.appendChild(sectionHead('Optimization', 'Last 30 days · not affected by the filters above' + (M.filter ? ' (export filtered: ' + M.filter + ')' : '')));
    if (!fs.length) {
      root.appendChild(el('div', { class: 'card' }, el('div', { class: 'empty' }, M.records ? 'No issues found. Usage looks efficient.' : 'No usage recorded yet, so there is nothing to analyze.')));
      return;
    }
    root.appendChild(el('div', { class: 'grid kpis' },
      kpi('Estimated monthly savings', money(totalSave), 'Findings may overlap; estimates, not guarantees'),
      kpi('Findings', String(fs.length), el('span', { class: 'sev-strip' }, ['high', 'medium', 'low', 'info'].filter(function (s) { return counts[s]; }).map(function (s) {
        return pill(SEV[s][0] === 'accent' ? 'accent' : SEV[s][0] || 'info', counts[s] + ' ' + SEV[s][1].toLowerCase());
      })))));
    var list = el('div', { class: 'findings', style: { 'margin-top': '14px' } });
    fs.forEach(function (f) {
      var sev = SEV[f.severity] || SEV.info;
      var dl = el('dl', null,
        el('dt', null, 'Why'), el('dd', null, f.detail),
        el('dt', null, 'Recommendation'), el('dd', null, f.recommendation));
      if (f.tokens_per_session > 0) { dl.appendChild(el('dt', null, 'Per session')); dl.appendChild(el('dd', null, '~' + tokens(f.tokens_per_session) + ' fewer tokens per session (estimate)')); }
      list.appendChild(el('div', { class: 'card finding' },
        el('div', null,
          el('div', { style: { 'margin-bottom': '8px' } }, pill(sev[0] || 'info', sev[1])),
          el('h3', null, f.title),
          el('div', { class: 'where' }, f.project ? el('span', null, 'Project: ' + f.project) : null, f.file ? el('span', { class: 'mono' }, f.file) : null, el('span', null, f.kind))),
        el('div', { class: 'save' }, f.estimated_monthly_savings >= 0.01 ? [el('b', null, money(f.estimated_monthly_savings)), el('span', null, 'est. per month')] : el('span', null, 'savings not estimated')),
        dl));
    });
    root.appendChild(list);
  }

  function renderBudgets(root) {
    root.appendChild(sectionHead('Budgets', 'Current periods · monitoring only, provider limits are not changed · not affected by filters'));
    var periods = ['daily', 'weekly', 'monthly'];
    var cap = { daily: 'Daily', weekly: 'Weekly', monthly: 'Monthly' };
    var since = { daily: 'today', weekly: 'since ' + dayLabel(M.week_start), monthly: M.month_label };
    function card(b, label) {
      var st = statusOf(b.fraction);
      var stLabel = st === 'bad' ? 'Exceeded' : st === 'warn' ? 'Near limit' : 'Healthy';
      return el('div', { class: 'card budget' },
        el('div', { class: 'budget-top' },
          el('div', null, el('div', { class: 'budget-name' }, label), el('div', { class: 'budget-period' }, cap[b.period] + ' · ' + since[b.period])),
          pill(st, stLabel)),
        el('div', { class: 'budget-amounts' }, el('b', null, money(b.spent)), el('span', null, 'of ' + money(b.limit))),
        progress(b.fraction, st),
        el('div', { class: 'budget-foot' },
          b.remaining >= 0 ? el('span', null, money(b.remaining) + ' remaining') : el('span', { class: 'over' }, money(-b.remaining) + ' over budget'),
          el('span', null, pct(b.fraction, 0) + ' used')),
        b.projected ? el('div', { class: 'hint' }, 'Projected ' + money(b.projected) + ' by month end at the current pace' + (b.projected > b.limit ? ' (over budget)' : '')) : null);
    }
    var global = el('div', { class: 'grid budgets-grid' });
    periods.forEach(function (p) {
      var b = D.budgets.filter(function (x) { return x.scope === 'global' && x.period === p; })[0];
      if (b) global.appendChild(card(b, 'All usage'));
      else global.appendChild(el('div', { class: 'card budget unset' },
        el('div', { class: 'budget-top' }, el('div', null, el('div', { class: 'budget-name' }, 'All usage'), el('div', { class: 'budget-period' }, cap[p])), pill('', 'Not set')),
        el('div', { class: 'hint' }, 'tokenmanager budget set ' + p + ' <amount>')));
    });
    root.appendChild(sectionHead('Global', null));
    root.appendChild(global);
    [['agent', 'Per agent'], ['project', 'Per project'], ['model', 'Per model']].forEach(function (s) {
      var bs = D.budgets.filter(function (b) { return b.scope === s[0]; });
      root.appendChild(sectionHead(s[1], null));
      if (!bs.length) {
        root.appendChild(el('p', { class: 'hint' }, 'None configured. Example: ', el('code', null, 'tokenmanager budget set monthly 40 --for ' + s[0] + ':' + { agent: 'claude-code', project: 'my-app', model: 'opus' }[s[0]])));
        return;
      }
      var grid = el('div', { class: 'grid budgets-grid' });
      bs.forEach(function (b) { grid.appendChild(card(b, s[0] === 'agent' ? agentName(b.target) : b.target)); });
      root.appendChild(grid);
    });
    if (M.thresholds && M.thresholds.length) {
      root.appendChild(el('p', { class: 'footnote' }, 'Alert thresholds: ' + M.thresholds.map(function (t) { return pct(t, 0); }).join(', ') + '. Bars turn amber at 80% and red when a budget is exceeded.'));
    }
  }

  // ------------------------------------------------------------ shell

  var RENDER = { overview: renderOverview, agents: renderAgents, sessions: renderSessions, projects: renderProjects, models: renderModels, tools: renderTools, optimization: renderOptimization, budgets: renderBudgets };

  function render() {
    hideTip();
    document.querySelectorAll('#range button').forEach(function (b) { b.setAttribute('aria-pressed', b.dataset.range === state.range ? 'true' : 'false'); });
    document.querySelectorAll('#tabs a').forEach(function (a) {
      if (a.dataset.tab === state.tab) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
    });
    ['agent', 'project', 'model'].forEach(function (k) { document.getElementById('f-' + k).classList.toggle('active', state[k] >= 0); });
    document.getElementById('reset').hidden = !hasDimFilter() && state.range === '30d';
    document.querySelectorAll('.tab').forEach(function (s) {
      var on = s.dataset.tab === state.tab;
      s.hidden = !on;
      if (on) { clear(s); RENDER[state.tab](s); }
    });
  }

  function fillSelect(id, label, values, display) {
    var sel = document.getElementById(id);
    var opts = values.map(function (v, i) { return { i: i, text: display ? display(v) : v }; });
    opts.sort(function (a, b) { return a.text.localeCompare(b.text); });
    sel.appendChild(el('option', { value: -1 }, 'All ' + label));
    opts.forEach(function (o) { sel.appendChild(el('option', { value: o.i }, o.text)); });
    sel.addEventListener('change', function () {
      state[id.slice(2)] = +sel.value; state.sessShown = 50; render();
    });
  }

  function banner() {
    var b = document.getElementById('banner');
    if (!M.records) {
      b.appendChild(el('div', { class: 'banner accent' }, el('div', null, el('b', null, 'No usage recorded yet. '),
        'Use a supported coding agent, then run ', el('code', null, 'tokenmanager web'), ' again to regenerate this page.')));
    }
    if (M.filter) {
      b.appendChild(el('div', { class: 'banner' }, el('div', null, el('b', null, 'Filtered export. '), 'Generated with ' + M.filter + '; other usage is not included in this file.')));
    }
  }

  function setTheme(t) {
    document.documentElement.setAttribute('data-theme', t);
    try { localStorage.setItem('tokenmanager-theme', t); } catch (e) { /* storage unavailable */ }
  }

  function init() {
    var saved = null;
    try { saved = localStorage.getItem('tokenmanager-theme'); } catch (e) { /* storage unavailable */ }
    if (saved === 'light' || saved === 'dark') document.documentElement.setAttribute('data-theme', saved);
    document.getElementById('theme-toggle').addEventListener('click', function () {
      setTheme(document.documentElement.getAttribute('data-theme') === 'light' ? 'dark' : 'light');
    });

    var gen = new Date(M.generated_at);
    document.getElementById('generated').textContent = 'Snapshot ' + MONTHS[gen.getMonth()] + ' ' + gen.getDate() + ', ' + gen.getFullYear() + ' ' +
      String(gen.getHours()).padStart(2, '0') + ':' + String(gen.getMinutes()).padStart(2, '0') + ' · static export';
    var foot = document.getElementById('footer');
    append(foot, [
      el('span', null, 'Generated by TokenManager ' + (M.version || '') + (M.scanned ? ' after a fresh scan' : ' from the existing database (--no-scan)')),
      el('span', null, 'This page does not refresh. Run ', el('code', null, 'tokenmanager web'), ' to regenerate.'),
      el('span', null, 'Contains aggregated usage data only; handle it as private.'),
      el('span', null, 'Costs in ' + M.currency + ', estimated at API list prices.')
    ]);

    fillSelect('f-agent', 'agents', D.dims.agents, agentName);
    fillSelect('f-project', 'projects', D.dims.projects);
    fillSelect('f-model', 'models', D.dims.models);
    if (!M.records) state.range = 'all';
    document.querySelectorAll('#range button').forEach(function (b) {
      b.addEventListener('click', function () { state.range = b.dataset.range; state.sessShown = 50; render(); });
    });
    document.getElementById('reset').addEventListener('click', function () {
      state.agent = state.project = state.model = -1; state.range = '30d';
      ['f-agent', 'f-project', 'f-model'].forEach(function (id) { document.getElementById(id).value = '-1'; });
      render();
    });
    var fromHash = function () {
      var h = location.hash.replace('#', '');
      state.tab = RENDER[h] ? h : 'overview';
    };
    window.addEventListener('hashchange', function () { fromHash(); render(); window.scrollTo(0, 0); });
    // Switch tabs in script so it also works where hash navigation is
    // blocked (embedded viewers, data: URLs); the hash is kept when allowed.
    document.querySelectorAll('#tabs a').forEach(function (a) {
      a.addEventListener('click', function (e) {
        e.preventDefault();
        state.tab = a.dataset.tab;
        try { history.replaceState(null, '', '#' + state.tab); } catch (err) { /* not allowed here */ }
        render();
        window.scrollTo(0, 0);
      });
    });
    fromHash();
    banner();
    var lastW = window.innerWidth, timer = null;
    window.addEventListener('resize', function () {
      clearTimeout(timer);
      timer = setTimeout(function () {
        if (Math.abs(window.innerWidth - lastW) > 4 && state.tab === 'overview') { lastW = window.innerWidth; render(); }
      }, 150);
    });
    render();
  }

  init();
})();
