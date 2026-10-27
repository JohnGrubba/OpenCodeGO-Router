/* OpenCode Go Router — dashboard */
(() => {
  'use strict';

  const CSRF = 'OpenCodeRouter';
  const state = {
    session: null,
    settings: null,
    models: null,
    modelsAt: 0,
    setup: null,
    route: { name: 'overview', id: null },
    range: '24h',
    plan: 'go',
    keySearch: '',
    activity: { key: '', model: '', status: '', rows: [], next: 0, loaded: false },
    charts: 0,
  };

  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

  // ---------- utilities ----------
  function esc(v) {
    return String(v ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  }
  function fmtMoney(v) {
    v = Number(v) || 0;
    if (v === 0) return '$0.00';
    if (v < 0.01) return '$' + v.toFixed(4);
    if (v < 1) return '$' + v.toFixed(3);
    return '$' + v.toFixed(2);
  }
  function fmtMoneyExact(v) {
    v = Number(v) || 0;
    if (v === 0) return '$0.00';
    if (v < 1) return '$' + v.toFixed(4);
    return '$' + v.toFixed(2);
  }
  function fmtNum(v) {
    return new Intl.NumberFormat().format(Number(v) || 0);
  }
  function fmtTokens(v) {
    v = Number(v) || 0;
    if (v >= 1e9) return (v / 1e9).toFixed(2) + 'B';
    if (v >= 1e6) return (v / 1e6).toFixed(2) + 'M';
    if (v >= 1e4) return (v / 1e3).toFixed(1) + 'K';
    return fmtNum(v);
  }
  function fmtDur(ms) {
    ms = Number(ms) || 0;
    if (ms >= 1000) return (ms / 1000).toFixed(1) + 's';
    return Math.round(ms) + 'ms';
  }
  function fmtDate(ts) {
    if (!ts) return '—';
    return new Date(ts * 1000).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
  }
  function fmtDateTime(ts) {
    if (!ts) return '—';
    return new Date(ts * 1000).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  }
  function relTime(ts) {
    if (!ts) return 'Never';
    const delta = ts - Date.now() / 1000;
    if (delta > 45) {
      const d = delta;
      if (d < 3600) return 'in ' + Math.ceil(d / 60) + 'm';
      if (d < 86400) return 'in ' + Math.ceil(d / 3600) + 'h';
      if (d < 86400 * 14) return 'in ' + Math.ceil(d / 86400) + 'd';
      return fmtDate(ts);
    }
    const d = Math.max(0, -delta);
    if (d < 45) return 'just now';
    if (d < 3600) return Math.floor(d / 60) + 'm ago';
    if (d < 86400) return Math.floor(d / 3600) + 'h ago';
    if (d < 86400 * 14) return Math.floor(d / 86400) + 'd ago';
    return fmtDate(ts);
  }
  function dateInput(ts) {
    if (!ts) return '';
    const d = new Date(ts * 1000);
    d.setMinutes(d.getMinutes() - d.getTimezoneOffset());
    return d.toISOString().slice(0, 10);
  }
  function fmtTimeShort(ts, long) {
    const d = new Date(ts * 1000);
    if (long) return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
    return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
  }
  function windowLabel(sec) {
    sec = Number(sec);
    if (sec === 18000) return '5h';
    if (sec === 86400) return '24h';
    if (sec === 604800) return '7d';
    if (sec === 2592000) return '30d';
    if (sec % 86400 === 0) return (sec / 86400) + 'd';
    if (sec % 3600 === 0) return (sec / 3600) + 'h';
    if (sec % 60 === 0) return (sec / 60) + 'm';
    return sec + 's';
  }
  function metricLabel(metric) {
    return metric === 'cost' ? 'Spend' : metric === 'requests' ? 'Requests' : 'Tokens';
  }
  function fmtLimit(v, metric) {
    if (metric === 'cost') return fmtMoneyExact(v);
    if (metric === 'tokens') return fmtTokens(v);
    return fmtNum(v);
  }
  function pct(used, max) {
    if (!max) return 0;
    return Math.max(0, Math.min(100, (used / max) * 100));
  }
  function barClass(percent) {
    return percent >= 100 ? 'danger' : percent >= 80 ? 'warn' : '';
  }
  function planLabel(plan) {
    return plan === 'go-plus' ? 'Go Plus' : 'Go';
  }
  function statusBadge(status) {
    const map = {
      active: ['ok', 'Active'],
      limited: ['warn', 'Limit reached'],
      disabled: ['danger', 'Disabled'],
      expired: ['danger', 'Expired'],
    };
    const [cls, label] = map[status] || ['', status];
    return `<span class="status ${cls}"><span class="dot ${cls === 'warn' ? 'warn' : cls === 'danger' ? 'danger' : 'ok'}"></span>${esc(label)}</span>`;
  }
  function modelName(id) {
    const m = (state.models || []).find((x) => x.id === id);
    return m ? m.name : id;
  }
  function toggleHidden(el, hidden) {
    if (!el) return;
    el.hidden = hidden;
    el.classList.toggle('hidden', hidden);
  }
  function copyText(text, label = 'Copied') {
    const done = () => toast(label, 'ok');
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(done).catch(() => fallbackCopy(text, done));
    } else {
      fallbackCopy(text, done);
    }
  }
  function fallbackCopy(text, done) {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy'); done(); } catch { toast('Could not copy', 'error'); }
    ta.remove();
  }

  const icons = {
    plus: '<svg viewBox="0 0 24 24"><path d="M12 5v14M5 12h14"/></svg>',
    refresh: '<svg viewBox="0 0 24 24"><path d="M20 11a8 8 0 10-2.3 5.7M20 5v6h-6"/></svg>',
    trash: '<svg viewBox="0 0 24 24"><path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6M10 11v5M14 11v5"/></svg>',
    edit: '<svg viewBox="0 0 24 24"><path d="M12 20h9M16.5 3.5a2.1 2.1 0 013 3L7 19l-4 1 1-4L16.5 3.5z"/></svg>',
    rotate: '<svg viewBox="0 0 24 24"><path d="M21 2v6h-6M3 12a9 9 0 0115-6.7L21 8M3 22v-6h6M21 12a9 9 0 01-15 6.7L3 16"/></svg>',
    copy: '<svg viewBox="0 0 24 24"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 01-2-2V4a2 2 0 012-2h9a2 2 0 012 2v1"/></svg>',
    back: '<svg viewBox="0 0 24 24"><path d="M19 12H5M12 19l-7-7 7-7"/></svg>',
    search: '<svg viewBox="0 0 24 24"><circle cx="11" cy="11" r="7"/><path d="M20 20l-4-4"/></svg>',
    external: '<svg viewBox="0 0 24 24"><path d="M14 3h7v7M10 14L21 3M21 14v5a2 2 0 01-2 2H5a2 2 0 01-2-2V5a2 2 0 012-2h5"/></svg>',
    download: '<svg viewBox="0 0 24 24"><path d="M12 3v12M7 10l5 5 5-5M4 21h16"/></svg>',
    key: '<svg viewBox="0 0 24 24"><circle cx="8" cy="15" r="4"/><path d="M11 12l8-8M16 4l4 4M14 6l4 4"/></svg>',
    alert: '<svg viewBox="0 0 24 24"><path d="M12 9v4M12 17h.01M10.3 3.9L2.4 18a2 2 0 001.7 3h15.8a2 2 0 001.7-3L13.7 3.9a2 2 0 00-3.4 0z"/></svg>',
    check: '<svg viewBox="0 0 24 24"><path d="M20 6L9 17l-5-5"/></svg>',
  };
  function icon(name) { return icons[name] || ''; }

  // ---------- api ----------
  async function api(path, opts = {}) {
    const headers = { 'X-Requested-With': CSRF };
    if (opts.body !== undefined) headers['Content-Type'] = 'application/json';
    const res = await fetch('/api' + path, {
      method: opts.method || 'GET',
      credentials: 'same-origin',
      headers,
      body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
    });
    const ct = res.headers.get('content-type') || '';
    const data = ct.includes('json') ? await res.json().catch(() => ({})) : await res.text();
    if (res.status === 401 && !opts.login) {
      showLogin();
      throw new Error('Session expired');
    }
    if (!res.ok) {
      throw new Error((data && data.error) || res.statusText || 'Request failed');
    }
    return data;
  }

  // ---------- boot ----------
  document.addEventListener('DOMContentLoaded', boot);

  async function boot() {
    bindShell();
    try {
      const sess = await fetch('/api/session', { credentials: 'same-origin' }).then((r) => r.json());
      state.session = sess;
      if (sess.authenticated) {
        state.plan = sess.plan || 'go';
        await enterApp();
      } else {
        showLogin();
        $('#login-title').textContent = sess.name || 'OpenCode Go Router';
      }
    } catch {
      showLogin();
      toast('Could not reach the router server.', 'error');
    }
  }

  function bindShell() {
    $('#login-form').addEventListener('submit', onLogin);
    $('#logout-btn').addEventListener('click', onLogout);
    window.addEventListener('hashchange', route);
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') closeModal();
    });
  }

  async function onLogin(e) {
    e.preventDefault();
    const btn = $('#login-btn');
    const err = $('#login-error');
    err.textContent = '';
    btn.disabled = true;
    btn.textContent = 'Signing in…';
    try {
      await api('/login', { method: 'POST', body: { password: $('#password').value }, login: true });
      state.session = { authenticated: true };
      $('#password').value = '';
      await enterApp();
    } catch (ex) {
      err.textContent = ex.message;
      $('#password').select();
    } finally {
      btn.disabled = false;
      btn.textContent = 'Sign in';
    }
  }

  async function onLogout() {
    try { await api('/logout', { method: 'POST' }); } catch { /* ignore */ }
    state.session = null;
    state.settings = null;
    state.models = null;
    showLogin();
    location.hash = '';
  }

  async function enterApp() {
    $('#login').hidden = true;
    $('#app').hidden = false;
    try {
      await loadSettings(true);
    } catch (ex) {
      // The dashboard can still render with defaults.
      console.warn(ex);
    }
    await route();
  }

  function showLogin() {
    $('#app').hidden = true;
    $('#login').hidden = false;
    const card = $('#login-card');
    if (card) card.classList.remove('hidden');
    setTimeout(() => $('#password')?.focus(), 30);
  }

  async function loadSettings(force = false) {
    if (!state.settings || force) {
      state.settings = await api('/settings');
      state.plan = state.settings.plan || 'go';
      $('#plan-label').textContent = planLabel(state.plan) + ' plan';
      $('#version-label').textContent = 'v' + (state.settings.version || '');
    }
    return state.settings;
  }

  async function loadModels(force = false) {
    if (!state.models || force || Date.now() - state.modelsAt > 10 * 60 * 1000) {
      const data = await api('/models');
      state.models = data.models || [];
      state.modelsAt = Date.now();
    }
    return state.models;
  }

  // ---------- routing ----------
  const pages = {
    overview: ['Overview', 'Usage'],
    keys: ['Keys', 'API keys'],
    activity: ['Activity', 'Request log'],
    models: ['Models', 'Model access & plan limits'],
    settings: ['Settings', 'Server configuration'],
  };

  async function route() {
    if (!state.session || !state.session.authenticated) return;
    const raw = location.hash.replace(/^#\/?/, '');
    const parts = raw.split('/').filter(Boolean);
    const name = parts[0] || 'overview';
    const id = parts[1] || null;
    state.route = { name, id };
    if (name === 'keys' && id) {
      setHeading('Keys', 'Key details', 'keys');
    } else if (pages[name]) {
      setHeading(pages[name][0], pages[name][1], name);
    } else {
      location.hash = '#/overview';
      return;
    }
    const view = $('#view');
    view.innerHTML = '<div class="loading-page"><div class="skeleton"></div><div class="skeleton" style="height:260px"></div><div class="skeleton" style="height:180px"></div></div>';
    $('#topbar-actions').innerHTML = '';
    try {
      if (name === 'overview') await renderOverview();
      else if (name === 'keys' && id) await renderKeyDetail(id);
      else if (name === 'keys') await renderKeys();
      else if (name === 'activity') await renderActivity();
      else if (name === 'models') await renderModels();
      else if (name === 'settings') await renderSettings();
    } catch (ex) {
      if (ex.message === 'Session expired') return;
      renderError(ex);
    }
  }

  function setHeading(crumb, title, nav) {
    $('#crumb').textContent = crumb;
    $('#page-title').textContent = title;
    $$('[data-nav]').forEach((a) => a.classList.toggle('active', a.dataset.nav === nav));
  }

  function renderError(ex) {
    $('#view').innerHTML = `<div class="card"><div class="empty">
      <div class="empty-icon">${icon('alert')}</div>
      <h3>Something went wrong</h3>
      <p>${esc(ex.message || 'Unknown error')}</p>
      <button class="btn" data-retry>Try again</button>
    </div></div>`;
    $('[data-retry]').addEventListener('click', route);
  }

  function emptyState(title, text, action = '') {
    return `<div class="empty">
      <div class="empty-icon">${icon('key')}</div>
      <h3>${esc(title)}</h3>
      <p>${esc(text)}</p>
      ${action}
    </div>`;
  }

  // ---------- overview ----------
  async function renderOverview() {
    setRangeActions();
    const data = await api('/overview?range=' + encodeURIComponent(state.range));
    const t = data.totals;
    const tokens = (t.input_tokens || 0) + (t.output_tokens || 0) + (t.cache_read_tokens || 0) + (t.cache_write_tokens || 0);
    const ranges = [['24h', '24h'], ['7d', '7d'], ['30d', '30d'], ['90d', '90d']];
    const actions = `<div class="seg">${ranges.map(([v, l]) => `<button data-range="${v}" class="${state.range === v ? 'active' : ''}">${l}</button>`).join('')}</div>
      <button class="btn icon" data-refresh title="Refresh">${icon('refresh')}</button>`;

    $('#view').innerHTML = `
      <div class="stack">
        <div class="grid stats">
          <div class="card stat-card accent">
            <div class="stat-label">Allowance used</div>
            <div class="stat-value">${fmtMoney(t.cost)}</div>
            <div class="stat-sub">${fmtNum(t.requests)} requests · ${data.range}</div>
          </div>
          <div class="card stat-card">
            <div class="stat-label">Tokens</div>
            <div class="stat-value">${fmtTokens(tokens)}</div>
            <div class="stat-sub">${fmtTokens(t.input_tokens)} in · ${fmtTokens(t.output_tokens)} out · ${fmtTokens(t.cache_read_tokens)} cached</div>
          </div>
          <div class="card stat-card">
            <div class="stat-label">API keys</div>
            <div class="stat-value">${fmtNum(data.counts.active)} <span class="faint" style="font-size:15px;font-weight:500">/ ${fmtNum(data.counts.keys)}</span></div>
            <div class="stat-sub">${data.counts.limited} at limit · ${data.counts.expiring} expiring soon</div>
          </div>
          <div class="card stat-card">
            <div class="stat-label">Avg. latency</div>
            <div class="stat-value">${Math.round(t.avg_duration_ms || 0)}<span class="faint" style="font-size:15px;font-weight:500"> ms</span></div>
            <div class="stat-sub">${fmtNum(t.errors)} errors</div>
          </div>
        </div>

        <div class="card">
          <div class="chart-head">
            <div>
              <div class="card-title">Allowance over time</div>
              <div class="card-sub">Usage counted against your Go plan</div>
            </div>
            <div class="row" style="gap:14px">
              <div class="chart-max" id="chart-max"></div>
              <div class="chart-legend"><span><i class="legend-dot"></i> Spend</span></div>
            </div>
          </div>
          ${chartHTML(data.series)}
          <div class="chart-x" id="chart-x"></div>
        </div>

        <div class="grid main-split">
          <div class="card">
            <div class="card-head">
              <div><div class="card-title">Models</div><div class="card-sub">By allowance in this range</div></div>
              <a class="card-link" href="#/models">Plan limits ${icon('external')}</a>
            </div>
            ${modelUsageList(data.models)}
          </div>
          <div class="card">
            <div class="card-head">
              <div><div class="card-title">Top keys</div><div class="card-sub">Who used the allowance</div></div>
              <a class="card-link" href="#/keys">All keys ${icon('external')}</a>
            </div>
            ${keyUsageList(data.keys)}
          </div>
        </div>
      </div>`;

    $('#topbar-actions').innerHTML = actions;
    bindRangeControls();
    bindChart();

    $$('[data-key-link]').forEach((el) => el.addEventListener('click', () => { location.hash = '#/keys/' + el.dataset.keyLink; }));
    const maxCost = Math.max(...data.series.map((p) => p.cost), 1e-9);
    $('#chart-max').textContent = 'max ' + fmtMoney(maxCost);
    const xs = data.series;
    if (xs.length) {
      $('#chart-x').innerHTML = `<span>${fmtTimeShort(xs[0].t, state.range !== '24h')}</span><span>${fmtTimeShort(xs[Math.floor(xs.length / 2)].t, state.range !== '24h')}</span><span>${fmtTimeShort(xs[xs.length - 1].t, state.range !== '24h')}</span>`;
    }
  }

  function setRangeActions() {
    const ranges = [['24h', '24h'], ['7d', '7d'], ['30d', '30d'], ['90d', '90d']];
    $('#topbar-actions').innerHTML = `<div class="seg">${ranges.map(([v, l]) => `<button data-range="${v}" class="${state.range === v ? 'active' : ''}">${l}</button>`).join('')}</div>
      <button class="btn icon" data-refresh title="Refresh">${icon('refresh')}</button>`;
    bindRangeControls();
  }

  function bindRangeControls() {
    $$('[data-range]').forEach((btn) => btn.addEventListener('click', () => {
      state.range = btn.dataset.range;
      route();
    }));
    const refresh = $('[data-refresh]');
    if (refresh) refresh.addEventListener('click', route);
  }

  function modelUsageList(models) {
    if (!models || !models.length) return emptyState('No model usage', 'Usage appears here after the first request.');
    const top = models.slice(0, 8);
    const max = Math.max(...top.map((m) => m.cost), ...top.map((m) => m.requests), 1);
    return `<div class="bar-list">${top.map((m) => {
      const val = m.cost > 0 ? m.cost : m.requests;
      const w = Math.max(2, (val / max) * 100);
      return `<div class="bar-item">
        <div style="min-width:0">
          <div class="name">${esc(m.name || modelName(m.model))}</div>
          <div class="sub">${fmtNum(m.requests)} requests · ${fmtTokens(m.tokens)} tokens</div>
          <div class="bar" style="margin-top:6px"><span style="width:${w.toFixed(1)}%"></span></div>
        </div>
        <div class="value">${fmtMoney(m.cost)}</div>
      </div>`;
    }).join('')}</div>`;
  }

  function keyUsageList(keys) {
    if (!keys || !keys.length) return emptyState('No usage yet', 'Create a key and connect a client.');
    const max = Math.max(...keys.map((k) => k.cost), 1e-9);
    return `<div class="bar-list">${keys.slice(0, 7).map((k) => {
      const w = Math.max(2, (k.cost / max) * 100);
      return `<div class="bar-item" data-key-link="${esc(k.key_id)}">
        <div style="min-width:0">
          <div class="name">${esc(k.key_name)}</div>
          <div class="sub">${fmtNum(k.requests)} requests · ${relTime(k.last_at)}</div>
          <div class="bar" style="margin-top:6px"><span style="width:${w.toFixed(1)}%"></span></div>
        </div>
        <div class="value">${fmtMoney(k.cost)}</div>
      </div>`;
    }).join('')}</div>`;
  }

  // ---------- chart ----------
  function chartHTML(points) {
    points = points || [];
    state._chartData = points;
    const id = 'g' + (++state.charts);
    const w = 1000, h = 220, top = 10, bottom = 12, pad = 2;
    const n = points.length;
    const vals = points.map((p) => Number(p.cost) || 0);
    const max = Math.max(...vals, 1e-9);
    const step = n > 1 ? (w - pad * 2) / (n - 1) : 0;
    const base = h - bottom;
    const coords = points.map((p, i) => {
      const y = base - ((Number(p.cost) || 0) / max) * (h - top - bottom);
      return [pad + i * step, y];
    });
    if (!coords.length) coords.push([pad, base]);
    const line = coords.map(([x, y], i) => `${i ? 'L' : 'M'}${x.toFixed(2)} ${y.toFixed(2)}`).join(' ');
    const area = `${line} L ${coords[coords.length - 1][0].toFixed(2)} ${base} L ${coords[0][0].toFixed(2)} ${base} Z`;
    const grid = [0.25, 0.5, 0.75, 1].map((p) => {
      const y = base - p * (h - top - bottom);
      return `<line class="grid-line" x1="0" y1="${y.toFixed(1)}" x2="${w}" y2="${y.toFixed(1)}"/>`;
    }).join('');
    const pointsMarkup = coords.map(([x, y], i) => `<circle class="point" data-i="${i}" cx="${x.toFixed(2)}" cy="${y.toFixed(2)}" r="3.5"/>`).join('');
    return `<div class="chart-wrap" data-chart>
      <svg class="chart" viewBox="0 0 ${w} ${h}" preserveAspectRatio="none" role="img" aria-label="Usage over time">
        <defs><linearGradient id="${id}" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stop-color="#c9f76f" stop-opacity=".28"/>
          <stop offset="100%" stop-color="#c9f76f" stop-opacity="0"/>
        </linearGradient></defs>
        ${grid}
        <line class="axis-line" x1="0" y1="${base}" x2="${w}" y2="${base}"/>
        <path class="area" d="${area}" fill="url(#${id})"/>
        <path class="line" d="${line}"/>
        ${pointsMarkup}
      </svg>
      <div class="chart-tooltip" data-tooltip><div class="tt-time"></div><div class="tt-value"></div></div>
    </div>`;
  }

  function bindChart() {
    $$('[data-chart]').forEach((el) => {
      const svg = $('svg', el);
      const tip = $('[data-tooltip]', el);
      const points = $('circle.point', el);
      const total = points.length;
      if (!total) return;
      const onMove = (ev) => {
        const rect = svg.getBoundingClientRect();
        const ratio = Math.max(0, Math.min(1, (ev.clientX - rect.left) / rect.width));
        const i = Math.round(ratio * (total - 1));
        const circle = points[i];
        if (!circle) return;
        points.forEach((p) => p.classList.toggle('active', p === circle));
        const x = (Number(circle.getAttribute('cx')) / 1000) * rect.width;
        const y = (Number(circle.getAttribute('cy')) / 220) * rect.height;
        tip.style.left = x + 'px';
        tip.style.top = y + 'px';
        tip.classList.add('visible');
        const data = state._chartData || [];
        const t = data[i] ? data[i].t : 0;
        const c = data[i] ? data[i].cost : 0;
        $('.tt-time', tip).textContent = t ? fmtDateTime(t) : '';
        $('.tt-value', tip).textContent = fmtMoney(c);
      };
      el.addEventListener('pointermove', onMove);
      el.addEventListener('pointerleave', () => {
        tip.classList.remove('visible');
        points.forEach((p) => p.classList.remove('active'));
      });
    });
  }

  // ---------- keys ----------
  async function renderKeys() {
    const [data, models] = await Promise.all([api('/keys'), loadModels()]);
    const keys = data.keys || [];
    const active = keys.filter((k) => k.status === 'active').length;
    const limited = keys.filter((k) => k.status === 'limited').length;

    $('#topbar-actions').innerHTML = `<button class="btn primary" data-create-key>${icon('plus')} Create key</button>`;
    $('#view').innerHTML = `<div class="stack">
      <div class="grid stats">
        <div class="card stat-card"><div class="stat-label">Total keys</div><div class="stat-value">${keys.length}</div><div class="stat-sub">Sub-keys for friends & tools</div></div>
        <div class="card stat-card"><div class="stat-label">Active</div><div class="stat-value">${active}</div><div class="stat-sub">Enabled and not expired</div></div>
        <div class="card stat-card"><div class="stat-label">At limit</div><div class="stat-value">${limited}</div><div class="stat-sub">Currently blocked by a limit</div></div>
        <div class="card stat-card"><div class="stat-label">Models available</div><div class="stat-value">${models.filter((m) => !m.deprecated).length}</div><div class="stat-sub">Across all API families</div></div>
      </div>
      <div class="card pad-0">
        <div class="row between wrap" style="padding:16px 18px;border-bottom:1px solid var(--border)">
          <div><div class="card-title">API keys</div><div class="card-sub">Each key has its own limits and model access</div></div>
          <div class="search">${icon('search')}<input type="search" placeholder="Search keys…" value="${esc(state.keySearch)}" data-key-search></div>
        </div>
        ${keys.length ? `<div class="table-wrap"><table id="keys-table">
          <thead><tr><th>Key</th><th>Status</th><th>Models</th><th>Limit</th><th class="num">5h</th><th class="num">24h</th><th class="num">7d</th><th>Last used</th><th></th></tr></thead>
          <tbody>${keys.map(keyRowHTML).join('')}</tbody>
        </table></div><div id="keys-no-match" class="empty hidden"><h3>No matching keys</h3><p>Try a different search.</p></div>` : `<div style="padding:20px">${emptyState('No keys yet', 'Create a sub-key for a friend or a tool.', '<button class="btn primary" data-create-key>Create your first key</button>')}</div>`}
      </div>
    </div>`;

    $('[data-key-search]')?.addEventListener('input', (e) => {
      state.keySearch = e.target.value;
      const q = e.target.value.trim().toLowerCase();
      let visible = 0;
      $$('#keys-table tr[data-key-row]').forEach((tr) => {
        const hit = !q || tr.dataset.search.includes(q);
        tr.classList.toggle('hidden', !hit);
        if (hit) visible++;
      });
      const none = $('#keys-no-match');
      if (none) none.classList.toggle('hidden', visible > 0);
    });
    $$('[data-create-key]').forEach((b) => b.addEventListener('click', () => openKeyModal(null)));
    $$('[data-key-open]').forEach((el) => el.addEventListener('click', () => { location.hash = '#/keys/' + el.dataset.keyOpen; }));
    $$('[data-key-action]').forEach((btn) => btn.addEventListener('click', async (e) => {
      e.stopPropagation();
      const id = btn.dataset.id;
      const key = keys.find((k) => k.id === id);
      if (!key) return;
      if (btn.dataset.keyAction === 'edit') openKeyModal(key);
      if (btn.dataset.keyAction === 'rotate') await rotateKey(key);
      if (btn.dataset.keyAction === 'delete') await deleteKey(key);
      if (btn.dataset.keyAction === 'toggle') await toggleKey(key);
    }));
  }

  function keyRowHTML(k) {
    const h5 = k.usage?.h5?.cost || 0;
    const d1 = k.usage?.d1?.cost || 0;
    const d7 = k.usage?.d7?.cost || 0;
    const lim = k.limits && k.limits.length ? k.limits[0] : null;
    const lp = lim ? pct(lim.used, lim.max) : 0;
    const limText = lim ? `${fmtLimit(lim.used, lim.metric)} / ${fmtLimit(lim.max, lim.metric)} ${windowLabel(lim.window_seconds)}` : 'No limit';
    return `<tr class="clickable" data-key-row data-search="${esc((k.name + ' ' + k.id + ' ' + k.prefix).toLowerCase())}" data-key-open="${esc(k.id)}">
      <td><div class="cell-main">${esc(k.name)}</div><div class="cell-sub mono">${esc(k.prefix)}…</div></td>
      <td>${statusBadge(k.status)}</td>
      <td>${k.allowed_models.includes('*') ? '<span class="badge">All models</span>' : `<span class="badge mono">${k.allowed_models.length} model${k.allowed_models.length === 1 ? '' : 's'}</span>`}</td>
      <td style="min-width:150px"><div class="row between" style="gap:8px"><span class="tiny muted">${limText}</span>${lim ? `<span class="tiny faint">${Math.round(lp)}%</span>` : ''}</div>${lim ? `<div class="bar ${barClass(lp)}" style="margin-top:5px"><span style="width:${lp.toFixed(1)}%"></span></div>` : ''}${k.limits.length > 1 ? `<div class="tiny faint" style="margin-top:3px">+${k.limits.length - 1} more</div>` : ''}</td>
      <td class="num">${fmtMoney(h5)}</td>
      <td class="num">${fmtMoney(d1)}</td>
      <td class="num">${fmtMoney(d7)}</td>
      <td class="muted nowrap">${relTime(k.last_used_at)}</td>
      <td><div class="row" style="gap:4px;justify-content:flex-end">
        <button class="btn icon ghost" title="Edit" data-key-action="edit" data-id="${esc(k.id)}">${icon('edit')}</button>
        <button class="btn icon ghost" title="Rotate secret" data-key-action="rotate" data-id="${esc(k.id)}">${icon('rotate')}</button>
        <button class="btn icon ghost" title="Delete" data-key-action="delete" data-id="${esc(k.id)}">${icon('trash')}</button>
      </div></td>
    </tr>`;
  }

  async function toggleKey(key) {
    try {
      await api('/keys/' + encodeURIComponent(key.id) + '/enabled', { method: 'POST', body: { enabled: !key.enabled } });
      toast(key.enabled ? 'Key disabled' : 'Key enabled', 'ok');
      route();
    } catch (ex) { toast(ex.message, 'error'); }
  }

  async function deleteKey(key) {
    const ok = await confirmModal('Delete key?', `“${key.name}” will stop working immediately. Usage history is kept.`, 'Delete key');
    if (!ok) return;
    try {
      await api('/keys/' + encodeURIComponent(key.id), { method: 'DELETE' });
      toast('Key deleted', 'ok');
      route();
    } catch (ex) { toast(ex.message, 'error'); }
  }

  async function rotateKey(key) {
    const ok = await confirmModal('Rotate secret?', `The current secret for “${key.name}” stops working immediately. The key, limits and history stay the same.`, 'Rotate secret');
    if (!ok) return;
    try {
      const res = await api('/keys/' + encodeURIComponent(key.id) + '/rotate', { method: 'POST' });
      showSecret(res.secret, key, 'Secret rotated');
      route();
    } catch (ex) { toast(ex.message, 'error'); }
  }

  // ---------- key create / edit ----------
  async function openKeyModal(key) {
    let models;
    try { models = await loadModels(); } catch (ex) { toast(ex.message, 'error'); return; }
    const usable = models.filter((m) => !m.deprecated);
    const isEdit = !!key;
    const allowedAll = !key || key.allowed_models.includes('*');
    const patterns = key ? key.allowed_models.filter((m) => m !== '*' && /[*?]/.test(m)) : [];
    const selected = new Set(key ? key.allowed_models.filter((m) => m !== '*' && !/[*?]/.test(m)) : []);
    const limits = key ? key.limits : [{ metric: 'cost', window_seconds: 86400, max: 5 }];
    const exp = key?.expires_at || 0;
    const expPreset = !exp ? 'never' : [7, 30, 90].includes(Math.round((exp - Date.now() / 1000) / 86400)) ? String(Math.round((exp - Date.now() / 1000) / 86400)) : 'custom';
    const expDate = exp ? dateInput(exp) : '';

    const body = `
      <div class="modal-section">
        <div class="modal-section-title"><h3>Identity</h3></div>
        <div class="form-row">
          <div class="field"><label for="key-name">Name</label><input id="key-name" type="text" maxlength="80" placeholder="e.g. Alex’s laptop" value="${esc(key?.name || '')}" required></div>
          <div class="field"><label for="key-expiry">Expires</label><select id="key-expiry">
            <option value="never" ${expPreset === 'never' ? 'selected' : ''}>Never</option>
            <option value="7" ${expPreset === '7' ? 'selected' : ''}>In 7 days</option>
            <option value="30" ${expPreset === '30' ? 'selected' : ''}>In 30 days</option>
            <option value="90" ${expPreset === '90' ? 'selected' : ''}>In 90 days</option>
            <option value="custom" ${expPreset === 'custom' ? 'selected' : ''}>Custom date</option>
          </select><input id="key-expiry-date" type="date" class="${expPreset === 'custom' ? '' : 'hidden'}" value="${esc(expDate)}"></div>
        </div>
        <div class="field" style="margin-top:12px"><label for="key-note">Note <span class="faint">(optional)</span></label><input id="key-note" type="text" maxlength="500" placeholder="Who is this for?" value="${esc(key?.note || '')}"></div>
      </div>

      <div class="modal-section">
        <div class="modal-section-title"><h3>Usage limits</h3><button type="button" class="btn small ghost" data-add-limit>${icon('plus')} Add limit</button></div>
        <div class="tiny faint" style="margin-bottom:8px">Rolling windows. A key is blocked when any limit is exhausted.</div>
        <div id="limit-rows">${limits.map((l) => limitRowHTML(l.metric, l.window_seconds, l.max)).join('')}</div>
        <div id="limit-empty" class="tiny faint ${limits.length ? 'hidden' : ''}" style="margin-top:6px">No limits — this key can use your full plan.</div>
      </div>

      <div class="modal-section">
        <div class="modal-section-title"><h3>Model access</h3><span class="tiny faint">${usable.length} available</span></div>
        <div class="radio-row" style="margin-bottom:10px">
          <label class="radio-card"><input type="radio" name="model-access" value="all" ${allowedAll ? 'checked' : ''}> All models</label>
          <label class="radio-card"><input type="radio" name="model-access" value="selected" ${allowedAll ? '' : 'checked'}> Selected models</label>
        </div>
        <div class="model-picker ${allowedAll ? 'disabled' : ''}" id="model-picker">
          <div class="model-picker-head"><div class="search grow">${icon('search')}<input type="search" placeholder="Filter models…" data-model-search></div><button type="button" class="btn small ghost" data-select-all>All</button><button type="button" class="btn small ghost" data-select-none>None</button></div>
          <div class="model-list">
            ${usable.map((m) => `<label class="model-row"><input type="checkbox" data-model="${esc(m.id)}" ${selected.has(m.id) ? 'checked' : ''}><span>${esc(m.name)}</span><span class="mid">${esc(m.id)}</span></label>`).join('')}
          </div>
        </div>
        <div class="field" style="margin-top:10px"><label for="key-model-rules">Advanced rules <span class="faint">(optional, comma separated; supports * and ?)</span></label><input id="key-model-rules" type="text" placeholder="e.g. kimi-*, gpt-6-*" value="${esc(patterns.join(', '))}"></div>
      </div>`;

    openModal({
      title: isEdit ? 'Edit key' : 'Create API key',
      subtitle: isEdit ? `Editing ${key.prefix}…` : 'The secret is shown once after creation.',
      body,
      wide: true,
      footer: `<button class="btn ghost" data-close>Cancel</button><button class="btn primary" id="key-save">${isEdit ? 'Save changes' : 'Create key'}</button>`,
      onMount: (modal) => {
        bindKeyForm(modal, key, isEdit);
      },
    });
  }

  function windowOptions(selected) {
    const opts = [[18000, '5 hours'], [86400, '24 hours'], [604800, '7 days'], [2592000, '30 days'], ['custom', 'Custom']];
    return opts.map(([v, l]) => `<option value="${v}" ${String(selected) === String(v) ? 'selected' : ''}>${l}</option>`).join('');
  }

  function limitRowHTML(metric = 'cost', window = 86400, max = 5) {
    const preset = [18000, 86400, 604800, 2592000].includes(Number(window));
    const custom = !preset;
    return `<div class="limit-edit" data-limit>
      <div class="field"><label>Metric</label><select data-limit-metric>
        <option value="cost" ${metric === 'cost' ? 'selected' : ''}>Spend (USD)</option>
        <option value="requests" ${metric === 'requests' ? 'selected' : ''}>Requests</option>
        <option value="tokens" ${metric === 'tokens' ? 'selected' : ''}>Tokens</option>
      </select></div>
      <div class="field"><label>Amount</label><input data-limit-max type="number" min="0" step="${metric === 'cost' ? '0.01' : metric === 'tokens' ? '1000' : '1'}" value="${esc(max)}"></div>
      <div class="field"><label>Window</label><select data-limit-window>${windowOptions(custom ? 'custom' : window)}</select></div>
      <div class="field ${custom ? '' : 'hidden'}" data-custom-window><label>Custom window</label><div class="row" style="gap:6px"><input data-limit-num type="number" min="1" value="${custom ? Math.max(1, Math.round(window / 60)) : 60}" style="width:80px"><select data-limit-unit style="width:90px"><option value="60">minutes</option><option value="3600" ${custom && window >= 3600 && window % 3600 === 0 ? 'selected' : ''}>hours</option><option value="86400" ${custom && window % 86400 === 0 ? 'selected' : ''}>days</option></select></div></div>
      <button type="button" class="btn icon ghost" data-remove-limit title="Remove">${icon('trash')}</button>
    </div>`;
  }

  function bindKeyForm(modal, key, isEdit) {
    const expiry = $('#key-expiry', modal);
    const expiryDate = $('#key-expiry-date', modal);
    expiry.addEventListener('change', () => {
      expiryDate.classList.toggle('hidden', expiry.value !== 'custom');
      if (expiry.value !== 'custom' && expiry.value !== 'never') {
        const d = new Date();
        d.setDate(d.getDate() + Number(expiry.value));
        expiryDate.value = dateInput(d.getTime() / 1000);
      }
    });

    const rows = $('#limit-rows', modal);
    $('[data-add-limit]', modal).addEventListener('click', () => {
      rows.insertAdjacentHTML('beforeend', limitRowHTML());
      bindLimitRows(modal);
      toggleLimitEmpty(modal);
    });
    bindLimitRows(modal);

    const picker = $('#model-picker', modal);
    const search = $('[data-model-search]', modal);
    search.addEventListener('input', () => {
      const q = search.value.toLowerCase();
      $$('.model-row', picker).forEach((row) => {
        row.classList.toggle('hidden', !row.textContent.toLowerCase().includes(q));
      });
    });
    const setPickerEnabled = () => {
      const all = $('input[name="model-access"]:checked', modal).value === 'all';
      picker.classList.toggle('disabled', all);
      $$('input[data-model]', picker).forEach((cb) => { cb.disabled = all; });
    };
    $$('input[name="model-access"]', modal).forEach((r) => r.addEventListener('change', setPickerEnabled));
    setPickerEnabled();
    $('[data-select-all]', modal).addEventListener('click', () => {
      $('input[name="model-access"][value="selected"]', modal).checked = true;
      setPickerEnabled();
      $$('input[data-model]', picker).forEach((cb) => { cb.checked = true; });
    });
    $('[data-select-none]', modal).addEventListener('click', () => {
      $('input[name="model-access"][value="selected"]', modal).checked = true;
      setPickerEnabled();
      $$('input[data-model]', picker).forEach((cb) => { cb.checked = false; });
    });

    $('#key-save', modal).addEventListener('click', async () => {
      const err = $('.form-error', modal);
      if (err) err.textContent = '';
      const name = $('#key-name', modal).value.trim();
      if (!name) { showFormError(modal, 'Name is required.'); return; }
      const expVal = expiry.value;
      let expires = 0;
      if (expVal === 'custom') {
        if (!expiryDate.value) { showFormError(modal, 'Choose an expiry date.'); return; }
        expires = Math.floor(new Date(expiryDate.value + 'T23:59:59').getTime() / 1000);
        if (expires <= Date.now() / 1000) { showFormError(modal, 'Expiry must be in the future.'); return; }
      } else if (expVal !== 'never') {
        expires = Math.floor(Date.now() / 1000) + Number(expVal) * 86400;
      }
      const all = $('input[name="model-access"]:checked', modal).value === 'all';
      const selected = $$('input[data-model]:checked', picker).map((cb) => cb.dataset.model);
      const rules = $('#key-model-rules', modal).value.split(',').map((s) => s.trim()).filter(Boolean);
      const allowed = all ? ['*'] : Array.from(new Set([...selected, ...rules]));
      if (!all && allowed.length === 0) { showFormError(modal, 'Select at least one model, add an advanced rule, or choose all models.'); return; }
      const limits = [];
      for (const row of $$('[data-limit]', rows)) {
        const metric = $('[data-limit-metric]', row).value;
        const max = Number($('[data-limit-max]', row).value);
        const wsel = $('[data-limit-window]', row).value;
        let win = wsel === 'custom' ? Number($('[data-limit-num]', row).value) * Number($('[data-limit-unit]', row).value) : Number(wsel);
        if (!Number.isFinite(max) || max <= 0) { showFormError(modal, 'Every limit needs an amount greater than zero.'); return; }
        if (!Number.isFinite(win) || win < 60) { showFormError(modal, 'Limit window must be at least one minute.'); return; }
        if (win > 90 * 86400) { showFormError(modal, 'Limit window cannot exceed 90 days.'); return; }
        limits.push({ metric, window_seconds: Math.round(win), max });
      }
      const payload = { name, note: $('#key-note', modal).value.trim(), expires_at: expires, allowed_models: allowed, limits };
      const btn = $('#key-save', modal);
      btn.disabled = true;
      btn.textContent = isEdit ? 'Saving…' : 'Creating…';
      try {
        if (isEdit) {
          const res = await api('/keys/' + encodeURIComponent(key.id), { method: 'PATCH', body: payload });
          closeModal();
          toast('Key updated', 'ok');
          if (state.route.name === 'keys' && state.route.id) route(); else route();
          return res;
        } else {
          const res = await api('/keys', { method: 'POST', body: payload });
          closeModal();
          showSecret(res.secret, res.key, 'Key created');
          route();
        }
      } catch (ex) {
        showFormError(modal, ex.message);
        btn.disabled = false;
        btn.textContent = isEdit ? 'Save changes' : 'Create key';
      }
    });
  }

  function toggleLimitEmpty(modal) {
    const empty = $('#limit-empty', modal);
    if (empty) empty.classList.toggle('hidden', $$('[data-limit]', modal).length > 0);
  }

  function bindLimitRows(modal) {
    $$('[data-limit]', modal).forEach((row) => {
      if (row.dataset.bound) return;
      row.dataset.bound = '1';
      $('[data-remove-limit]', row).addEventListener('click', () => {
        row.remove();
        toggleLimitEmpty(modal);
      });
      const metric = $('[data-limit-metric]', row);
      const max = $('[data-limit-max]', row);
      metric.addEventListener('change', () => {
        max.step = metric.value === 'cost' ? '0.01' : metric.value === 'tokens' ? '1000' : '1';
        max.value = metric.value === 'cost' ? '5' : metric.value === 'tokens' ? '1000000' : '100';
      });
      const win = $('[data-limit-window]', row);
      const custom = $('[data-custom-window]', row);
      win.addEventListener('change', () => custom.classList.toggle('hidden', win.value !== 'custom'));
    });
  }

  function showFormError(modal, msg) {
    let el = $('.form-error', modal);
    if (!el) {
      el = document.createElement('div');
      el.className = 'form-error';
      $('.modal-foot', modal).before(el);
    }
    el.textContent = msg;
  }

  async function openKeyConfig(key) {
    let setup;
    try { setup = await api('/setup?key=' + encodeURIComponent(key.id)); } catch (ex) { toast(ex.message, 'error'); return; }
    openModal({
      title: 'Client config for ' + key.name,
      subtitle: 'Only models allowed for this key are included.',
      wide: true,
      body: `<div class="stack">
        <div class="callout accent">${icon('check')}<div>Save this as <span class="mono">${esc(setup.config_path_hint)}</span>, then export the key and run the client.</div></div>
        <div class="field"><label>Environment</label><div class="code-block"><pre>${esc('export ' + setup.env_var + '="ocgr_..."')}</pre></div></div>
        <div class="field"><label>opencode.jsonc</label><div class="code-block"><pre>${esc(setup.opencode_jsonc)}</pre><button class="btn small copy-corner" data-copy-key-cfg>${icon('copy')}</button></div></div>
        <div class="field"><label>Try it</label><div class="code-block"><pre>${esc(setup.example_command)}</pre></div></div>
      </div>`,
      footer: `<button class="btn primary" data-close>Done</button>`,
      onMount: (modal) => {
        $('[data-copy-key-cfg]', modal).addEventListener('click', () => copyText(setup.opencode_jsonc, 'Config copied'));
      },
    });
  }

  function showSecret(secret, key, title) {
    const name = key?.name || 'this key';
    const cmd = `export OPENCODE_GO_KEY="${secret}"`;
    openModal({
      title: title || 'Your new API key',
      subtitle: `Copy it now — the secret is not stored in readable form.`,
      body: `<div class="stack">
        <div class="callout warn">${icon('alert')}<div>This secret is shown only once. Store it in a password manager or hand it to ${esc(name)} now.</div></div>
        <div class="field"><label>Secret</label><div class="secret-box"><span class="grow">${esc(secret)}</span><button class="btn small" data-copy-secret>${icon('copy')} Copy</button></div></div>
        <div class="field"><label>Client environment</label><div class="code-block"><pre>${esc(cmd)}</pre><button class="btn small copy-corner" data-copy-env>${icon('copy')}</button></div></div>
        <div class="field"><label>Client config for this key</label><div id="secret-config"><div class="skeleton" style="height:80px"></div></div></div>
        <div class="callout">${icon('key')}<div>Open <a href="#/settings" data-close style="color:var(--accent)">Settings → Client setup</a> to copy the matching <span class="mono">opencode.jsonc</span> config. Then run <span class="mono">opencode run --model go/kimi-k3 "hello"</span>.</div></div>
      </div>`,
      footer: `<button class="btn primary" data-close>Done</button>`,
      onMount: (modal) => {
        $('[data-copy-secret]', modal).addEventListener('click', () => copyText(secret, 'Secret copied'));
        $('[data-copy-env]', modal).addEventListener('click', () => copyText(cmd, 'Command copied'));
        if (key?.id) {
          api('/setup?key=' + encodeURIComponent(key.id)).then((setup) => {
            const box = $('#secret-config', modal);
            if (!box) return;
            box.innerHTML = `<div class="code-block"><pre>${esc(setup.opencode_jsonc)}</pre><button class="btn small copy-corner" data-copy-key-cfg>${icon('copy')}</button></div>`;
            $('[data-copy-key-cfg]', box).addEventListener('click', () => copyText(setup.opencode_jsonc, 'Config copied'));
          }).catch(() => {
            const box = $('#secret-config', modal);
            if (box) box.innerHTML = '<div class="tiny faint">Open Settings → Client setup to copy the config.</div>';
          });
        }
      },
    });
  }

  // ---------- key detail ----------
  async function renderKeyDetail(id) {
    const [data] = await Promise.all([api('/keys/' + encodeURIComponent(id)), loadModels()]);
    const k = data.key;
    const t = data.totals_30d;
    const isEnabled = k.enabled;
    $('#topbar-actions').innerHTML = `
      <button class="btn" data-detail-action="config">${icon('key')} Client config</button>
      <button class="btn" data-detail-action="edit">${icon('edit')} Edit</button>
      <button class="btn" data-detail-action="rotate">${icon('rotate')} Rotate</button>
      <button class="btn ${isEnabled ? '' : 'primary'}" data-detail-action="toggle">${isEnabled ? 'Disable' : 'Enable'}</button>
      <button class="btn danger" data-detail-action="delete">${icon('trash')} Delete</button>`;

    const usageCard = (label, u) => `<div class="stat-mini"><span class="k">${label}</span><span class="v">${fmtMoney(u.cost)}</span><span class="tiny faint">${fmtNum(u.requests)} req · ${fmtTokens(u.tokens)} tok</span></div>`;

    $('#view').innerHTML = `<div class="stack">
      <div>
        <a class="page-back" href="#/keys">${icon('back')} All keys</a>
        <div class="hero-row">
          <div>
            <div class="hero-title"><h2>${esc(k.name)}</h2>${statusBadge(k.status)}</div>
            <div class="hero-meta"><span class="mono">${esc(k.prefix)}…</span><span>Created ${fmtDate(k.created_at)}</span><span>Last used ${relTime(k.last_used_at)}</span>${k.expires_at ? `<span>Expires ${fmtDate(k.expires_at)}</span>` : '<span>No expiry</span>'}</div>
            ${k.note ? `<div class="muted small" style="margin-top:8px">${esc(k.note)}</div>` : ''}
          </div>
        </div>
      </div>

      <div class="card">
        <div class="grid cols-4">
          ${usageCard('5 hours', k.usage.h5)}
          ${usageCard('24 hours', k.usage.d1)}
          ${usageCard('7 days', k.usage.d7)}
          ${usageCard('30 days', k.usage.d30)}
        </div>
      </div>

      <div class="detail-grid">
        <div class="stack">
          <div class="card">
            <div class="chart-head"><div><div class="card-title">Spend over the last 7 days</div><div class="card-sub">Allowance counted against your plan</div></div><div class="chart-max">${fmtTokens((t.input_tokens || 0) + (t.output_tokens || 0) + (t.cache_read_tokens || 0) + (t.cache_write_tokens || 0))} tokens · ${fmtNum(t.requests)} requests</div></div>
            ${chartHTML(data.series || [])}
            <div class="chart-x"><span>${fmtTimeShort((data.series?.[0]?.t) || Date.now() / 1000, true)}</span><span>${fmtTimeShort((data.series?.[Math.floor((data.series?.length || 1) / 2)]?.t) || Date.now() / 1000, true)}</span><span>${fmtTimeShort((data.series?.[data.series.length - 1]?.t) || Date.now() / 1000, true)}</span></div>
          </div>
          <div class="card pad-0">
            <div class="row between" style="padding:16px 18px;border-bottom:1px solid var(--border)"><div><div class="card-title">Recent requests</div><div class="card-sub">Latest activity for this key</div></div><a class="card-link" href="#/activity">Full log ${icon('external')}</a></div>
            ${(data.recent || []).length ? `<div class="table-wrap"><table><thead><tr><th>Time</th><th>Model</th><th>Status</th><th class="num">Tokens</th><th class="num">Spend</th><th class="num">Latency</th></tr></thead><tbody>
              ${data.recent.map((r) => `<tr class="${r.status >= 400 ? 'error-row' : ''}"><td class="muted nowrap">${fmtDateTime(r.created_at)}</td><td>${esc(modelName(r.model))}</td><td>${requestStatus(r)}</td><td class="num">${fmtTokens(r.input_tokens + r.output_tokens + r.cache_read_tokens + r.cache_write_tokens)}</td><td class="num">${fmtMoney(r.cost)}</td><td class="num muted">${fmtDur(r.duration_ms)}</td></tr>`).join('')}
            </tbody></table></div>` : emptyState('No requests yet', 'This key has not been used.')}
          </div>
        </div>

        <div class="stack">
          <div class="card">
            <div class="card-head"><div><div class="card-title">Limits</div><div class="card-sub">Rolling windows, all enforced</div></div><button class="btn small ghost" data-detail-action="edit">${icon('edit')} Edit</button></div>
            ${k.limits.length ? k.limits.map((l) => {
              const p = pct(l.used, l.max);
              return `<div class="limit-row">
                <div class="limit-top"><span>${metricLabel(l.metric)} / ${windowLabel(l.window_seconds)}</span><span class="limit-value">${fmtLimit(l.used, l.metric)} / ${fmtLimit(l.max, l.metric)}</span></div>
                <div class="bar ${barClass(p)}"><span style="width:${p.toFixed(1)}%"></span></div>
                <div class="tiny faint">${p >= 100 ? `Blocked${l.reset_at ? ' · resets ' + relTime(l.reset_at) : ''}` : `${Math.round(p)}% used${l.reset_at ? ' · frees up ' + relTime(l.reset_at) : ''}`}</div>
              </div>`;
            }).join('') : '<div class="muted small">No limits configured. This key can use the full plan.</div>'}
          </div>
          <div class="card">
            <div class="card-title" style="margin-bottom:10px">Model access</div>
            ${k.allowed_models.includes('*') ? '<span class="badge accent">All models</span>' : `<div class="chips">${k.allowed_models.map((m) => `<span class="chip">${esc(modelName(m))}</span>`).join('')}</div>`}
          </div>
          <div class="card">
            <div class="card-title" style="margin-bottom:8px">Details</div>
            <div class="kv"><span class="k">Key ID</span><span class="v mono tiny">${esc(k.id)}</span></div>
            <div class="kv"><span class="k">Prefix</span><span class="v mono">${esc(k.prefix)}…</span></div>
            <div class="kv"><span class="k">Created</span><span class="v">${fmtDateTime(k.created_at)}</span></div>
            <div class="kv"><span class="k">Expires</span><span class="v">${k.expires_at ? fmtDateTime(k.expires_at) : 'Never'}</span></div>
            <div class="kv"><span class="k">Usage rows</span><span class="v">${fmtNum(t.requests)} (30d)</span></div>
          </div>
        </div>
      </div>
    </div>`;

    bindChart();
    $$('[data-detail-action]').forEach((btn) => btn.addEventListener('click', async () => {
      const action = btn.dataset.detailAction;
      if (action === 'config') openKeyConfig(k);
      if (action === 'edit') openKeyModal(k);
      if (action === 'rotate') { await rotateKey(k); }
      if (action === 'delete') {
        const ok = await confirmModal('Delete key?', `“${k.name}” will stop working immediately. Usage history is kept.`, 'Delete key');
        if (!ok) return;
        try { await api('/keys/' + encodeURIComponent(k.id), { method: 'DELETE' }); toast('Key deleted', 'ok'); location.hash = '#/keys'; } catch (ex) { toast(ex.message, 'error'); }
      }
      if (action === 'toggle') {
        try { await api('/keys/' + encodeURIComponent(k.id) + '/enabled', { method: 'POST', body: { enabled: !k.enabled } }); toast(k.enabled ? 'Key disabled' : 'Key enabled', 'ok'); route(); } catch (ex) { toast(ex.message, 'error'); }
      }
    }));
  }

  function requestStatus(r) {
    if (!r.status) return '<span class="status muted"><span class="dot"></span>pending</span>';
    if (r.status >= 500) return `<span class="status danger"><span class="dot danger"></span>${r.status}</span>`;
    if (r.status >= 400) return `<span class="status warn"><span class="dot warn"></span>${r.status}</span>`;
    return `<span class="status ok"><span class="dot ok"></span>${r.status}</span>`;
  }

  // ---------- activity ----------
  async function renderActivity() {
    const [keys, models] = await Promise.all([api('/keys'), loadModels()]);
    const a = state.activity;
    const keyOptions = (keys.keys || []).map((k) => `<option value="${esc(k.id)}" ${a.key === k.id ? 'selected' : ''}>${esc(k.name)}</option>`).join('');
    const modelOptions = models.filter((m) => !m.deprecated).map((m) => `<option value="${esc(m.id)}" ${a.model === m.id ? 'selected' : ''}>${esc(m.name)}</option>`).join('');

    $('#topbar-actions').innerHTML = `<a class="btn" href="/api/export.csv?range=30d" data-export>${icon('download')} Export CSV</a><button class="btn icon" data-refresh title="Refresh">${icon('refresh')}</button>`;
    $('#view').innerHTML = `<div class="stack">
      <div class="card">
        <div class="filter-row">
          <select data-filter-key><option value="">All keys</option>${keyOptions}</select>
          <select data-filter-model><option value="">All models</option>${modelOptions}</select>
          <select data-filter-status>
            <option value="">Any status</option>
            <option value="success" ${a.status === 'success' ? 'selected' : ''}>Successful</option>
            <option value="error" ${a.status === 'error' ? 'selected' : ''}>Errors</option>
          </select>
          <button class="btn" data-filter-apply>Apply</button>
          <button class="btn ghost" data-filter-reset>Reset</button>
        </div>
      </div>
      <div class="card pad-0">
        <div class="table-wrap">
          <table>
            <thead><tr><th>Time</th><th>Key / session</th><th>Model</th><th>Status</th><th class="num">Tokens</th><th class="num">Spend</th><th class="num">Latency</th></tr></thead>
            <tbody id="activity-body"></tbody>
          </table>
        </div>
        <div class="row between" style="padding:12px 16px;border-top:1px solid var(--border)">
          <span class="tiny faint" id="activity-count"></span>
          <button class="btn small" id="activity-more">Load more</button>
        </div>
      </div>
    </div>`;

    $('[data-filter-key]').value = a.key;
    $('[data-filter-model]').value = a.model;
    $('[data-filter-status]').value = a.status;
    $('[data-filter-apply]').addEventListener('click', () => {
      a.key = $('[data-filter-key]').value;
      a.model = $('[data-filter-model]').value;
      a.status = $('[data-filter-status]').value;
      a.rows = []; a.next = 0; a.loaded = false;
      loadActivity().catch((ex) => toast(ex.message, 'error'));
    });
    $('[data-filter-reset]').addEventListener('click', () => {
      state.activity = { key: '', model: '', status: '', rows: [], next: 0, loaded: false };
      renderActivity().catch(renderError);
    });
    $('[data-refresh]').addEventListener('click', () => { a.rows = []; a.next = 0; a.loaded = false; loadActivity().catch((ex) => toast(ex.message, 'error')); });
    const exportLink = $('[data-export]');
    if (exportLink) exportLink.addEventListener('click', (e) => {
      e.preventDefault();
      window.location.href = '/api/export.csv?range=' + encodeURIComponent(state.range);
    });
    $('#activity-more').addEventListener('click', () => loadActivity().catch((ex) => toast(ex.message, 'error')));
    if (!a.loaded) { a.rows = []; a.next = 0; await loadActivity(); } else renderActivityRows();
  }

  async function loadActivity() {
    const a = state.activity;
    const params = new URLSearchParams({ limit: '50' });
    if (a.key) params.set('key', a.key);
    if (a.model) params.set('model', a.model);
    if (a.status) params.set('status', a.status);
    if (a.next) params.set('before', String(a.next));
    const data = await api('/requests?' + params.toString());
    if (a.next) a.rows = a.rows.concat(data.requests || []);
    else a.rows = data.requests || [];
    a.next = data.next_before || 0;
    a.loaded = true;
    renderActivityRows();
  }

  function renderActivityRows() {
    const a = state.activity;
    const body = $('#activity-body');
    if (!body) return;
    if (!a.rows.length) {
      body.innerHTML = `<tr><td colspan="7"><div class="empty"><div class="empty-icon">${icon('key')}</div><h3>No requests found</h3><p>Activity shows up here as soon as a key is used.</p></div></td></tr>`;
    } else {
      body.innerHTML = a.rows.map((r) => {
        const tokens = (r.input_tokens || 0) + (r.output_tokens || 0) + (r.cache_read_tokens || 0) + (r.cache_write_tokens || 0);
        return `<tr class="${r.status >= 400 ? 'error-row' : ''}" ${r.error ? `title="${esc(r.error)}"` : ''}>
          <td class="muted nowrap">${fmtDateTime(r.created_at)}</td>
          <td><span class="cell-main">${esc(r.key_name || 'Deleted key')}</span><div class="cell-sub mono">${esc((r.session_id || r.endpoint || '').slice(0, 26))}</div></td>
          <td>${esc(modelName(r.model))}</td>
          <td>${requestStatus(r)}</td>
          <td class="num"><span class="cell-main">${fmtTokens(tokens)}</span><div class="cell-sub">${fmtTokens(r.input_tokens)} in / ${fmtTokens(r.output_tokens)} out</div></td>
          <td class="num">${fmtMoney(r.cost)}</td>
          <td class="num muted">${fmtDur(r.duration_ms)}</td>
        </tr>`;
      }).join('');
    }
    const count = $('#activity-count');
    if (count) count.textContent = `${a.rows.length} loaded${tokensTotal(a.rows) ? ' · ' + fmtTokens(tokensTotal(a.rows)) + ' tokens' : ''}`;
    const more = $('#activity-more');
    if (more) more.classList.toggle('hidden', !a.next);
  }

  function tokensTotal(rows) {
    return rows.reduce((s, r) => s + (r.input_tokens || 0) + (r.output_tokens || 0) + (r.cache_read_tokens || 0) + (r.cache_write_tokens || 0), 0);
  }

  // ---------- models / plan ----------
  async function renderModels() {
    const [modelsData, planData] = await Promise.all([api('/models'), api('/plan?plan=' + encodeURIComponent(state.plan))]);
    const models = modelsData.models || [];
    const byId = new Map(models.map((m) => [m.id, m]));
    const rows = planData.models || [];
    const totalPct = planData.totals.limit ? (planData.totals.used / planData.totals.limit) * 100 : 0;

    $('#topbar-actions').innerHTML = `
      <div class="seg"><button data-plan="go" class="${state.plan === 'go' ? 'active' : ''}">Go</button><button data-plan="go-plus" class="${state.plan === 'go-plus' ? 'active' : ''}">Go Plus</button></div>
      <button class="btn icon" data-refresh-models title="Refresh model list">${icon('refresh')}</button>`;

    $('#view').innerHTML = `<div class="stack">
      <div class="grid stats">
        <div class="card stat-card accent"><div class="stat-label">Plan allowance used</div><div class="stat-value">${fmtMoney(planData.totals.used)}</div><div class="stat-sub">of ${fmtMoney(planData.totals.limit)} summed monthly limits</div></div>
        <div class="card stat-card"><div class="stat-label">Plan</div><div class="stat-value">${planLabel(planData.plan)}</div><div class="stat-sub">Limits are tracked per model</div></div>
        <div class="card stat-card"><div class="stat-label">Models</div><div class="stat-value">${models.filter((m) => !m.deprecated).length}</div><div class="stat-sub">${rows.length} with a plan limit</div></div>
        <div class="card stat-card"><div class="stat-label">Catalog refreshed</div><div class="stat-value" style="font-size:17px">${modelsData.fetched_at ? relTime(modelsData.fetched_at) : 'snapshot'}</div><div class="stat-sub">${modelsData.error ? 'Live refresh failed — using cached list' : 'Merged with the live upstream list'}</div></div>
      </div>

      <div class="card">
        <div class="card-head"><div><div class="card-title">How plan limits work</div><div class="card-sub">OpenCode Go tracks every model separately</div></div></div>
        <div class="grid cols-3">
          <div class="stat-mini"><span class="k">5 hours</span><span class="v">20%</span><span class="tiny faint">of the model's monthly limit</span></div>
          <div class="stat-mini"><span class="k">1 week</span><span class="v">50%</span><span class="tiny faint">of the model's monthly limit</span></div>
          <div class="stat-mini"><span class="k">1 month</span><span class="v">100%</span><span class="tiny faint">of the model's monthly limit</span></div>
        </div>
        <div class="tiny faint" style="margin-top:10px">These bars show your recent usage against the plan limits; they are not the limits you set on sub-keys. Summing monthly limits is only a rough overview because each model has its own bucket.</div>
      </div>

      <div class="card pad-0">
        <div class="row between wrap" style="padding:16px 18px;border-bottom:1px solid var(--border)">
          <div><div class="card-title">Plan usage by model</div><div class="card-sub">Rolling 5h / 7d / 30d windows</div></div>
          <div class="search">${icon('search')}<input type="search" placeholder="Filter models…" data-model-filter></div>
        </div>
        <div class="table-wrap"><table>
          <thead><tr><th>Model</th><th>Family</th><th class="num">Context</th><th class="num">Price / 1M</th><th>5h</th><th>7d</th><th>30d</th></tr></thead>
          <tbody id="plan-body">
            ${rows.map((r) => planRowHTML(r, byId.get(r.id))).join('')}
          </tbody>
        </table></div>
      </div>

      <div class="card pad-0">
        <div class="row between wrap" style="padding:16px 18px;border-bottom:1px solid var(--border)"><div><div class="card-title">All models</div><div class="card-sub">Available through the router</div></div></div>
        <div class="table-wrap"><table>
          <thead><tr><th>Model</th><th>API family</th><th class="num">Context</th><th class="num">Input</th><th class="num">Output</th><th>Status</th></tr></thead>
          <tbody>
            ${models.filter((m) => !m.deprecated).map((m) => `<tr>
              <td><div class="cell-main">${esc(m.name)}</div><div class="cell-sub mono">${esc(m.id)}</div></td>
              <td><span class="badge ${m.api_family === 'messages' ? 'violet' : m.api_family === 'google' ? 'warn' : m.api_family === 'responses' ? 'accent' : ''}">${esc(familyLabel(m.api_family))}</span></td>
              <td class="num">${fmtTokens(m.context)}</td>
              <td class="num">${fmtMoneyExact(m.cost.input)}</td>
              <td class="num">${fmtMoneyExact(m.cost.output)}</td>
              <td>${m.live ? '<span class="badge ok">live</span>' : '<span class="badge">snapshot</span>'}</td>
            </tr>`).join('')}
          </tbody>
        </table></div>
      </div>
    </div>`;

    bindChart();
    $$('[data-plan]').forEach((btn) => btn.addEventListener('click', async () => {
      state.plan = btn.dataset.plan;
      try {
        await api('/settings', { method: 'PATCH', body: { plan: state.plan } });
        await loadSettings(true);
        route();
      } catch (ex) { toast(ex.message, 'error'); }
    }));
    $('[data-refresh-models]').addEventListener('click', async () => {
      try { await api('/models/refresh', { method: 'POST' }); state.models = null; state.modelsAt = 0; toast('Model list refreshed', 'ok'); route(); } catch (ex) { toast(ex.message, 'error'); }
    });
    const filter = $('[data-model-filter]');
    filter.addEventListener('input', () => {
      const q = filter.value.toLowerCase();
      $$('#plan-body tr').forEach((tr) => tr.classList.toggle('hidden', !tr.textContent.toLowerCase().includes(q)));
    });
    const maxp = Math.max(totalPct, 0);
    if (maxp > 100) toast('Summed plan usage exceeds 100% because limits are per model.', 'warn');
  }

  function familyLabel(f) {
    return f === 'messages' ? 'Anthropic' : f === 'google' ? 'Gemini' : f === 'responses' ? 'Responses' : 'Chat';
  }

  function planRowHTML(r, m) {
    const bars = [['5h', r.h5], ['7d', r.d7], ['30d', r.d30]];
    return `<tr>
      <td><div class="cell-main">${esc(r.name)}</div><div class="cell-sub mono">${esc(r.id)}</div></td>
      <td>${m ? `<span class="badge ${m.api_family === 'messages' ? 'violet' : m.api_family === 'google' ? 'warn' : m.api_family === 'responses' ? 'accent' : ''}">${esc(familyLabel(m.api_family))}</span>` : '—'}</td>
      <td class="num">${m ? fmtTokens(m.context) : '—'}</td>
      <td class="num">${m ? `<span class="tiny muted">${fmtMoneyExact(m.cost.input)} / ${fmtMoneyExact(m.cost.output)}</span>` : '—'}</td>
      ${bars.map(([label, b]) => {
        const p = pct(b.used, b.limit);
        return `<td style="min-width:130px"><div class="row between" style="gap:8px"><span class="tiny faint">${fmtMoney(b.used)} / ${fmtMoney(b.limit)}</span><span class="tiny ${p >= 80 ? 'color-warn' : 'faint'}">${Math.round(p)}%</span></div><div class="bar ${barClass(p)}" style="margin-top:5px"><span style="width:${p.toFixed(1)}%"></span></div></td>`;
      }).join('')}
    </tr>`;
  }

  // ---------- settings ----------
  async function renderSettings() {
    const [settings, setup] = await Promise.all([api('/settings'), api('/setup')]);
    state.settings = settings;
    state.setup = setup;
    state.plan = settings.plan;
    $('#plan-label').textContent = planLabel(settings.plan) + ' plan';
    $('#version-label').textContent = 'v' + (settings.version || '');
    $('#topbar-actions').innerHTML = `<button class="btn icon" data-refresh title="Refresh">${icon('refresh')}</button>`;

    const upstream = settings.upstream || {};
    const cfg = setup.opencode_jsonc || '';
    $('#view').innerHTML = `<div class="stack">
      <div class="grid cols-2">
        <div class="card">
          <div class="card-head"><div><div class="card-title">Instance</div><div class="card-sub">How the dashboard presents itself</div></div></div>
          <form id="instance-form" class="stack" style="gap:12px">
            <div class="field"><label for="instance-name">Dashboard name</label><input id="instance-name" type="text" maxlength="60" value="${esc(settings.name)}"></div>
            <div class="form-row">
              <div class="field"><label for="instance-plan">Plan</label><select id="instance-plan"><option value="go" ${settings.plan === 'go' ? 'selected' : ''}>Go</option><option value="go-plus" ${settings.plan === 'go-plus' ? 'selected' : ''}>Go Plus</option></select></div>
              <div class="field"><label for="instance-retention">Request retention (days)</label><input id="instance-retention" type="number" min="1" max="3650" value="${settings.retention_days}"><span class="hint">Logs are kept at least as long as the longest key limit window (max 90 days).</span></div>
            </div>
            <div class="form-actions" style="margin-top:4px"><button class="btn primary" type="submit">Save</button></div>
          </form>
        </div>

        <div class="card">
          <div class="card-head"><div><div class="card-title">Upstream</div><div class="card-sub">Your OpenCode Go connection</div></div><span class="badge ${upstream.configured ? 'ok' : 'danger'}">${upstream.configured ? 'configured' : 'missing key'}</span></div>
          <div class="kv"><span class="k">Base URL</span><span class="v mono tiny">${esc(upstream.base_url || '—')}</span></div>
          <div class="kv"><span class="k">API key</span><span class="v">${upstream.configured ? 'Set via environment' : 'Not set'}</span></div>
          <div class="kv"><span class="k">Models refreshed</span><span class="v">${upstream.fetched_at ? relTime(upstream.fetched_at) : 'Not yet'}</span></div>
          <div class="kv"><span class="k">Public URL</span><span class="v mono tiny">${esc(settings.public_url)}</span></div>
          <div class="kv"><span class="k">Trust proxy headers</span><span class="v">${settings.trust_proxy ? 'Yes' : 'No'}</span></div>
          ${upstream.error ? `<div class="callout danger" style="margin-top:10px">${icon('alert')}<div>${esc(upstream.error)}</div></div>` : ''}
          <div class="form-actions"><button class="btn" data-refresh-models>${icon('refresh')} Refresh models</button></div>
        </div>
      </div>

      <div class="card">
        <div class="card-head"><div><div class="card-title">Admin password</div><div class="card-sub">Sign in password for this dashboard</div></div></div>
        <form id="password-form" class="stack" style="gap:12px">
          <div class="form-row three">
            <div class="field"><label for="pw-current">Current password</label><input id="pw-current" type="password" autocomplete="current-password"></div>
            <div class="field"><label for="pw-new">New password</label><input id="pw-new" type="password" autocomplete="new-password" minlength="12"></div>
            <div class="field"><label for="pw-confirm">Confirm new password</label><input id="pw-confirm" type="password" autocomplete="new-password" minlength="12"></div>
          </div>
          <div class="tiny faint">At least 12 characters. Other sessions are signed out after a change.</div>
          <div class="form-actions" style="margin-top:4px"><button class="btn" type="submit">Change password</button></div>
        </form>
      </div>

      <div class="card">
        <div class="card-head"><div><div class="card-title">Client setup</div><div class="card-sub">Official opencode v2 client</div></div><button class="btn small" data-copy-config>${icon('copy')} Copy config</button></div>
        <div class="stack" style="gap:10px">
          <div class="callout accent">${icon('check')}<div>This provider config is generated for <span class="mono">${esc(settings.public_url)}</span>. Place it at <span class="mono">${esc(setup.config_path_hint)}</span>, export your key, then run the client.</div></div>
          <div class="form-row">
            <div class="field"><label>Install</label><div class="code-block"><pre>${esc(setup.install_command)}</pre></div></div>
            <div class="field"><label>Environment</label><div class="code-block"><pre>${esc('export ' + setup.env_var + '="ocgr_..."')}</pre></div></div>
          </div>
          <div class="field"><label>opencode.jsonc</label><div class="code-block"><pre>${esc(cfg)}</pre><button class="btn small copy-corner" data-copy-config>${icon('copy')}</button></div></div>
          <div class="field"><label>Try it</label><div class="code-block"><pre>${esc(setup.example_command)}</pre></div></div>
        </div>
      </div>

      <div class="grid cols-2">
        <div class="card">
          <div class="card-head"><div><div class="card-title">Export activity</div><div class="card-sub">CSV for spreadsheets or accounting</div></div></div>
          <div class="row wrap">
            <a class="btn" href="/api/export.csv?range=24h">${icon('download')} Last 24h</a>
            <a class="btn" href="/api/export.csv?range=7d">${icon('download')} Last 7d</a>
            <a class="btn" href="/api/export.csv?range=30d">${icon('download')} Last 30d</a>
          </div>
        </div>
        <div class="card">
          <div class="card-head"><div><div class="card-title">About</div><div class="card-sub">Server details</div></div></div>
          <div class="kv"><span class="k">Version</span><span class="v mono">${esc(settings.version)}</span></div>
          <div class="kv"><span class="k">Session lifetime</span><span class="v">${settings.session_ttl_hours} hours</span></div>
          <div class="kv"><span class="k">Retention</span><span class="v">${settings.retention_days} days</span></div>
          <div class="kv"><span class="k">Models available</span><span class="v">${setup.model_count}</span></div>
        </div>
      </div>
    </div>`;

    $('#instance-form').addEventListener('submit', async (e) => {
      e.preventDefault();
      try {
        await api('/settings', { method: 'PATCH', body: { name: $('#instance-name').value.trim(), plan: $('#instance-plan').value, retention_days: Number($('#instance-retention').value) } });
        toast('Settings saved', 'ok');
        await loadSettings(true);
        route();
      } catch (ex) { toast(ex.message, 'error'); }
    });

    $('#password-form').addEventListener('submit', async (e) => {
      e.preventDefault();
      const current = $('#pw-current').value;
      const next = $('#pw-new').value;
      const confirm = $('#pw-confirm').value;
      if (next !== confirm) { toast('New passwords do not match', 'error'); return; }
      if (next.length < 12) { toast('New password must be at least 12 characters', 'error'); return; }
      try {
        await api('/password', { method: 'POST', body: { current, new: next } });
        $('#password-form').reset();
        toast('Password changed', 'ok');
      } catch (ex) { toast(ex.message, 'error'); }
    });

    $('[data-refresh-models]')?.addEventListener('click', async () => {
      try { await api('/models/refresh', { method: 'POST' }); state.models = null; state.modelsAt = 0; toast('Model list refreshed', 'ok'); route(); } catch (ex) { toast(ex.message, 'error'); }
    });
    $$('[data-copy-config]').forEach((b) => b.addEventListener('click', () => copyText(cfg, 'Config copied')));
    $('[data-refresh]')?.addEventListener('click', route);
  }

  // ---------- modal ----------
  function openModal({ title, subtitle = '', body, footer = '', wide = false, narrow = false, onMount = null }) {
    const root = $('#modal-root');
    root.innerHTML = `<div class="modal-backdrop" data-close>
      <div class="modal ${wide ? 'wide' : narrow ? 'narrow' : ''}" role="dialog" aria-modal="true" aria-label="${esc(title)}">
        <div class="modal-head"><div><h2>${esc(title)}</h2>${subtitle ? `<p>${esc(subtitle)}</p>` : ''}</div><button class="modal-close" data-close aria-label="Close">✕</button></div>
        <div class="modal-body">${body}</div>
        ${footer ? `<div class="modal-foot">${footer}</div>` : ''}
      </div>
    </div>`;
    document.body.classList.add('modal-open');
    const backdrop = $('.modal-backdrop', root);
    backdrop.addEventListener('click', (e) => { if (e.target === backdrop) closeModal(); });
    $$('[data-close]', root).forEach((el) => el.addEventListener('click', (e) => { e.preventDefault(); closeModal(); }));
    const modal = $('.modal', root);
    modal.addEventListener('click', (e) => e.stopPropagation());
    setTimeout(() => { const focus = $('input:not([type=hidden]), select, textarea, button', modal); focus?.focus(); }, 40);
    if (onMount) onMount(modal);
  }

  function closeModal() {
    const root = $('#modal-root');
    root.innerHTML = '';
    document.body.classList.remove('modal-open');
  }

  function confirmModal(title, text, confirmLabel) {
    return new Promise((resolve) => {
      openModal({
        title,
        subtitle: '',
        narrow: true,
        body: `<div class="muted" style="font-size:13px;line-height:1.6">${esc(text)}</div>`,
        footer: `<button class="btn ghost" data-cancel>Cancel</button><button class="btn danger" data-confirm>${esc(confirmLabel)}</button>`,
        onMount: (modal) => {
          $('[data-cancel]', modal).addEventListener('click', () => { closeModal(); resolve(false); });
          $('[data-confirm]', modal).addEventListener('click', () => { closeModal(); resolve(true); });
        },
      });
    });
  }

  // ---------- toasts ----------
  function toast(message, type = 'info') {
    const root = $('#toasts');
    const el = document.createElement('div');
    el.className = 'toast ' + type;
    el.innerHTML = `<span class="dot ${type === 'ok' ? 'ok' : type === 'error' ? 'danger' : type === 'warn' ? 'warn' : ''}"></span><span>${esc(message)}</span>`;
    root.appendChild(el);
    setTimeout(() => { el.style.opacity = '0'; el.style.transform = 'translateY(6px)'; el.style.transition = 'all .2s'; setTimeout(() => el.remove(), 220); }, 4200);
  }
})();
