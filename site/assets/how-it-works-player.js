/* SISF · "How it works": the flow player shared by the 3D scene and the isometric drawing.

   It owns everything that is not drawing: the four flows (read from the ordered lists in the
   page, so every sentence has one source), their timing, the caption, the progressive step
   list, the one pinned step note, the part callouts, the parts list, playback and autoplay.
   A view draws from `P.show` every frame and answers a few geometry questions:

     view.wake()              ask for frames
     view.size()              { W, H } of the scene box in CSS pixels
     view.anchorPx(id)        { x, y } of a part (or 'canary') inside the scene box
     view.labelRects()        boxes of the persistent labels, for the note to keep clear of
     view.focus(id | null)    ease toward a part, or back (optional)
     view.focused()           true while zoomed toward a part
     view.home()              title card between flows: back to the overview
     view.onStep(units)       a step began (the 3D camera leans toward its parts)
     view.progress(t0)        a flow starts its stretch of the loop at t0
     view.firstView()         the scene came into view for the first time (optional)
     view.autoplayDelay       ms between first view and autoplay

   Each step runs motion first, words second: the work item travels and the parts light up,
   then the caption, the list entry and the note appear, then the step holds long enough to
   read. Between flows a short title card names the next one. */

const RM = matchMedia('(prefers-reduced-motion: reduce)');
const MOB = matchMedia('(max-width: 700px)');
const clamp = (v, a, b) => Math.min(b, Math.max(a, v));
const easeIO = t => (t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2);
const pad2 = n => String(n).padStart(2, '0');
const split = s => (s || '').split(',').map(x => x.trim()).filter(Boolean);
const overlap = (x, y, w, h, r) => Math.max(0, Math.min(x + w, r.x + r.w) - Math.max(x, r.x)) * Math.max(0, Math.min(y + h, r.y + r.h) - Math.max(y, r.y));

/* pacing, in player-clock milliseconds */
const CARD_MS = 1800;              // the "Next" title card between flows
const WORDS_AFTER_ARRIVAL = 200;   // words follow the motion
const WORDS_AFTER_STILL = 450;     // a step with no travel still gets a beat of motion first
const readMs = st => clamp(1400 + 95 * (st.title + ' ' + st.body).split(/\s+/).length, 2500, 4500);

export { RM, MOB, clamp, easeIO };

