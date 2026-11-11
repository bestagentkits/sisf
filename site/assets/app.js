/* SISF landing page behaviour. Progressive enhancement only: every section is
   complete without this file. No inline styles are written; the drawing is
   driven through SVG attributes and classes so a strict CSP stays possible. */
(function () {
  'use strict';
  var root = document.documentElement;
  var reduce = matchMedia('(prefers-reduced-motion: reduce)');
  function $(sel, ctx) { return (ctx || document).querySelector(sel); }
  function $$(sel, ctx) { return Array.prototype.slice.call((ctx || document).querySelectorAll(sel)); }
  function onMQ(mq, fn) { if (mq.addEventListener) mq.addEventListener('change', fn); else if (mq.addListener) mq.addListener(fn); }

  /* ---------------- Theme: follows the system until the visitor picks one ---------------- */
  (function theme() {
    var btn = $('[data-theme-toggle]');
    if (!btn) return;
    var sysDark = matchMedia('(prefers-color-scheme: dark)');
    var metas = $$('meta[name="theme-color"]');
    var COLORS = { light: '#F3F6F9', dark: '#0A2240' };
    function current() { return root.getAttribute('data-theme') || (sysDark.matches ? 'dark' : 'light'); }
    function sync() {
      var t = current();
      btn.setAttribute('aria-label', t === 'dark' ? 'Switch to light theme' : 'Switch to dark theme');
      /* an explicit choice also recolours the browser chrome */
      if (root.hasAttribute('data-theme')) metas.forEach(function (m) { m.setAttribute('content', COLORS[t]); });
    }
    btn.addEventListener('click', function () {
      var next = current() === 'dark' ? 'light' : 'dark';
      root.setAttribute('data-theme', next);
      try { localStorage.setItem('sisf-theme', next); } catch (e) { /* storage blocked: theme still applies for this visit */ }
      sync();
    });
    onMQ(sysDark, sync);
    sync();
  })();

  /* ---------------- Mobile menu: a native <details>; JS adds Escape, outside click, link click ---------------- */
  (function menu() {
    var det = $('details.menu');
    if (!det) return;
    var sum = $('summary', det);
    function close(focus) { if (det.open) { det.open = false; if (focus) sum.focus(); } }
    document.addEventListener('keydown', function (e) { if (e.key === 'Escape') close(det.contains(document.activeElement)); });
    document.addEventListener('click', function (e) { if (!det.contains(e.target)) close(false); });
    det.addEventListener('click', function (e) { if (e.target.closest && e.target.closest('.menu-panel a')) close(false); });
  })();

  /* ---------------- Planned controls (Deploy on TOSE): not links; a press points at the explanation ---------------- */
  $$('[data-tose-deploy]').forEach(function (el) {
    if (el.tagName !== 'BUTTON') return;
    el.addEventListener('click', function (e) {
      e.preventDefault();
      var note = document.getElementById(el.getAttribute('aria-describedby') || '');
      if (!note || reduce.matches) return;
      note.classList.remove('flash');
      void note.offsetWidth; /* restart the CSS animation */
      note.classList.add('flash');
    });
  });

  /* ---------------- Copy the one working command ---------------- */
  $$('[data-copy]').forEach(function (btn) {
    var src = document.getElementById(btn.getAttribute('data-copy'));
    var status = btn.parentNode.parentNode.querySelector('[data-copy-status]');
    var timer = 0;
    function report(ok) {
      btn.textContent = ok ? 'Copied' : 'Copy';
      if (status) status.textContent = ok ? 'Copied to the clipboard.' : 'Copy failed. The command is selected; press Ctrl+C or Cmd+C.';
      if (!ok) { var r = document.createRange(); r.selectNodeContents(src); var s = getSelection(); s.removeAllRanges(); s.addRange(r); }
      clearTimeout(timer);
      timer = setTimeout(function () { btn.textContent = 'Copy'; if (status) status.textContent = ''; }, 2400);
    }
    function legacy(text) {
      var ta = document.createElement('textarea');
      ta.value = text; ta.setAttribute('readonly', ''); ta.className = 'vh';
      document.body.appendChild(ta); ta.select();
      var ok = false;
      try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
      document.body.removeChild(ta);
      return ok;
    }
    btn.addEventListener('click', function () {
      var text = src.textContent.trim();
      if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(text).then(function () { report(true); }, function () { report(legacy(text)); });
      } else report(legacy(text));
    });
  });

  /* ---------------- Seven rules: Expand all / Collapse all ---------------- */
  (function rules() {
    var btn = $('[data-expand-all]');
    var items = $$('#rules details');
    if (!btn || !items.length) return;
    function allOpen() { return items.every(function (d) { return d.open; }); }
    function sync() { btn.textContent = allOpen() ? 'Collapse all' : 'Expand all'; }
    btn.addEventListener('click', function () { var open = !allOpen(); items.forEach(function (d) { d.open = open; }); sync(); });
    items.forEach(function (d) { d.addEventListener('toggle', sync); });
    sync();
  })();

  /* ================================================================
     How it works: the loop. Three tiers, recorded on <html data-how>:
       3d      Three.js scene     (JS, a usable WebGL context, the module loads)
       2d      isometric drawing  (JS but no usable WebGL, or the scene failed)
       static  the drawing as an <img> plus every flow as text (no JS, no
               module support, or both tiers failed)
     Nothing heavy is fetched until the box is about 200px from the
     viewport, and never before the page has finished loading. Until a
     tier is ready the box shows the static drawing ("wait"), at the
     same size, so nothing moves when the scene arrives.
     Testing: ?scene=3d|2d|static forces a tier (3d skips the GPU check).
     ================================================================ */
  (function howItWorks() {
    var host = $('[data-loop]');
    if (!host) return;
    var box = $('[data-scene]', host);
    var V = '20261111';
    var m = /[?&]scene=(3d|2d|static)\b/.exec(location.search);
    var force = m ? m[1] : '';
    var modern = 'noModule' in HTMLScriptElement.prototype && 'IntersectionObserver' in window && 'fetch' in window;
    function set(mode) { root.setAttribute('data-how', mode); }
    if (force === 'static' || !modern) { set('static'); return; }
    set('wait');

    var tried = {};
    function load(tier) {
      if (tried[tier]) { set('static'); return; }
      tried[tier] = true;
      var s = document.createElement('script');
      s.type = 'module';
      s.src = '/assets/how-it-works-' + (tier === '3d' ? 'scene' : 'isometric') + '.js?v=' + V;
      /* a module or one of its imports failed to download or parse */
      s.onerror = function () { fallback(tier); };
      document.head.appendChild(s);
    }
    /* 3d falls back to 2d, 2d to static */
    function fallback(from) { if (from === '3d') load('2d'); else set('static'); }
    /* the modules raise this when they start but cannot run (no context, a lost context, a failed fetch) */
    document.addEventListener('sisf:how-fallback', function (e) { fallback(e.detail && e.detail.from); });

    /* a real GPU: software-only WebGL gets the lighter drawing instead of a slow scene.
       The caveat flag alone is not enough: some Chromium builds hand out SwiftShader with it set,
       and the scene's shaders can then hold the main thread for many seconds. So the renderer
       name is checked too: Firefox gives the real one as RENDERER, Chromium only through the
       debug-info extension (asked for only when RENDERER is the generic one). */
    var SOFT = /swiftshader|llvmpipe|softpipe|software|basic render/i;
    function gpu() {
      try {
        var c = document.createElement('canvas');
        var o = { failIfMajorPerformanceCaveat: true };
        var gl = c.getContext('webgl2', o) || c.getContext('webgl', o);
        if (!gl) return false;
        var name = String(gl.getParameter(gl.RENDERER) || '');
        if (/^webkit webgl$/i.test(name)) {
          var info = gl.getExtension('WEBGL_debug_renderer_info');
          if (info) name = String(gl.getParameter(info.UNMASKED_RENDERER_WEBGL) || '');
        }
        var lose = gl.getExtension('WEBGL_lose_context');
        if (lose) lose.loseContext();
        return !SOFT.test(name);
      } catch (e) { return false; }
    }
    function go() { load(force === '2d' ? '2d' : force === '3d' || gpu() ? '3d' : '2d'); }

    function arm() {
      var io = new IntersectionObserver(function (entries) {
        for (var i = 0; i < entries.length; i++) {
          if (entries[i].isIntersecting) { io.disconnect(); go(); return; }
        }
      }, { rootMargin: '200px 0px' });
      io.observe(box);
    }
    if (document.readyState === 'complete') arm();
    else window.addEventListener('load', arm);
  })();

  /* ================================================================
     Run the line. One issue travels through 15 lifecycle states.
     The <ol class="states"> in the page is the single source of the
     zone and note for each state; only two extra frames live here.
     Both drawings (wide and tall) are driven together, so a resize
     mid-run never desynchronises them.
     ================================================================ */
  var stage = $('.line-stage');
  var figure = $('.line');
  if (!stage || !figure) return;

  var STATE = {};
  $$('.states li[data-state]').forEach(function (li) {
    STATE[li.getAttribute('data-state')] = {
      zone: ($('.s-zone', li) || {}).textContent || '',
      note: ($('.s-note', li) || {}).textContent || '',
      li: li
    };
  });
  STATE.gh = { name: 'Issue opened', zone: 'GitHub · outside the factory', note: 'Someone opens an issue and adds the sisf:ready label. The label asks for work; it grants no permission.' };
  STATE.CanaryFault = { name: 'Canary', zone: 'Z1 · Application', note: 'At the 5%* step the error rate crosses the threshold, for example 5xx > 2%* above baseline. The supervisor stops the release here.' };

  var HEAD = [
    { st: 'open', state: 'gh' },
    { st: 'intake', state: 'Intake' }, { st: 'intake', state: 'Clarifying' }, { st: 'intake', state: 'ScopeDraft' },
    { st: 'approve', state: 'ApprovalPending' }, { st: 'approve', state: 'Ready' },
    { st: 'agent', state: 'Claimed' }, { st: 'agent', state: 'Developing' },
    { st: 'qa', state: 'QAEvaluation' },
    { st: 'sup', state: 'ReleaseCandidate', fx: 'receipt' }
  ];
  var OK_TAIL = [
    { st: 'canary', state: 'Canary', fx: 'canary' },
    { st: 'monitor', state: 'Monitoring' }, { st: 'monitor', state: 'Verified' },
    { st: 'closed', state: 'Closed', fx: 'closed' }
  ];
  var FAULT_TAIL = [
    { st: 'canary', state: 'CanaryFault', fx: 'fault' },
    { st: 'rb', state: 'RolledBack', fx: 'rollback' },
    { st: 'rb', state: 'Blocked', fx: 'blocked' }
  ];
  var DWELL = { gh: 1000, Canary: 1800, CanaryFault: 1500, Closed: 1600, RolledBack: 1400, Blocked: 2000 };
  var DWELL_DEFAULT = 1150, END_PAUSE = 1400, INTRO = 1700;

  /* ---- geometry: token position from polyline points and cumulative lengths ---- */
  function pts(s) { return s.trim().split(/\s+/).map(function (p) { return p.split(',').map(Number); }); }
  function cumul(a) { var c = [0]; for (var i = 1; i < a.length; i++) c.push(c[i - 1] + Math.hypot(a[i][0] - a[i - 1][0], a[i][1] - a[i - 1][1])); return c; }
  function at(a, c, d) {
    var T = c[c.length - 1];
    if (d <= 0) return a[0];
    if (d >= T) return a[a.length - 1];
    var i = 1; while (c[i] < d) i++;
    var t = (d - c[i - 1]) / (c[i] - c[i - 1]);
    return [a[i - 1][0] + (a[i][0] - a[i - 1][0]) * t, a[i - 1][1] + (a[i][1] - a[i - 1][1]) * t];
  }
  var L = $$('.schem', stage).map(function (svg) {
    var route = pts($('.route', svg).getAttribute('points'));
    var rb = pts($('.rb-path', svg).getAttribute('points'));
    var rc = cumul(route), bc = cumul(rb), stops = {};
    svg.getAttribute('data-stops').split(',').forEach(function (kv) { var p = kv.split(':'); stops[p[0]] = rc[+p[1]]; });
    var l = { svg: svg, route: route, rb: rb, rc: rc, bc: bc, RT: rc[rc.length - 1], BT: bc[bc.length - 1], stops: stops,
      trail: $('.trail', svg), rbTrail: $('.rb-trail', svg), token: $('.token', svg) };
    l.trail.setAttribute('stroke-dasharray', l.RT + ' ' + l.RT);
    l.rbTrail.setAttribute('stroke-dasharray', l.BT + ' ' + l.BT);
    l.trail.setAttribute('data-ready', '');
    l.rbTrail.setAttribute('data-ready', '');
    return l;
  });
  if (!L.length) return;

  function each(sel, fn) { L.forEach(function (l) { $$(sel, l.svg).forEach(fn); }); }
  function cls(sel, c, on) { each(sel, function (n) { n.classList.toggle(c, on); }); }

  /* p in [0,1] between two frames; distances are per layout */
  function place(from, to, p) {
    L.forEach(function (l) {
      var xy;
      if (to.st === 'rb') {
        var d = (from.st === 'rb' ? 1 : p) * l.BT;
        xy = at(l.rb, l.bc, d);
        l.rbTrail.setAttribute('stroke-dashoffset', (l.BT - d).toFixed(1));
        l.trail.setAttribute('stroke-dashoffset', (l.RT - l.stops.canary).toFixed(1));
      } else {
        var a = l.stops[from.st], b = l.stops[to.st], d2 = a + (b - a) * p;
        xy = at(l.route, l.rc, d2);
        l.trail.setAttribute('stroke-dashoffset', (l.RT - d2).toFixed(1));
        l.rbTrail.setAttribute('stroke-dashoffset', l.BT.toFixed(1));
      }
      l.token.setAttribute('transform', 'translate(' + xy[0].toFixed(1) + ' ' + xy[1].toFixed(1) + ')');
    });
  }
  function durOf(from, to) {
    if (reduce.matches) return 0;
    var l = L[0], dd;
    if (to.st === 'rb') dd = from.st === 'rb' ? 0 : l.BT;
    else dd = Math.abs(l.stops[to.st] - l.stops[from.st]);
    return dd ? Math.max(380, dd / l.RT * 7200) : 0;
  }
  function ease(t) { return t < .5 ? 2 * t * t : 1 - Math.pow(-2 * t + 2, 2) / 2; }

  /* ---- pass model: a fault is one-shot; it is consumed when the issue reaches the canary ---- */
  var idx = 0, tail = null, armed = false, tailWasFault = false;
  var faultBtn = $('[data-line="fault"]');
  function syncFault() { faultBtn.setAttribute('aria-pressed', String(armed)); }
  function frame(k) {
    if (k < HEAD.length) return HEAD[k];
    if (!tail) { tailWasFault = armed; tail = armed ? FAULT_TAIL : OK_TAIL; if (armed) { armed = false; syncFault(); } }
    return tail[k - HEAD.length];
  }
  function lastIndex() { return HEAD.length + (tail || (armed ? FAULT_TAIL : OK_TAIL)).length - 1; }

  /* ---- readout, ruler and schedule ---- */
  var readoutEl = $('#readout'), roState = $('#ro-state'), roZone = $('#ro-zone'), roNote = $('#ro-note');
  var ruler = $$('.ruler li');
  function readout(k) {
    var cur = frame(k), key = cur.state, s = STATE[key] || {};
    roState.textContent = s.name || key;
    roZone.textContent = s.zone || '';
    roNote.textContent = s.note || '';
    var seen = {};
    for (var j = 0; j <= k; j++) { var st = frame(j).state; seen[st === 'CanaryFault' ? 'Canary' : st] = true; }
    var curKey = key === 'CanaryFault' ? 'Canary' : key;
    ruler.forEach(function (li) { var d = li.getAttribute('data-s'); li.classList.toggle('is-done', !!seen[d] && d !== curKey); li.classList.toggle('is-cur', d === curKey); });
    Object.keys(STATE).forEach(function (n) { if (STATE[n].li) STATE[n].li.classList.toggle('is-cur', n === curKey); });
  }

  /* ---- render: rebuild the whole picture for frame k (used for every jump, back or forward) ---- */
  function render(k) {
    ['is-active', 'is-fault', 'is-done'].forEach(function (c) { cls('.st', c, false); });
    cls('.seg', 'is-on', false); cls('.seg', 'is-fault', false);
    cls('.receipt', 'is-on', false); cls('.tag-rb', 'is-on', false); cls('.tag-blocked', 'is-on', false);
    cls('.token', 'is-fault', false);
    for (var j = 0; j <= k; j++) {
      var fx = frame(j).fx;
      if (fx === 'receipt') cls('.receipt', 'is-on', true);
      if (fx === 'canary') cls('.seg', 'is-on', true);
      if (fx === 'fault') { cls('.seg[data-seg="0"]', 'is-fault', true); cls('.token', 'is-fault', true); }
      if (fx === 'rollback') cls('.tag-rb', 'is-on', true);
      if (fx === 'blocked') cls('.tag-blocked', 'is-on', true);
      if (fx === 'closed') cls('.st[data-st="closed"]', 'is-done', true);
    }
    var f = frame(k);
    if (f.fx === 'fault') cls('.st[data-st="canary"]', 'is-fault', true);
    cls('.st[data-st="' + (f.st === 'rb' ? 'sup' : f.st) + '"]', 'is-active', true);
    var prev = k > 0 ? frame(k - 1) : f;
    place(f.st === 'rb' ? prev : f, f, 1);
    readout(k);
  }
  /* rearm: stepping back before the canary un-consumes a fault; starting a new pass does not */
  function goTo(k, rearm) {
    if (k < HEAD.length && tail) {
      if (rearm && tailWasFault) { armed = true; syncFault(); }
      tail = null; tailWasFault = false;
    }
    idx = Math.max(0, Math.min(k, lastIndex()));
    render(idx);
  }

  /* ---- scheduler: one rAF loop, alive only while playing, on screen and the tab is visible ---- */
  var playing = false, onScreen = !('IntersectionObserver' in window), phase = null, raf = 0, lastTs = 0;
  function running() { return playing && onScreen && !document.hidden; }
  function dwell(on) { cls('.token', 'is-dwell', on && !reduce.matches); }
  function wait(ms, done) { phase = { kind: 'wait', t: 0, dur: ms, done: done }; dwell(true); }
  function arrive() { wait(DWELL[frame(idx).state] || DWELL_DEFAULT, nextMove); }
  function nextMove() {
    if (idx >= lastIndex()) { wait(END_PAUSE, function () { goTo(0); wait(DWELL.gh, nextMove); }); return; }
    var from = frame(idx), to = frame(idx + 1);
    dwell(false);
    phase = { kind: 'move', t: 0, dur: durOf(from, to), from: from, to: to, done: function () { idx++; render(idx); arrive(); } };
  }
  function loop(ts) {
    raf = 0;
    if (!running() || !phase) { lastTs = 0; return; }
    var dt = lastTs ? Math.min(ts - lastTs, 64) : 0;
    lastTs = ts;
    phase.t += dt;
    if (phase.kind === 'move' && phase.dur) place(phase.from, phase.to, ease(Math.min(1, phase.t / phase.dur)));
    if (phase.t >= phase.dur) { var p = phase; phase = null; p.done(); }
    raf = requestAnimationFrame(loop);
  }
  function kick() {
    if (running()) { if (!raf) { lastTs = 0; raf = requestAnimationFrame(loop); } }
    else if (raf) { cancelAnimationFrame(raf); raf = 0; }
    dwell(running() && !!phase && phase.kind === 'wait');
  }

  /* ---- controls ---- */
  var playBtn = $('[data-line="play"]'), playLbl = $('[data-line-label]');
  function syncPlay() {
    playLbl.textContent = playing ? 'Pause' : 'Play';
    playBtn.setAttribute('data-state', playing ? 'playing' : 'paused');
    readoutEl.setAttribute('aria-live', playing ? 'off' : 'polite');
  }
  function setPlaying(on) {
    playing = on;
    if (on && !phase) { if (idx >= lastIndex()) goTo(0); nextMove(); }
    syncPlay(); kick();
  }
  function manual(k, rearm) { playing = false; phase = null; syncPlay(); kick(); goTo(k, rearm); }
  function stepFwd() { manual(idx >= lastIndex() ? 0 : idx + 1); }
  function stepBack() { manual(idx > 0 ? idx - 1 : 0, true); }
  function toggleFault() { armed = !armed; syncFault(); }

  playBtn.addEventListener('click', function () { setPlaying(!playing); });
  $('[data-line="step"]').addEventListener('click', stepFwd);
  $('[data-line="back"]').addEventListener('click', stepBack);
  faultBtn.addEventListener('click', toggleFault);

  /* keyboard, on the focusable drawing */
  stage.setAttribute('tabindex', '0');
  stage.addEventListener('keydown', function (e) {
    if (e.altKey || e.ctrlKey || e.metaKey) return;
    var k = e.key, handled = true;
    if (k === 'ArrowRight' || k === 'ArrowDown') stepFwd();
    else if (k === 'ArrowLeft' || k === 'ArrowUp') stepBack();
    else if (k === ' ' || k === 'Enter') setPlaying(!playing);
    else if (k === 'f' || k === 'F') toggleFault();
    else if (k === 'Home') manual(0);
    else if (k === 'End') manual(lastIndex());
    else handled = false;
    if (handled) e.preventDefault();
  });

  /* pause off screen and in background tabs; CSS animations in the drawing pause too */
  if ('IntersectionObserver' in window) {
    new IntersectionObserver(function (entries) {
      onScreen = entries[entries.length - 1].isIntersecting;
      figure.classList.toggle('is-off', !onScreen);
      kick();
    }).observe(figure);
  }
  document.addEventListener('visibilitychange', kick);
  onMQ(reduce, function () { if (reduce.matches && playing) { playing = false; syncPlay(); kick(); } });

  /* the full schedule stays open without JS; with the live drawing it starts folded */
  var sched = $('details.schedule');
  if (sched) sched.open = false;

  goTo(0);
  syncFault();
  if (!reduce.matches) { playing = true; wait(INTRO, nextMove); }
  syncPlay();
  kick();
})();
