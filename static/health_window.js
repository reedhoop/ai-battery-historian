/*
 * health_window.js — 电池健康度「按时间段统计」交互层 (P3-C time-range)。
 *
 * 在结果页的健康度卡片上方注入：
 *   1) 预设档位按钮（全程 / 近1小时 / 近3小时 / 近12小时）
 *   2) 一条可拖拽选区的电量曲线（d3 brushX）
 * 用户选择时间段后调用 GET /api/health?from=&to= 重新评分，并重渲染卡片。
 *
 * 该脚本不依赖 Closure，独立运行；结果页 HTML 由服务端渲染后注入 DOM，
 * 故用 MutationObserver 等待 #panel-health 出现再初始化。
 */
(function () {
  'use strict';

  var HEALTH_PANEL = '#panel-health';
  var BATTERY_LEVEL_URL = '/api/batterylevel';
  var HEALTH_URL = '/api/health';

  function $(sel, root) { return (root || document).querySelector(sel); }
  function $$(sels, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sels)); }
  function el(tag, cls, html) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (html != null) e.innerHTML = html;
    return e;
  }
  function escapeHtml(s) {
    if (s == null) return '';
    return String(s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }
  function fmtTime(ms) {
    var d = new Date(ms);
    function p(n) { return (n < 10 ? '0' : '') + n; }
    return p(d.getHours()) + ':' + p(d.getMinutes());
  }
  function fmtDur(ms) {
    var h = Math.floor(ms / 3600000);
    var m = Math.round((ms % 3600000) / 60000);
    if (h > 0) return h + '小时' + (m > 0 ? m + '分' : '');
    return m + '分';
  }

  // 按 /api/health JSON 重渲染卡片 body（与 health_card.html 模板对齐）。
  function renderHealthCard(rep) {
    var body = $(HEALTH_PANEL + ' .panel-body');
    if (!body) return;
    var html = '';
    html += '<div class="health-head">';
    html += '  <div class="health-score">' + Math.round(rep.score) + '</div>';
    html += '  <div class="health-score-meta">';
    html += '    <div>综合评分 / 100</div>';
    html += '    <div class="health-grade-text">等级 ' + escapeHtml(rep.grade) + '</div>';
    if (rep.evaluated < rep.total) {
      html += '<div class="health-coverage">已评估 ' + rep.evaluated + ' / ' + rep.total + ' 项（N/A 未计入）</div>';
    }
    if (rep.isWindow) {
      html += '<div class="health-window-badge">时间窗评分 · ' + fmtTime(rep.windowStartMs) + '–' +
        fmtTime(rep.windowEndMs) + ' · ' + fmtDur(rep.windowEndMs - rep.windowStartMs) + '</div>';
    }
    html += '  </div>';
    html += '</div>';
    if (rep.summary) html += '<div class="health-summary">' + escapeHtml(rep.summary) + '</div>';
    (rep.alerts || []).forEach(function (a) {
      var cls = a.level === 'critical' ? 'alert-danger' : (a.level === 'warning' ? 'alert-warning' : 'alert-info');
      html += '<div class="alert ' + cls + '"><strong>' + escapeHtml(a.category) +
        '</strong>：' + escapeHtml(a.message) + (a.value ? ' (' + escapeHtml(a.value) + ')' : '') + '</div>';
    });
    if (rep.dimensions && rep.dimensions.length) {
      html += '<table class="table table-condensed health-dims"><thead><tr><th>维度</th><th>评分</th><th>状态</th><th>说明</th></tr></thead><tbody>';
      rep.dimensions.forEach(function (d) {
        var scoreCell = d.status === 'n/a' ? 'N/A' : Math.round(d.score);
        var stCls = d.status === 'good' ? 'label-success' : (d.status === 'fair' ? 'label-warning' : (d.status === 'poor' ? 'label-danger' : 'label-default'));
        var frozen = d.weight === 0 ? ' <span class="label label-default">全程值</span>' : '';
        html += '<tr><td>' + escapeHtml(d.label) + frozen + '</td><td>' + scoreCell +
          '</td><td><span class="label ' + stCls + '">' + escapeHtml(d.status) + '</span></td>' +
          '<td class="health-dim-detail">' + escapeHtml(d.detail) + '</td></tr>';
      });
      html += '</tbody></table>';
    }
    body.innerHTML = html;
    var gradeLabel = $(HEALTH_PANEL + ' .health-grade');
    if (gradeLabel) gradeLabel.textContent = '等级 ' + rep.grade;
  }

  function clearPresetActive() {
    $$('.health-preset').forEach(function (b) { b.classList.remove('active'); });
  }

  // 请求指定时间窗的健康度并重渲染。activeBtn 可选。
  function loadWindow(from, to, activeBtn) {
    var url = HEALTH_URL + '?from=' + Math.round(from) + '&to=' + Math.round(to);
    fetch(url).then(function (r) { return r.json(); }).then(function (rep) {
      if (!rep || rep.error) { console.warn('health window:', (rep && rep.error) || 'empty'); return; }
      renderHealthCard(rep);
      clearPresetActive();
      if (activeBtn) activeBtn.classList.add('active');
    }).catch(function (e) { console.error('health window fetch failed', e); });
  }

  function drawBrush(container, pts, spanStart, spanEnd, presets) {
    var W = container.clientWidth || 640;
    var H = 96, m = { t: 8, r: 12, b: 22, l: 32 };
    var iw = W - m.l - m.r, ih = H - m.t - m.b;

    var svg = d3.select(container).append('svg')
      .attr('width', W).attr('height', H)
      .attr('viewBox', '0 0 ' + W + ' ' + H)
      .attr('preserveAspectRatio', 'none');
    var g = svg.append('g').attr('transform', 'translate(' + m.l + ',' + m.t + ')');

    var x = d3.scaleLinear().domain([spanStart, spanEnd]).range([0, iw]);
    var y = d3.scaleLinear().domain([0, 100]).range([ih, 0]);

    var area = d3.area()
      .x(function (d) { return x(d.t); })
      .y0(ih).y1(function (d) { return y(d.level); })
      .curve(d3.curveStepAfter);
    g.append('path').datum(pts).attr('class', 'health-level-area').attr('d', area);

    g.append('g').attr('class', 'health-axis').attr('transform', 'translate(0,' + ih + ')')
      .call(d3.axisBottom(x).ticks(4).tickFormat(function (d) { return fmtTime(d); }));

    var firstMove = true;
    var brush = d3.brushX().extent([[0, 0], [iw, ih]]).on('end', function () {
      if (!d3.event.selection) return;
      if (firstMove) { firstMove = false; return; } // 初始化时的默认全选不触发加载
      var sx = d3.event.selection;
      var from = x.invert(sx[0]), to = x.invert(sx[1]);
      loadWindow(from, to, null);
    });
    g.append('g').attr('class', 'health-brush').call(brush).call(brush.move, [0, iw]);
  }

  function init(panel) {
    if (panel.__hwInited) return;
    panel.__hwInited = true;

    var stale = $('#health-window-controls');
    if (stale && stale.parentNode) stale.parentNode.removeChild(stale);

    var root = el('div', 'health-window-controls');
    root.id = 'health-window-controls';

    var presets = el('div', 'health-preset-bar');
    var defs = [
      { label: '全程', kind: 'full' },
      { label: '近1小时', kind: 'last', ms: 3600000 },
      { label: '近3小时', kind: 'last', ms: 3 * 3600000 },
      { label: '近12小时', kind: 'last', ms: 12 * 3600000 }
    ];
    defs.forEach(function (d) {
      var b = el('button', 'btn btn-xs health-preset', d.label);
      b.type = 'button';
      b.dataset.kind = d.kind;
      if (d.ms) b.dataset.ms = d.ms;
      presets.appendChild(b);
    });
    root.appendChild(presets);

    var chart = el('div', 'health-brush-chart');
    chart.id = 'health-brush-chart';
    root.appendChild(chart);

    root.appendChild(el('div', 'health-brush-hint', '拖动下方选区可重算该时间段的健康度'));

    panel.parentNode.insertBefore(root, panel);

    fetch(BATTERY_LEVEL_URL).then(function (r) { return r.json(); }).then(function (data) {
      var pts = data.points || [];
      if (!pts.length) {
        chart.textContent = '此报告缺少逐段电量数据，暂不支持按时间段重算健康度（仅显示全程评分）。';
        presets.querySelectorAll('.health-preset').forEach(function (b) {
          b.disabled = true;
          b.classList.add('disabled');
        });
        return;
      }
      pts.sort(function (a, b) { return a.t - b.t; });
      var spanStart = pts[0].t, spanEnd = pts[pts.length - 1].t;

      presets.querySelectorAll('.health-preset').forEach(function (b) {
        b.addEventListener('click', function () {
          if (b.dataset.kind === 'full') {
            loadWindow(spanStart, spanEnd, b);
          } else {
            var ms = +b.dataset.ms;
            var to = spanEnd, from = Math.max(spanStart, spanEnd - ms);
            loadWindow(from, to, b);
          }
        });
      });
      var fullBtn = presets.querySelector('[data-kind="full"]');
      if (fullBtn) fullBtn.classList.add('active');

      drawBrush(chart, pts, spanStart, spanEnd, presets);
    }).catch(function (e) { chart.textContent = '电量曲线加载失败'; console.error(e); });
  }

  // 结果页由服务端渲染后注入 DOM，故等待 #panel-health 出现。
  function maybeInit() {
    var panel = $(HEALTH_PANEL);
    if (panel && !panel.__hwInited) init(panel);
  }
  function observe() {
    var target = $('#body-contents') || document.body;
    if (window.MutationObserver) {
      var mo = new MutationObserver(function () { maybeInit(); });
      mo.observe(target, { childList: true, subtree: true });
    }
    // 兜底：定时轮询（老旧浏览器 / observer 未触发）
    var t = setInterval(maybeInit, 800);
    setTimeout(function () { clearInterval(t); }, 30000);
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', observe);
  } else {
    observe();
  }
})();