export function createPlayer(host, view) {
  const $ = (s, r = host) => r.querySelector(s);
  const $$ = (s, r = host) => Array.from(r.querySelectorAll(s));
  /* every listener is registered with one signal, so a view that fails later can hand over cleanly */
  const ac = new AbortController();
  const on = (el, type, fn) => el.addEventListener(type, fn, { signal: ac.signal });

  /* ---------- the flows, from the ordered lists ---------- */
  const flows = $$('[data-flow]').map(ol => ({
    name: ol.dataset.name,
    sum: ol.dataset.sum || '',
    t0: +ol.dataset.t0,
    li: $$('li', ol),
    steps: $$('li', ol).map(li => ({
      t: +li.dataset.t,
      units: split(li.dataset.units),
      fx: split(li.dataset.fx),
      bead: li.dataset.bead || 'work',
      ghost: li.hasAttribute('data-ghost'),
      at: li.dataset.at || split(li.dataset.units)[0],
      note: li.dataset.note || '',
      title: li.querySelector('b').textContent,
      body: li.querySelector('span').textContent,
    })),
  }));

  const box = $('[data-scene]');
  const tabs = $$('[role="tab"]'), panels = $$('[role="tabpanel"]');
  const ro = {
    box: $('[data-readout]'), scn: $('[data-ro-scn]'), n: $('[data-ro-n]'), words: $('[data-ro-words]'),
    title: $('[data-ro-title]'), body: $('[data-ro-body]'), dots: $('[data-ro-dots]'),
  };
  const live = $('[data-live]');
  const nowEl = $('[data-now]'), nowN = $('[data-now-n]'), nowT = $('[data-now-t]');
  const chapter = $('[data-chapter]'), chK = $('[data-ch-k]'), chT = $('[data-ch-t]');
  const playerEl = $('[data-player]');
  const playBtn = $('[data-act="play"]'), playLbl = $('[data-play-lbl]');
  const pbtns = $$('[data-parts] [data-unit]');
  const btnFor = Object.fromEntries(pbtns.map(b => [b.dataset.unit, b]));
  const actorLbls = $$('[data-actor]', $('[data-labels]'));

  /* ---------- what the views draw ---------- */
  const show = {
    active: new Set(),       // parts involved in the current step
    fx: new Set(),           // effects of the current step
    fxAt: 0,                 // player clock when the effects started
    beadKind: 'work',        // work | fault
    beadOn: false,
    bead: { from: 0, to: 0, t0: 0, dur: 0, t: 0 },
    ghost: null,             // { t0, dur, from } on the player clock: the thin-evidence counter-case
  };
  const P = {
    show, clock: 0, playing: false, single: false, pendingFx: null, visible: false,
    hoverId: null, kbId: null, pinnedId: null, flows,
  };
  const progressive = () => !RM.matches;   // with reduced motion every list is shown whole

  let fi = 0, si = -1;              // flow, and step (-1 = the flow's title card)
  let userPaused = false, started = false, autoTimer = 0;
  let stepEnd = 0, singleEnd = 0, wordsAt = 0, wordsShown = true;

  P.beadT = () => {
    const b = show.bead;
    if (!b.dur) return b.to;
    return b.from + (b.to - b.from) * easeIO(clamp((P.clock - b.t0) / b.dur, 0, 1));
  };
  P.beadMoving = () => show.bead.dur > 0 && P.clock < show.bead.t0 + show.bead.dur;
  P.ghostMoving = () => !!show.ghost && P.clock < show.ghost.t0 + show.ghost.dur;
  /* frames at full rate only while something moves; a playing clock still needs (slower) frames */
  P.busy = () => P.single || P.beadMoving() || !!P.pendingFx || P.ghostMoving();
  P.needsFrames = () => P.playing || P.single;

  function selectTab(f, focus) {
    tabs.forEach((t, j) => {
      const on = j === f;
      t.setAttribute('aria-selected', String(on));
      t.tabIndex = on ? 0 : -1;
      panels[j].hidden = !on;
    });
    if (focus) tabs[f].focus();
  }
  function markParts() {
    pbtns.forEach(b => b.classList.toggle('is-on', show.active.has(b.dataset.unit)));
    actorLbls.forEach(el => el.classList.toggle('is-on', show.active.has(el.dataset.actor) || P.pinnedId === el.dataset.actor));
  }

  /* ---------- a flow's title card: the tab switches, the list empties, the scene clears, the view goes home ---------- */
  function titleCard(f, animate) {
    const flow = flows[f];
    selectTab(f, false);
    fi = f; si = -1;
    show.active = new Set();
    show.beadOn = false;
    show.ghost = null;
    P.pendingFx = { at: P.clock, fx: new Set() };
    applyFx();
    wordsShown = true;
    stepEnd = P.clock + (animate ? CARD_MS : 0);
    view.onStep([]);
    if (animate && !RM.matches && !P.pinnedId) view.home();
    hideNote();
    const first = !animate;
    ro.scn.textContent = `${first ? '' : 'Next · '}Flow ${f + 1} of ${flows.length}`;
    ro.n.textContent = `${flow.steps.length} steps`;
    ro.box.classList.remove('is-fault');
    setWords(`${flow.name}.`, flow.sum);
    paintDots();
    paintList();
    nowEl.classList.remove('is-fault');
    nowN.textContent = first ? `Flow ${f + 1}` : 'Next';
    nowT.textContent = flow.name;
    nowT.classList.add('is-in');
    chK.textContent = `${first ? '' : 'Next · '}Flow ${f + 1} of ${flows.length}`;
    chT.textContent = flow.name;
    chapter.classList.add('is-in');
    markParts();
    view.wake();
  }

  /* ---------- one step ---------- */
  function enter(f, s, animate) {
    const flow = flows[f], st = flow.steps[s];
    const moving = animate && !RM.matches;
    if (f !== fi || !show.beadOn) {
      selectTab(f, false);
      view.progress(flow.t0);
      show.bead = { from: flow.t0, to: flow.t0, t0: P.clock, dur: 0, t: flow.t0 };
    }
    fi = f; si = s;
    chapter.classList.remove('is-in');
    /* travel from where the previous step was headed, so quick Step presses never replay old travel */
    const from = moving ? show.bead.to : st.t;
    const dt = Math.max(0, st.t - from);
    const dur = moving && dt > 0 ? 700 + 2600 * dt : 0;
    show.bead = { from: moving ? from : st.t, to: st.t, t0: P.clock, dur, t: from };
    show.beadOn = true;
    show.beadKind = st.bead;
    show.active = new Set(st.units);
    const arrive = P.clock + dur;
    P.pendingFx = { at: arrive, fx: new Set(st.fx) };
    if (!moving) applyFx();
    show.ghost = st.ghost && moving ? { t0: arrive + 500, dur: 2000, from: st.t } : null;
    wordsAt = moving ? arrive + (dur ? WORDS_AFTER_ARRIVAL : WORDS_AFTER_STILL) : P.clock;
    wordsShown = false;
    stepEnd = wordsAt + readMs(st);
    singleEnd = Math.max(wordsAt + 400, arrive + 1300, show.ghost ? show.ghost.t0 + show.ghost.dur : 0);
    view.onStep(st.units);
    /* the frame of the step changes at once; its words wait for the motion */
    ro.scn.textContent = `Flow ${fi + 1} · ${flow.name}`;
    ro.n.textContent = `Step ${si + 1} / ${flow.steps.length}`;
    ro.box.classList.toggle('is-fault', st.bead === 'fault');
    nowEl.classList.toggle('is-fault', st.bead === 'fault');
    nowN.textContent = `${pad2(si + 1)}/${pad2(flow.steps.length)}`;
    ro.words.classList.remove('is-in');
    ro.box.classList.add('is-wait');
    nowT.classList.remove('is-in');
    hideNote();
    paintDots();
    paintList();
    markParts();
    if (!moving) showWords();
    view.wake();
  }
  function applyFx() {
    if (!P.pendingFx) return;
    const was = show.fx;
    show.fx = P.pendingFx.fx;
    /* restart blinks and the rollback beam time themselves from the moment they begin */
    if ([...show.fx].some(x => !was.has(x))) show.fxAt = P.clock;
    P.pendingFx = null;
  }

  /* the words of the current step: caption, list entry and the note in the scene, together */
  function showWords() {
    const st = flows[fi].steps[si];
    wordsShown = true;
    setWords(st.title, st.body);
    nowT.textContent = st.title;
    void nowT.offsetWidth;
    nowT.classList.add('is-in');
    paintList(true);
    showNote(st);
  }
  function setWords(title, body) {
    ro.box.classList.remove('is-wait');
    ro.title.textContent = title;
    ro.body.textContent = body;
    ro.words.classList.remove('is-in');
    void ro.words.offsetWidth;              // restart the fade
    ro.words.classList.add('is-in');
  }
  function paintDots() {
    const n = flows[fi].steps.length;
    ro.dots.textContent = '';
    for (let j = 0; j < n; j++) {
      const d = document.createElement('li');
      if (j < si) d.className = 'is-done';
      if (j === si) d.className = 'is-cur';
      ro.dots.appendChild(d);
    }
  }
  /* only steps already reached are drawn; the rest keep their place as numbered hairlines.
     Their words stay in the accessibility tree, so screen readers always have every step. */
  function paintList(fresh) {
    const prog = progressive();
    flows[fi].li.forEach((li, j) => {
      const cur = j === si;
      const shown = !prog || j < si || (cur && wordsShown);
      li.classList.toggle('is-ahead', !shown);
      li.classList.toggle('is-past', prog && j < si);
      li.classList.toggle('is-cur', cur && shown);
      li.classList.toggle('is-new', !!fresh && cur && prog);
      if (cur) li.setAttribute('aria-current', 'step'); else li.removeAttribute('aria-current');
    });
  }
  /* the live region speaks only when someone steps by hand */
  function announce() {
    const flow = flows[fi], st = flow.steps[si];
    if (st) live.textContent = `Flow ${fi + 1}, step ${si + 1} of ${flow.steps.length}. ${st.title} ${st.body}`;
  }

  function advance() {
    if (si < flows[fi].steps.length - 1) enter(fi, si + 1, true);
    else titleCard((fi + 1) % flows.length, true);
  }
  function nextStep() {
    if (si < flows[fi].steps.length - 1) enter(fi, si + 1, true);
    else enter((fi + 1) % flows.length, 0, true);
  }
  function prevStep() {
    if (si > 0) enter(fi, si - 1, false);
    else { const f = (fi + flows.length - 1) % flows.length; enter(f, flows[f].steps.length - 1, false); }
  }

  /* Called by the view every frame: the clock runs only while a flow plays or steps. */
  P.tick = dt => {
    if (P.playing || P.single) P.clock += dt;
    if (P.pendingFx && P.clock >= P.pendingFx.at) applyFx();
    if (!wordsShown && P.clock >= wordsAt) showWords();
    if (P.playing) { if (P.clock >= stepEnd) advance(); }
    else if (P.single && P.clock >= singleEnd && !P.pendingFx && wordsShown) P.single = false;
  };

  function play() {
    P.playing = true; userPaused = false;
    /* resuming after a pause: the words were there to read, so move on after a short beat
       (a step whose words have not appeared yet keeps its full timeline) */
    if (wordsShown) stepEnd = Math.min(stepEnd, P.clock + (si < 0 ? 300 : 700));
    P.single = false;
    playBtn.dataset.state = 'playing';
    playBtn.setAttribute('aria-label', 'Pause the flows');
    playLbl.textContent = 'Pause';
    view.wake();
  }
  function pause(byUser) {
    P.playing = false;
    if (byUser) {
      userPaused = true;
      /* let the current step finish its motion and show its words, then hold */
      if (si >= 0 && !RM.matches && (!wordsShown || P.beadMoving() || P.pendingFx)) P.single = true;
      view.wake();
    }
    playBtn.dataset.state = 'paused';
    playBtn.setAttribute('aria-label', 'Play the flows');
    playLbl.textContent = 'Play';
  }
  P.pause = pause;
  /* any manual step takes over: the chain waits until Play is pressed again */
  function takeOver() { if (P.playing) pause(true); else userPaused = true; closeFocus(); }

  on(playBtn, 'click', () => (P.playing ? pause(true) : play()));
  on($('[data-act="step"]'), 'click', () => {
    takeOver();
    P.single = !RM.matches;
    nextStep();
    announce();
  });
  on($('[data-act="back"]'), 'click', () => {
    takeOver();
    P.single = false;
    prevStep();
    announce();
  });
  function pickFlow(j, focus) {
    takeOver();
    P.single = false;
    selectTab(j, focus);
    enter(j, 0, false);
    announce();
  }
  tabs.forEach((t, j) => {
    on(t, 'click', () => pickFlow(j, false));
    on(t, 'keydown', e => {
      const n = tabs.length;
      const k = { ArrowRight: j + 1, ArrowDown: j + 1, ArrowLeft: j - 1, ArrowUp: j - 1, Home: 0, End: n - 1 }[e.key];
      if (k === undefined) return;
      e.preventDefault();
      pickFlow((k + n) % n, true);
    });
  });
  /* like a carousel: when keyboard focus arrives in the player, the chain stops until Play is pressed */
  on(playerEl, 'focusin', e => {
    if (!P.playing || playerEl.contains(e.relatedTarget) || !e.target.matches(':focus-visible')) return;
    pause(true);
  });

  /* ---------- the parts: the list is the text source for every callout ---------- */
  function partInfo(id) {
    const b = btnFor[id];
    return {
      name: b.querySelector('[data-pb-n]').textContent,
      text: b.querySelector('.vh').textContent.replace(/^\.\s*/, ''),
      where: b.dataset.where,
    };
  }
  const co = $('[data-callout]'), coK = $('[data-co-k]'), coN = $('[data-co-n]'), coT = $('[data-co-t]');
  let coId = null;
  function showCallout(id, pinned) {
    if (!btnFor[id]) return;
    const p = partInfo(id);
    coId = id;
    coK.textContent = p.where;
    coN.textContent = p.name;
    coT.textContent = p.text;
    co.classList.toggle('is-pinned', pinned);
    co.hidden = false;
    placeCallout();
  }
  function hideCallout() { co.hidden = true; co.classList.remove('is-pinned'); coId = null; }
  function placeCallout() {
    if (!coId) return;
    const { W, H } = view.size();
    const p = view.anchorPx(coId);
    const cw = co.offsetWidth, ch = co.offsetHeight;
    const x = clamp(p.x - cw / 2, 8, W - cw - 8);
    let y = p.y - ch - 18;
    if (y < 56) y = p.y + 26;               // keep clear of the view buttons at the top
    y = clamp(y, 8, H - ch - 8);
    co.style.transform = `translate(${Math.round(x)}px, ${Math.round(y)}px)`;
  }
  function refreshPreview() {
    if (P.pinnedId) return;
    const id = P.kbId || P.hoverId;
    if (id) showCallout(id, false); else hideCallout();
  }
  /* select = ease toward a part and pin its callout */
  function select(id) {
    if (!btnFor[id]) return;
    if (P.playing) pause(true);
    P.pinnedId = id;
    showCallout(id, true);
    view.focus(id);
    pbtns.forEach(b => b.setAttribute('aria-expanded', String(b.dataset.unit === id)));
    markParts();
    view.wake();
  }
  function closeFocus() {
    if (!P.pinnedId) return;
    P.pinnedId = null;
    view.focus(null);
    pbtns.forEach(b => b.setAttribute('aria-expanded', 'false'));
    hideCallout();
    refreshPreview();
    markParts();
    view.wake();
  }
  P.select = select;
  P.closeFocus = closeFocus;
  P.setHover = id => { if (id === P.hoverId) return; P.hoverId = id; refreshPreview(); view.wake(); };
  on($('[data-act="close"]'), 'click', () => {
    const id = P.pinnedId;
    closeFocus();
    if (id && btnFor[id] && co.contains(document.activeElement)) btnFor[id].focus();
  });
  /* parts list: roving focus, preview on focus or hover, select on activate */
  function rove(i) {
    pbtns.forEach((b, j) => { b.tabIndex = j === i ? 0 : -1; });
    pbtns[i].focus();
  }
  pbtns.forEach((b, i) => {
    const id = b.dataset.unit;
    on(b, 'click', () => (P.pinnedId === id ? closeFocus() : select(id)));
    on(b, 'focus', () => { pbtns.forEach((o, j) => { o.tabIndex = j === i ? 0 : -1; }); P.kbId = id; refreshPreview(); view.wake(); });
    on(b, 'blur', () => { P.kbId = null; refreshPreview(); view.wake(); });
    on(b, 'mouseenter', () => { P.hoverId = id; refreshPreview(); view.wake(); });
    on(b, 'mouseleave', () => { if (P.hoverId === id) P.hoverId = null; refreshPreview(); view.wake(); });
    on(b, 'keydown', e => {
      const n = pbtns.length;
      const next = { ArrowRight: i + 1, ArrowDown: i + 1, ArrowLeft: i - 1, ArrowUp: i - 1, Home: 0, End: n - 1 }[e.key];
      if (next === undefined) return;
      e.preventDefault();
      rove((next + n) % n);
    });
  });
  on(document, 'keydown', e => {
    if (e.key !== 'Escape' || !P.pinnedId) return;
    const id = P.pinnedId;
    closeFocus();
    if (co.contains(document.activeElement) && btnFor[id]) btnFor[id].focus();
  });

  /* ---------- the step note: one small label pinned to the part the current step is about ----------
     It fades in with the step's words and out when the next step begins. A leader runs from a pin
     on the part to the nearest edge of the label, re-placed every frame. */
  const note = {
    el: $('[data-anno]'), box: $('[data-anno-t]'), k: $('[data-anno-k]'), n: $('[data-anno-n]'),
    ln: $('[data-anno-ln]'), pin: $('[data-anno-pin]'), at: null, side: -1, w: 0, h: 0,
  };
  const fixedRects = [];               // view buttons and the hint: the note keeps clear of them
  function showNote(st) {
    if (!st.note || !(st.at === 'canary' || btnFor[st.at])) { hideNote(); return; }
    note.at = st.at;
    note.k.textContent = st.at === 'canary' ? 'canary' : partInfo(st.at).name;
    note.n.textContent = st.note;
    note.el.classList.toggle('is-fault', st.bead === 'fault');
    note.w = note.box.offsetWidth;
    note.h = note.box.offsetHeight;
    note.side = -1;
    placeNote();
    note.el.classList.add('is-in');
  }
  function hideNote() { note.el.classList.remove('is-in'); }
  function placeNote() {
    /* exploring a part takes over: its callout replaces the note */
    note.el.classList.toggle('is-away', !co.hidden || !!view.focused());
    if (!note.at) return;
    const { W, H } = view.size();
    const p = view.anchorPx(note.at);
    const g = MOB.matches ? 20 : 30, w = note.w, h = note.h;
    const cand = [[p.x + g, p.y - g - h], [p.x - g - w, p.y - g - h], [p.x + g, p.y + g], [p.x - g - w, p.y + g]];
    const rects = [...fixedRects, ...view.labelRects()];
    const cost = cand.map(([x, y], i) => {
      let c = i > 1 ? 60 : 0;                                   // above the part reads first
      if (p.x > W * 0.55 && i % 2) c += 80;                     // and outward from the middle
      if (p.x < W * 0.45 && !(i % 2)) c += 80;
      const ox = Math.max(0, 6 - x) + Math.max(0, x + w - W + 6), oy = Math.max(0, 6 - y) + Math.max(0, y + h - H + 6);
      c += (ox * h + oy * w) * 4;                               // leaving the scene is worst
      for (const r of rects) c += overlap(x, y, w, h, r) * 2;
      return c;
    });
    let best = 0;
    cost.forEach((c, i) => { if (c < cost[best]) best = i; });
    if (note.side < 0 || cost[note.side] > cost[best] + 400) note.side = best;   // no flip-flopping while it drifts
    const x = clamp(cand[note.side][0], 6, W - w - 6), y = clamp(cand[note.side][1], 6, H - h - 6);
    note.box.style.transform = `translate(${Math.round(x)}px, ${Math.round(y)}px)`;
    const ex = clamp(p.x, x, x + w), ey = clamp(p.y, y, y + h);
    note.ln.setAttribute('x1', p.x.toFixed(1)); note.ln.setAttribute('y1', p.y.toFixed(1));
    note.ln.setAttribute('x2', ex.toFixed(1)); note.ln.setAttribute('y2', ey.toFixed(1));
    note.pin.setAttribute('cx', p.x.toFixed(1)); note.pin.setAttribute('cy', p.y.toFixed(1));
  }
  /* the view calls this every frame, after it has drawn and placed its labels */
  P.placeOverlays = () => { placeCallout(); placeNote(); };
  /* and this whenever the box changes size */
  P.measure = () => {
    fixedRects.length = 0;
    for (const el of [$('[data-view-ctl]'), $('[data-hint]')]) {
      if (el && el.offsetParent) fixedRects.push({ x: el.offsetLeft, y: el.offsetTop, w: el.offsetWidth, h: el.offsetHeight });
    }
    if (note.at) { note.w = note.box.offsetWidth; note.h = note.box.offsetHeight; }
  };

  /* ---------- visibility, first view and autoplay ---------- */
  function firstView() {
    started = true;
    if (RM.matches) return;
    if (view.firstView) view.firstView();
    /* then the flows play on their own, chained 1 to 4 and round again, unless someone has taken over */
    autoTimer = setTimeout(() => { if (!userPaused && !P.playing && !RM.matches) play(); }, view.autoplayDelay || 600);
  }
  const io = new IntersectionObserver(entries => {
    const e = entries[entries.length - 1];
    P.visible = e.isIntersecting;
    if (P.visible) view.wake();
    if (!started && e.intersectionRatio >= 0.3) firstView();
  }, { threshold: [0, 0.3] });
  on(document, 'visibilitychange', () => { if (!document.hidden) view.wake(); });
  on(RM, 'change', () => {
    if (RM.matches) { pause(false); P.single = false; enter(fi, Math.max(0, si), false); }
    view.wake();
  });

  /* ---------- start: reduced motion shows step 1 and its whole list; otherwise the title card of flow 1 ---------- */
  P.start = () => {
    pause(false);
    if (RM.matches) enter(0, 0, false); else titleCard(0, false);
    P.measure();
    io.observe(box);
  };
  /* a view that fails after start-up hands over to the next tier: drop every listener and timer */
  P.destroy = () => {
    ac.abort();
    io.disconnect();
    clearTimeout(autoTimer);
    P.playing = false; P.single = false;
    hideCallout(); hideNote();
    [co, note.box].forEach(el => el.style.removeProperty('transform'));
  };
  return P;
}
