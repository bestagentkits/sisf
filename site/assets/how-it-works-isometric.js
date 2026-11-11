/* SISF · "How it works": the isometric view (tier 2 of 3).

   Loaded by app.js instead of the Three.js scene when the browser has no usable WebGL, or when
   the scene fails. It takes the same drawing the page already shows as an <img> (the no-JS
   tier), inlines it so the page's theme tokens colour it, and animates it with the same flow
   player: the work item travels the ring, the parts at work light up, the same captions and
   step notes appear. No camera, no ambient motion: frames are drawn only while something moves
   or a playing step holds, and never off-screen or in a hidden tab. */
import { createPlayer, RM, clamp, easeIO } from './how-it-works-player.js?v=20261111';

const root = document.documentElement;
const host = document.querySelector('[data-loop]');
const SVGNS = 'http://www.w3.org/2000/svg';
const fallback = why => document.dispatchEvent(new CustomEvent('sisf:how-fallback', { detail: { from: '2d', why: String((why && why.message) || why) } }));

if (!host) fallback('no section');
else start().catch(fallback);

async function start() {
  const $ = (s, r = host) => r.querySelector(s);
  const $$ = (s, r = host) => Array.from(r.querySelectorAll(s));
  const box = $('[data-scene]'), frameEl = $('[data-frame]'), img = $('[data-poster]'), labelsEl = $('[data-labels]');

  /* ---------- the drawing's colours, then the drawing: the same file as the <img>, inlined ---------- */
  const styled = new Promise((ok, no) => {
    const link = document.createElement('link');
    link.rel = 'stylesheet';
    link.href = new URL('./how-it-works-isometric.css?v=20261111', import.meta.url).href;
    link.onload = ok;
    link.onerror = () => no(new Error('drawing styles did not load'));
    document.head.appendChild(link);
  });
  /* the rejection is handled at "await styled" below; this stops it being reported as unhandled
     when the stylesheet fails while the drawing is still downloading */
  styled.catch(() => {});
  const res = await fetch(img.currentSrc || img.src, { credentials: 'same-origin' });
  if (!res.ok) throw new Error(`drawing: HTTP ${res.status}`);
  const doc = new DOMParser().parseFromString(await res.text(), 'image/svg+xml');
  await styled;
  if (doc.documentElement.nodeName !== 'svg') throw new Error('drawing: not an SVG');
  const style = doc.querySelector('style');
  if (style) style.remove();                      // the theme-neutral colours are for the <img> only
  const svg = document.importNode(doc.documentElement, true);
  svg.removeAttribute('width');
  svg.removeAttribute('height');
  svg.setAttribute('class', 'loop-svg');
  svg.setAttribute('aria-hidden', 'true');
  svg.setAttribute('focusable', 'false');
  const vb = svg.getAttribute('viewBox').split(/\s+/).map(Number);
  const ANCH = JSON.parse(svg.getAttribute('data-anchors'));
  const RING = JSON.parse(svg.getAttribute('data-ring'));

  /* ---------- the ring, in the drawing's own projection ---------- */
  const zc = (RING.lo + RING.hi) / 2, amp = (RING.hi - RING.lo) / 2;
  const rs = amp / RING.r, rc = Math.sqrt(1 - rs * rs), K = Math.SQRT1_2;
  function ringXY(t) {
    const th = 2 * Math.PI * t, ct = Math.cos(th), st = Math.sin(th);
    const x = RING.cx + RING.r * (ct * rc * K - st * K);
    const y = RING.cy + RING.r * (ct * rc * K + st * K);
    const z = zc - RING.r * ct * rs;
    return { x: (x - y) * RING.c, y: (x + y) / 2 - z, front: ct > 0 };
  }
  const wrap = t => ((t % 1) + 1) % 1;
  const f1 = v => v.toFixed(1);

  /* dynamic layers: progress behind and in front of the stack, then tethers, beam, ghost, work item */
  const back = svg.querySelector('[data-dyn-back]'), front = svg.querySelector('[data-dyn-front]');
  const mk = (tag, cls, parent, attrs = {}) => {
    const el = document.createElementNS(SVGNS, tag);
    el.setAttribute('class', cls);
    for (const k in attrs) el.setAttribute(k, attrs[k]);
    parent.appendChild(el);
    return el;
  };
  const progBack = mk('path', 'loop-prog', back), progFront = mk('path', 'loop-prog', front);
  const tethers = [0, 1, 2].map(() => mk('line', 'loop-tether', front));
  const beam = mk('line', 'loop-beam', front);
  const ghost = mk('circle', 'loop-ghost', front, { r: 6 });
  const tok = mk('g', 'loop-tok', front);
  const halo = mk('circle', 'loop-tok-halo', tok, { r: 15 });
  mk('circle', 'loop-tok-b', tok, { r: 7.5 });
  mk('path', 'loop-tok-x', tok, { d: 'M-3.2 -3.2 L3.2 3.2 M3.2 -3.2 L-3.2 3.2' });
  const chevrons = $$('.i-chev', svg).map(el => ({ el, t: +el.getAttribute('data-t') }));

  /* the parts: every group that stands for a part of the 3D scene carries its id */
  const groups = {};
  $$('[data-unit]', svg).forEach(g => { (groups[g.dataset.unit] = groups[g.dataset.unit] || []).push(g); });
  const canaryG = svg.querySelector('#iso-u-canary');
  const risers = { api: svg.querySelector('#iso-u-api'), rel: svg.querySelector('#iso-u-rel'), tel: svg.querySelector('#iso-u-tel') };

  /* ---------- geometry questions from the player ---------- */
  let W = 1, H = 1, fx0 = 0, fy0 = 0, sc = 1, rects = [];
  const toBox = (X, Y) => ({ x: fx0 + (X - vb[0]) * sc, y: fy0 + (Y - vb[1]) * sc });
  function measure() {
    W = Math.max(1, box.clientWidth);
    H = Math.max(1, box.clientHeight);
    fx0 = frameEl.offsetLeft; fy0 = frameEl.offsetTop;
    sc = frameEl.offsetWidth / vb[2];
    const br = box.getBoundingClientRect();
    rects = $$('[data-zone], [data-actor]', labelsEl).map(el => {
      const r = el.getBoundingClientRect();
      return { x: r.left - br.left, y: r.top - br.top, w: r.width, h: r.height };
    });
    if (P) P.measure();
    wake();
  }

  /* ---------- drawing a frame from what the player shows ---------- */
  let progT0 = 0, fxKey = '', restartAt = -1;
  function arc(t0, t1) {
    /* sampled path strings for the stretch t0..t1, split where the ring passes behind the stack */
    const n = Math.max(2, Math.ceil((t1 - t0) * 240));
    let b = '', f = '', prev = null;
    for (let i = 0; i <= n; i++) {
      const p = ringXY(wrap(t0 + (t1 - t0) * i / n));
      const cmd = prev === p.front ? 'L' : 'M';
      const seg = `${cmd}${f1(p.x)},${f1(p.y)}`;
      if (p.front) f += seg; else b += seg;
      prev = p.front;
    }
    return [b, f];
  }
  function draw() {
    const show = P.show;
    const t = P.beadT();
    const fault = show.beadKind === 'fault';
    svg.classList.toggle('is-fault', fault);
    /* the work item and the stretch of the loop it has covered in this flow */
    tok.style.display = show.beadOn ? '' : 'none';
    const frac = show.beadOn ? clamp(t - progT0, 0, 1) : 0;
    if (show.beadOn) {
      const p = ringXY(wrap(t));
      tok.setAttribute('transform', `translate(${f1(p.x)} ${f1(p.y)})`);
      const [b, f] = frac > 0.002 ? arc(progT0, progT0 + frac) : ['', ''];
      progBack.setAttribute('d', b || 'M0,0');
      progFront.setAttribute('d', f || 'M0,0');
      progBack.style.display = b ? '' : 'none';
      progFront.style.display = f ? '' : 'none';
    } else { progBack.style.display = progFront.style.display = 'none'; }
    chevrons.forEach(c => {
      const ct = c.t >= progT0 ? c.t : c.t + 1;
      c.el.classList.toggle('is-on', show.beadOn && frac > 0 && ct <= t + 1e-6);
    });
    /* the counter-case: with thin evidence the work item slips back to watching */
    let gv = false;
    if (show.ghost && !RM.matches) {
      const p = (P.clock - show.ghost.t0) / show.ghost.dur;
      gv = p > 0 && p < 1;
      if (gv) {
        const g = ringXY(wrap(show.ghost.from * (1 - easeIO(p))));
        ghost.setAttribute('cx', f1(g.x)); ghost.setAttribute('cy', f1(g.y));
        ghost.style.opacity = String(0.85 * (1 - Math.max(0, p - 0.7) / 0.3));
      }
    }
    ghost.style.display = gv ? '' : 'none';
    /* tethers from the work item to up to three parts at work */
    const ids = [...show.active].filter(id => ANCH[id]).slice(0, 3);
    const tp = show.beadOn ? ringXY(wrap(t)) : null;
    tethers.forEach((l, i) => {
      const a = ANCH[ids[i]];
      const on = !!(tp && a);
      l.style.display = on ? '' : 'none';
      if (!on) return;
      l.setAttribute('x1', f1(tp.x)); l.setAttribute('y1', f1(tp.y));
      l.setAttribute('x2', f1(a[0])); l.setAttribute('y2', f1(a[1]));
    });
    /* rollback: the last known good comes down from the supervisor to the app */
    const lkg = show.fx.has('lkg');
    beam.style.display = lkg ? '' : 'none';
    if (lkg) {
      const [x1, y1] = ANCH.supervisor, [x2, y2] = ANCH.app;
      const g = easeIO(RM.matches ? 1 : clamp((P.clock - show.fxAt) / 1100, 0, 1));
      beam.setAttribute('x1', f1(x1)); beam.setAttribute('y1', f1(y1));
      beam.setAttribute('x2', f1(x1 + (x2 - x1) * g)); beam.setAttribute('y2', f1(y1 + (y2 - y1) * g));
    }
    /* the parts at work, and the one under the pointer or the keyboard */
    const hot = P.pinnedId || P.kbId || P.hoverId || '';
    const key = [...show.active].join() + '|' + [...show.fx].join() + '|' + show.fxAt + '|' + hot;
    if (key !== fxKey) {
      fxKey = key;
      for (const id in groups) {
        groups[id].forEach(g => {
          const lit = show.active.has(id) && (g !== canaryG || show.fx.has('canary'));
          g.classList.toggle('is-on', lit);
          g.classList.toggle('is-hl', id === hot);
        });
      }
      if (risers.api) risers.api.classList.toggle('is-on', show.active.has('dispatcher'));
      if (risers.rel) risers.rel.classList.toggle('is-on', show.fx.has('canary') || show.fx.has('lkg'));
      if (risers.tel) risers.tel.classList.toggle('is-on', show.active.has('decision'));
      ['err', 'canary', 'fault', 'flaw', 'lock', 'lkg'].forEach(x => svg.classList.toggle(`fx-${x}`, show.fx.has(x)));
      /* restart: Zones 2 and 3 blink while Zone 1 keeps serving; a new restart replays the blink */
      const restart = show.fx.has('restart');
      if (restart && show.fxAt !== restartAt) { svg.classList.remove('fx-restart'); void svg.getBoundingClientRect(); restartAt = show.fxAt; }
      svg.classList.toggle('fx-restart', restart);
    }
  }

  /* ---------- the loop: only while something moves or a playing step holds ---------- */
  let P = null, raf = 0, lastT = 0, lastRender = 0, dirty = true;
  function frame(now) {
    raf = 0;
    if (!P.visible || document.hidden) return;
    const moving = P.busy();
    if (!moving && !dirty && now - lastRender < 32) { if (P.needsFrames()) raf = requestAnimationFrame(frame); return; }
    const dt = Math.min(50, now - lastT);
    lastT = now;
    P.tick(dt);
    draw();
    P.placeOverlays();
    dirty = false;
    lastRender = now;
    if (P.busy() || P.needsFrames()) raf = requestAnimationFrame(frame);
  }
  function wake() {
    dirty = true;
    if (!raf && P && P.visible && !document.hidden) { lastT = performance.now(); raf = requestAnimationFrame(frame); }
  }

  /* ---------- pointer: hover previews a part, a click selects it ---------- */
  const unitAt = e => { const g = e.target.closest && e.target.closest('[data-unit]'); return g ? g.dataset.unit : null; };
  svg.addEventListener('pointermove', e => { if (e.pointerType === 'mouse') P.setHover(unitAt(e)); });
  svg.addEventListener('pointerleave', () => P.setHover(null));
  svg.addEventListener('click', e => { const id = unitAt(e); if (id) P.select(id); else P.closeFocus(); });
  $$('[data-actor]', labelsEl).forEach(el => el.addEventListener('click', () => P.select(el.dataset.actor)));

  P = createPlayer(host, {
    wake,
    size: () => ({ W, H }),
    anchorPx: id => { const a = ANCH[id] || ANCH.app; return toBox(a[0], a[1]); },
    labelRects: () => rects,
    focus: () => wake(),
    focused: () => false,
    home: () => {},
    onStep: () => {},
    progress: t0 => { progT0 = t0; },
    autoplayDelay: 600,
  });

  try {
    img.after(svg);
    root.setAttribute('data-how', '2d');
    measure();
    new ResizeObserver(measure).observe(box);
    P.start();
    draw();
    P.placeOverlays();
    wake();
    if (document.fonts && document.fonts.ready) document.fonts.ready.then(measure);
  } catch (err) {
    /* hand the section back to the static drawing and its text */
    P.destroy();
    svg.remove();
    throw err;
  }
}
