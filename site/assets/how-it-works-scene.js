/* SISF · "How it works": the Three.js view (tier 1 of 3).

   Loaded by app.js only when the section nears the viewport and the browser has WebGL.
   One deployment as three floating floors, one ring for the self-improvement loop, three
   outside actors. The flow player (how-it-works-player.js) owns the words, the timing and
   the controls; this file draws what the player shows and answers its geometry questions.

   If anything here fails (no context, a failed import, a lost context later on), the view
   cleans up after itself and raises "sisf:how-fallback": app.js then loads the isometric
   drawing, which reads the same flows. Sections:
     1 setup · 2 objects · 3 motion · 4 labels and picking · 5 loop, sizing, lifecycle */
import * as THREE from './vendor/three/three-0.170.0.module.min.js';
import { OrbitControls } from './vendor/three/OrbitControls-0.170.0.js';
import { createPlayer, RM, MOB, clamp, easeIO } from './how-it-works-player.js?v=20261111';

/* Scene layout in scene units (generated from the same model as the isometric drawing). */
const LAYOUT = {"slab":{"w":7.4,"d":5.2,"t":0.22},"yExp":[0.0,2.35,4.7],"yCol":[0.0,1.06,2.12],"cam":{"az":40,"polar":62,"fov":28},"ring":{"r":5.75,"below":0.4,"above":0.6},"units":{"app":{"zone":1,"parts":[[-0.6,1.5,2.8,1.4,0.55,"stable"],[1.75,1.5,1.3,1.4,0.55,"canary"],[3.2,0.1,0.34,2.8,0.3,"edge"]],"anchor":[-0.6,0.55,1.5]},"dispatcher":{"zone":2,"parts":[[-2.2,1.35,1.7,1.4,0.5]],"anchor":[-2.2,0.5,1.35]},"runner":{"zone":2,"parts":[[0.45,1.45,1.0,1.0,0.48]],"anchor":[0.45,0.48,1.45]},"qa-runner":{"zone":2,"parts":[[1.85,1.45,1.0,1.0,0.48]],"anchor":[1.85,0.48,1.45]},"egress":{"zone":2,"parts":[[3.35,0.2,0.24,0.8,0.62]],"anchor":[3.35,0.62,0.2]},"control":{"zone":3,"parts":[[-2.0,0.85,2.2,1.6,0.55]],"anchor":[-2.0,0.55,0.85]},"decision":{"zone":3,"parts":[[-2.15,-1.35,1.7,1.1,0.42]],"anchor":[-2.15,0.42,-1.35]},"approvals":{"zone":3,"parts":[[1.75,-1.45,1.1,0.9,0.34]],"anchor":[1.75,0.34,-1.45]},"supervisor":{"zone":3,"parts":[[1.52,0.97,1.0,0.9,0.5]],"anchor":[1.52,0.78,0.97]}},"sandbox":[1.45,0.65,3.8,3.3,0.32],"vault":[1.52,0.97,1.85,1.85,0.78],"column":{"x":3.05,"z":-1.95,"r":0.28,"top":0.75},"actors":{"github":{"pos":[-5.8,1.5,0.5],"zone":3,"to":["control",[-3.1,0.3,0.85]]},"owner":{"pos":[3.7,1.6,-3.9],"zone":3,"to":["approvals",[1.75,0.34,-1.9]]},"users":{"pos":[7.2,0.3,-0.6],"zone":1,"to":["app",[3.37,0.15,0.1]]}}};

const root = document.documentElement;
const host = document.querySelector('[data-loop]');
const fallback = why => document.dispatchEvent(new CustomEvent('sisf:how-fallback', { detail: { from: '3d', why: String((why && why.message) || why) } }));

if (!host) fallback('no section');
else {
  try { start(); } catch (err) { fallback(err); }
}

function start() {
  /* ======================= 1. setup: renderer, camera, controls, palette ======================= */
  const $ = (s, r = host) => r.querySelector(s);
  const $$ = (s, r = host) => Array.from(r.querySelectorAll(s));
  const lerp = (a, b, t) => a + (b - a) * t;
  const DEG = Math.PI / 180;
  const SYS_DARK = matchMedia('(prefers-color-scheme: dark)');

  const sceneEl = $('[data-scene]');
  const frameEl = $('[data-frame]');
  const labelsEl = $('[data-labels]');
  const canvas = document.createElement('canvas');
  canvas.className = 'loop-canvas';
  canvas.setAttribute('aria-hidden', 'true');

  /* throws when no context can be made: the caller falls back before anything is on the page */
  const renderer = new THREE.WebGLRenderer({ canvas, antialias: true, alpha: true, powerPreference: 'low-power' });
  let P = null, cleanups = [];
  try {
    build();
  } catch (err) {
    teardown();
    throw err;
  }

  /* undo everything this view put on the page, so the next tier starts from the markup */
  function teardown() {
    cleanups.forEach(fn => { try { fn(); } catch (e) { /* keep going */ } });
    cleanups = [];
    if (P) P.destroy();
    try { renderer.dispose(); } catch (e) { /* context may already be gone */ }
    canvas.remove();
    $$('[data-zone], [data-actor]', labelsEl).forEach(el => { el.style.removeProperty('transform'); el.classList.remove('is-away'); });
    if (root.getAttribute('data-how') === '3d') root.setAttribute('data-how', 'wait');
  }

  function build() {
  renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 2));   // DPR cap: crisp enough, half the fill cost of 3x
  renderer.setClearColor(0x000000, 0);           // the page's paper shows through
  renderer.shadowMap.enabled = true;
  renderer.shadowMap.type = THREE.PCFSoftShadowMap;

  const scene = new THREE.Scene();
  const camera = new THREE.PerspectiveCamera(LAYOUT.cam.fov, 1.6, 1, 200);
  const AZ0 = LAYOUT.cam.az * DEG, POLAR0 = LAYOUT.cam.polar * DEG;

  /* Orbit within limits: no zoom (the page keeps its scroll wheel), no pan, a bounded arc. */
  const controls = new OrbitControls(camera, canvas);
  controls.enableZoom = false;
  controls.enablePan = false;
  controls.enableDamping = !RM.matches;
  controls.dampingFactor = 0.08;
  controls.rotateSpeed = 0.5;
  controls.minAzimuthAngle = AZ0 - 0.95;
  controls.maxAzimuthAngle = AZ0 + 0.95;
  controls.minPolarAngle = 42 * DEG;
  controls.maxPolarAngle = 76 * DEG;
  /* Touch: a vertical swipe scrolls the page as usual; only a sideways drag turns the scene.
     (OrbitControls sets touch-action: none when it connects, so this comes after it.) */
  canvas.style.touchAction = 'pan-y';

  /* palette: read from the section's CSS tokens, so light and dark live in one place */
  const isDark = () => (root.dataset.theme || (SYS_DARK.matches ? 'dark' : 'light')) === 'dark';
  const pal = {};
  function readPalette() {
    const cs = getComputedStyle(host);
    const col = n => new THREE.Color(cs.getPropertyValue(n).trim() || '#888');
    const num = (n, d) => { const v = parseFloat(cs.getPropertyValue(n)); return Number.isFinite(v) ? v : d; };
    Object.assign(pal, {
      paper: col('--paper'), line: col('--line'), signal: col('--g-signal'), fault: col('--fault'),
      slab: col('--g-slab'), unit: col('--g-unit'), edge: col('--g-edge'), glass: col('--g-glass'),
      on: col('--g-on'), flt: col('--g-flt'),
      edgeA: num('--g-edge-a', 0.8), slabA: num('--g-slab-a', 0.5), glassA: num('--g-glass-a', 0.07),
      shadowA: num('--g-shadow-a', 0.13), hemi: num('--g-hemi', 2.3), key: num('--g-key', 1.1),
    });
    pal.shadow = isDark() ? new THREE.Color('#000610') : pal.edge.clone();
  }
  readPalette();

  /* lights: soft sky light, one shadow-casting key from the upper left, a weak fill */
  const hemi = new THREE.HemisphereLight(0xffffff, 0xdde5ee, pal.hemi);
  const key = new THREE.DirectionalLight(0xffffff, pal.key);
  key.position.set(-6, 15, 9);
  key.castShadow = true;
  key.shadow.mapSize.set(2048, 2048);
  Object.assign(key.shadow.camera, { left: -10, right: 10, top: 10, bottom: -10, near: 1, far: 50 });
  key.shadow.bias = -0.0004;
  key.shadow.normalBias = 0.02;
  const fill = new THREE.DirectionalLight(0xffffff, 0.35);
  fill.position.set(10, 4, -3);
  scene.add(hemi, key, fill);

  /* materials, recoloured on a theme change by applyPalette */
  const M = {
    slab: new THREE.MeshLambertMaterial({ color: pal.slab }),
    slabEdge: [1, 2, 3].map(() => new THREE.LineBasicMaterial({ color: pal.edge, transparent: true, opacity: pal.slabA })),
    grid: new THREE.LineBasicMaterial({ color: pal.edge, transparent: true, opacity: pal.slabA * 0.16 }),
    glass: new THREE.MeshLambertMaterial({ color: pal.glass, transparent: true, opacity: pal.glassA, depthWrite: false, side: THREE.DoubleSide }),
    glassEdge: new THREE.LineDashedMaterial({ color: pal.edge, transparent: true, opacity: pal.edgeA * 0.7, dashSize: 0.12, gapSize: 0.08 }),
    vaultEdge: new THREE.LineBasicMaterial({ color: pal.edge, transparent: true, opacity: pal.edgeA }),
    none: new THREE.MeshBasicMaterial({ visible: false }),
    shadow: new THREE.ShadowMaterial({ color: pal.shadow, opacity: pal.shadowA }),
    ring: new THREE.MeshBasicMaterial({ color: pal.line, transparent: true, opacity: 0.55 }),
    ringOn: new THREE.MeshBasicMaterial({ color: pal.signal }),
    chev: new THREE.MeshBasicMaterial({ color: pal.line, transparent: true, opacity: 0.85 }),
    chevOn: new THREE.MeshBasicMaterial({ color: pal.signal }),
    bead: new THREE.MeshBasicMaterial({ color: pal.signal }),
    halo: new THREE.MeshBasicMaterial({ color: pal.signal, transparent: true, opacity: 0.16, depthWrite: false }),
    ghost: new THREE.MeshBasicMaterial({ color: pal.line, transparent: true, opacity: 0.8, depthWrite: false }),
    tether: new THREE.LineDashedMaterial({ color: pal.signal, transparent: true, opacity: 0.9, dashSize: 0.14, gapSize: 0.1 }),
    link: new THREE.LineDashedMaterial({ color: pal.line, transparent: true, opacity: 0.75, dashSize: 0.1, gapSize: 0.09 }),
    bound: new THREE.LineDashedMaterial({ color: pal.line, transparent: true, opacity: 0.4, dashSize: 0.34, gapSize: 0.16 }),
    dot: new THREE.MeshBasicMaterial({ color: pal.line }),
    beam: new THREE.MeshBasicMaterial({ color: pal.fault, transparent: true, opacity: 0.85, depthTest: false }),
  };

  /* hatch for the trusted vault's lid: diagonal hairlines on a transparent canvas */
  const hatchCanvas = document.createElement('canvas');
  hatchCanvas.width = hatchCanvas.height = 128;
  const hatchTex = new THREE.CanvasTexture(hatchCanvas);
  hatchTex.wrapS = hatchTex.wrapT = THREE.RepeatWrapping;
  hatchTex.repeat.set(2, 2);
  hatchTex.colorSpace = THREE.SRGBColorSpace;
  function drawHatch() {
    const g = hatchCanvas.getContext('2d');
    g.clearRect(0, 0, 128, 128);
    g.strokeStyle = '#' + pal.edge.getHexString(THREE.SRGBColorSpace);
    g.globalAlpha = isDark() ? 0.42 : 0.5;
    g.lineWidth = 2.2;
    for (let i = -128; i <= 256; i += 16) { g.beginPath(); g.moveTo(i, 0); g.lineTo(i - 128, 128); g.stroke(); }
    hatchTex.needsUpdate = true;
  }
  drawHatch();
  M.hatch = new THREE.MeshBasicMaterial({ map: hatchTex, transparent: true, depthWrite: false, side: THREE.DoubleSide });

  /* ======================= 2. objects: everything in the scene, built once from LAYOUT ======================= */
  const S = LAYOUT.slab;
  const R = LAYOUT.ring.r;
  const COL = LAYOUT.column;

  function boxGeo(w, h, d) { const g = new THREE.BoxGeometry(w, h, d); g.translate(0, h / 2, 0); return g; }
  function edgesOf(geo, mat) {
    const e = new THREE.LineSegments(new THREE.EdgesGeometry(geo, 20), mat);
    if (mat.isLineDashedMaterial) e.computeLineDistances();
    return e;
  }
  function solid(geo, mat, edgeMat, shadows = true) {
    const m = new THREE.Mesh(geo, mat);
    m.castShadow = m.receiveShadow = shadows;
    m.add(edgesOf(geo, edgeMat));
    return m;
  }
  function lineGeo(n) {
    const g = new THREE.BufferGeometry();
    g.setAttribute('position', new THREE.BufferAttribute(new Float32Array(n * 3), 3));
    return g;
  }

  /* the three floors: one group per zone; everything on a floor rides with it */
  function slabGrid() {
    const pts = [], step = 0.6, hw = S.w / 2 - 0.3, hd = S.d / 2 - 0.3, y = 0.004;
    for (let x = -hw; x <= hw + 1e-6; x += step) pts.push(x, y, -hd, x, y, hd);
    for (let z = -hd; z <= hd + 1e-6; z += step) pts.push(-hw, y, z, hw, y, z);
    const geo = new THREE.BufferGeometry();
    geo.setAttribute('position', new THREE.Float32BufferAttribute(pts, 3));
    return new THREE.LineSegments(geo, M.grid);
  }
  const slabs = [1, 2, 3].map(z => {
    const g = new THREE.Group();
    const geo = new THREE.BoxGeometry(S.w, S.t, S.d);
    geo.translate(0, -S.t / 2, 0);
    const m = new THREE.Mesh(geo, M.slab);
    m.castShadow = m.receiveShadow = true;
    m.add(edgesOf(geo, M.slabEdge[z - 1]));
    g.add(m, slabGrid());
    scene.add(g);
    return g;
  });
  const blockers = slabs.map(g => g.children[0]);   // a floor in front of a part hides it from picking

  /* units */
  const units = {};
  const pickables = [];
  const EPHEMERAL = new Set(['runner', 'qa-runner']);   // exist only during a run: dashed until active
  const canary = {
    mat: new THREE.MeshLambertMaterial({ color: pal.unit, transparent: true, opacity: 0.3 }),
    edge: new THREE.LineDashedMaterial({ color: pal.edge, transparent: true, opacity: pal.edgeA * 0.6, dashSize: 0.1, gapSize: 0.07 }),
    mesh: null,
  };
  function addUnit(id, spec) {
    const eph = EPHEMERAL.has(id);
    const mat = new THREE.MeshLambertMaterial({ color: pal.unit, transparent: eph, opacity: eph ? 0.55 : 1 });
    const edge = eph
      ? new THREE.LineDashedMaterial({ color: pal.edge, transparent: true, opacity: pal.edgeA, dashSize: 0.09, gapSize: 0.06 })
      : new THREE.LineBasicMaterial({ color: pal.edge, transparent: true, opacity: pal.edgeA });
    const grp = new THREE.Group();
    slabs[spec.zone - 1].add(grp);
    const U = { id, kind: 'unit', zone: spec.zone, grp, mat, edge, eph, meshes: [], anchor: new THREE.Vector3(...spec.anchor) };
    for (const [cx, cz, w, d, h, role] of spec.parts) {
      const m = role === 'canary' ? solid(boxGeo(w, h, d), canary.mat, canary.edge) : solid(boxGeo(w, h, d), mat, edge);
      if (role === 'canary') canary.mesh = m;
      m.position.set(cx, 0, cz);
      m.userData.unit = id;
      grp.add(m);
      U.meshes.push(m);
      pickables.push(m);
    }
    units[id] = U;
  }
  for (const [id, spec] of Object.entries(LAYOUT.units)) addUnit(id, spec);

  /* a tiny error-rate chart on the app: the last bar is the endpoint that climbs, then recovers */
  const bars = [0.16, 0.22, 0.18, 0.2].map((h0, i) => {
    const mat = new THREE.MeshLambertMaterial({ color: pal.line });
    const m = new THREE.Mesh(boxGeo(0.13, 1, 0.13), mat);
    m.position.set(-1.65 + i * 0.22, 0.55, 1.75);
    m.scale.y = h0;
    m.castShadow = true;
    m.userData.unit = 'app';
    units.app.grp.add(m);
    return { m, mat, h0 };
  });

  /* Zone 2's sandbox network segment: low glass walls, dashed rim */
  {
    const [cx, cz, w, d, h] = LAYOUT.sandbox;
    const geo = boxGeo(w, h, d);
    const m = new THREE.Mesh(geo, [M.glass, M.glass, M.none, M.none, M.glass, M.glass]);
    m.position.set(cx, 0, cz);
    m.add(edgesOf(geo, M.glassEdge));
    m.renderOrder = 2;
    slabs[1].add(m);
  }

  /* the trusted supervisor's vault: glass walls, a hatched lid, solid edges */
  {
    const [cx, cz, w, d, h] = LAYOUT.vault;
    const geo = boxGeo(w, h, d);
    const m = new THREE.Mesh(geo, [M.glass, M.glass, M.hatch, M.none, M.glass, M.glass]);
    m.position.set(cx, 0, cz);
    m.add(edgesOf(geo, M.vaultEdge));
    m.renderOrder = 3;
    slabs[2].add(m);
  }

  /* the lock that appears over the vault when the factory changes itself */
  const lock = (() => {
    const g = new THREE.Group();
    const mat = new THREE.MeshLambertMaterial({ color: pal.on });
    const edge = new THREE.LineBasicMaterial({ color: pal.signal });
    const body = solid(boxGeo(0.46, 0.36, 0.18), mat, edge, false);
    const shackle = new THREE.Mesh(new THREE.TorusGeometry(0.14, 0.045, 10, 28, Math.PI), new THREE.MeshBasicMaterial({ color: pal.signal }));
    shackle.position.y = 0.36;
    g.add(body, shackle);
    g.position.set(LAYOUT.vault[0], LAYOUT.vault[4] + 0.4, LAYOUT.vault[1]);
    g.userData.size = 1.4;
    g.scale.setScalar(0.001);
    g.visible = false;
    slabs[2].add(g);
    return { g, mat, edge, shackle, s: 0 };
  })();

  /* shared state + artifacts: a glass column through all three floors */
  const colMat = new THREE.MeshLambertMaterial({ color: pal.glass, transparent: true, opacity: pal.glassA * 1.3, depthWrite: false, side: THREE.DoubleSide });
  const tubeGeo = new THREE.CylinderGeometry(COL.r, COL.r, 1, 40, 1, true);
  tubeGeo.translate(0, 0.5, 0);
  const tube = new THREE.Mesh(tubeGeo, colMat);
  tube.renderOrder = 2;
  scene.add(tube);
  const capGeo = new THREE.BufferGeometry().setFromPoints(
    Array.from({ length: 41 }, (_, i) => new THREE.Vector3(Math.cos(i / 40 * Math.PI * 2) * COL.r, 0, Math.sin(i / 40 * Math.PI * 2) * COL.r)));
  const cap = new THREE.Line(capGeo, M.vaultEdge);
  scene.add(cap);
  {
    const mat = new THREE.MeshLambertMaterial({ color: pal.unit });
    const edge = new THREE.LineBasicMaterial({ color: pal.edge, transparent: true, opacity: pal.edgeA });
    const U = { id: 'state', kind: 'unit', zone: 2, grp: new THREE.Group(), mat, edge, eph: false, meshes: [], anchor: new THREE.Vector3(COL.x, 0.32, COL.z) };
    slabs[1].add(U.grp);
    slabs.forEach((g, i) => {
      const collar = solid(new THREE.CylinderGeometry(COL.r + 0.12, COL.r + 0.12, 0.13, 40).translate(0, 0.065, 0), mat, edge);
      collar.position.set(COL.x, 0, COL.z);
      collar.userData.unit = 'state';
      (i === 1 ? U.grp : g).add(collar);
      U.meshes.push(collar);
      pickables.push(collar);
    });
    units.state = U;
  }

  /* outside actors: small pucks with a dashed line to their port */
  for (const [id, a] of Object.entries(LAYOUT.actors)) {
    const mat = new THREE.MeshLambertMaterial({ color: pal.unit });
    const edge = new THREE.LineBasicMaterial({ color: pal.edge, transparent: true, opacity: pal.edgeA });
    const grp = new THREE.Group();
    const puck = solid(new THREE.CylinderGeometry(0.34, 0.34, 0.12, 40), mat, edge, false);
    puck.userData.unit = id;
    grp.add(puck);
    grp.position.set(...a.pos);
    slabs[a.zone - 1].add(grp);
    pickables.push(puck);
    const port = new THREE.Vector3(...a.to[1]);
    const link = new THREE.Line(new THREE.BufferGeometry().setFromPoints([new THREE.Vector3(...a.pos), port]), M.link);
    link.computeLineDistances();
    const dot = new THREE.Mesh(new THREE.SphereGeometry(0.06, 12, 8), M.dot);
    dot.position.copy(port);
    slabs[a.zone - 1].add(link, dot);
    units[id] = { id, kind: 'actor', zone: a.zone, grp, mat, edge, eph: false, meshes: [puck], anchor: new THREE.Vector3(0, 0.1, 0), port };
  }

  /* the deployment boundary: one dashed box around all three zones */
  const bound = new THREE.LineSegments(lineGeo(24), M.bound);
  scene.add(bound);
  let boundK = -1;
  function rebuildBound(y0, y1) {
    const x = 4.25, z = 3.1, p = bound.geometry.attributes.position.array;
    const c = [[-x, y0, -z], [x, y0, -z], [x, y0, z], [-x, y0, z], [-x, y1, -z], [x, y1, -z], [x, y1, z], [-x, y1, z]];
    const e = [0, 1, 1, 2, 2, 3, 3, 0, 4, 5, 5, 6, 6, 7, 7, 4, 0, 4, 1, 5, 2, 6, 3, 7];
    e.forEach((ci, i) => { p[i * 3] = c[ci][0]; p[i * 3 + 1] = c[ci][1]; p[i * 3 + 2] = c[ci][2]; });
    bound.geometry.attributes.position.needsUpdate = true;
    bound.geometry.computeBoundingSphere();
    bound.computeLineDistances();
  }

  /* the loop: one ring around the whole stack, low by Zone 1, high by Zone 3 */
  class Arc extends THREE.Curve {
    constructor(a0) { super(); this.a0 = a0; }
    getPoint(t, out = new THREE.Vector3()) { const a = this.a0 + t * Math.PI * 2; return out.set(R * Math.cos(a), 0, R * Math.sin(a)); }
  }
  const RING_SEG = 240;
  const ringG = new THREE.Group();
  scene.add(ringG);
  ringG.add(new THREE.Mesh(new THREE.TubeGeometry(new Arc(0), RING_SEG, 0.022, 6, true), M.ring));
  const ringProg = new THREE.Mesh(new THREE.TubeGeometry(new Arc(0), RING_SEG, 0.045, 8, false), M.ringOn);
  ringProg.geometry.setDrawRange(0, 0);
  ringG.add(ringProg);
  let progT0 = 0;
  function setProgressStart(t0) {
    if (t0 === progT0) return;
    ringProg.geometry.dispose();
    ringProg.geometry = new THREE.TubeGeometry(new Arc(t0 * Math.PI * 2), RING_SEG, 0.045, 8, false);
    ringProg.geometry.setDrawRange(0, 0);
    progT0 = t0;
  }
  /* direction chevrons, so the loop reads clockwise at a glance */
  const chevrons = Array.from({ length: 6 }, (_, k) => {
    const a = (k + 0.5) * Math.PI / 3;
    const m = new THREE.Mesh(new THREE.ConeGeometry(0.1, 0.28, 14), M.chev);
    m.position.set(R * Math.cos(a), 0, R * Math.sin(a));
    m.quaternion.setFromUnitVectors(new THREE.Vector3(0, 1, 0), new THREE.Vector3(-Math.sin(a), 0, Math.cos(a)));
    ringG.add(m);
    return { m, t: a / (Math.PI * 2) };
  });
  const _rp = new THREE.Vector3();
  function ringPoint(t, out = _rp) {
    const a = t * Math.PI * 2;
    out.set(R * Math.cos(a), 0, R * Math.sin(a));
    return ringG.localToWorld(out);
  }

  /* the work item, its ghost (the counter-case), and up to three tethers to the units at work */
  const bead = new THREE.Group();
  const beadCore = new THREE.Mesh(new THREE.SphereGeometry(0.16, 24, 16), M.bead);
  const beadHalo = new THREE.Mesh(new THREE.SphereGeometry(0.34, 24, 16), M.halo);
  bead.add(beadCore, beadHalo);
  bead.visible = false;
  scene.add(bead);
  const ghost = new THREE.Mesh(new THREE.SphereGeometry(0.12, 20, 14), M.ghost);
  ghost.visible = false;
  scene.add(ghost);
  M.tetherF = M.tether.clone();
  M.tetherF.color = pal.fault.clone();
  const tethers = Array.from({ length: 3 }, () => {
    const l = new THREE.Line(lineGeo(2), M.tether);
    l.visible = false;
    l.frustumCulled = false;
    scene.add(l);
    return l;
  });

  /* user traffic: two small dots taking turns, a heartbeat rather than a swarm */
  const usersA = LAYOUT.actors.users;
  const traffic = [0, 1].map(() => {
    const m = new THREE.Mesh(new THREE.SphereGeometry(0.07, 12, 8), M.dot);
    m.visible = false;
    slabs[0].add(m);
    return m;
  });
  const trafficFrom = new THREE.Vector3(...usersA.pos);
  const trafficTo = new THREE.Vector3(...usersA.to[1]);
  const trafficMid = trafficFrom.clone().lerp(trafficTo, 0.5).add(new THREE.Vector3(0, 0.55, 0));

  /* rollback: the last known good travels from the supervisor down to the app */
  const beam = new THREE.Mesh(new THREE.CylinderGeometry(0.05, 0.05, 1, 10).translate(0, 0.5, 0), M.beam);
  beam.visible = false;
  beam.renderOrder = 10;                    // drawn over the floors it passes, so it reads as one path
  scene.add(beam);

  /* a soft shadow on the "table" under the stack */
  const ground = new THREE.Mesh(new THREE.PlaneGeometry(40, 40), M.shadow);
  ground.rotation.x = -Math.PI / 2;
  ground.position.y = -1.1;
  ground.receiveShadow = true;
  scene.add(ground);

  /* ======================= 3. motion: per-frame layout, unit states, the loop, the camera ======================= */
  const ambientOn = () => !RM.matches;
  let explodeK = 0;                 // 0 = assembled into one building, 1 = floors lifted apart
  const ring = { yc: 0, amp: 0 };
  const DL = new THREE.Vector3(Math.sin(AZ0), 0, Math.cos(AZ0));    // the ring's low point faces the default view
  const VV = new THREE.Vector3(-Math.cos(AZ0), 0, Math.sin(AZ0));   // and it climbs on the viewer's left: clockwise
  const _u = new THREE.Vector3(), _n = new THREE.Vector3(), _m4 = new THREE.Matrix4();
  const _a = new THREE.Vector3(), _b = new THREE.Vector3(), _q = new THREE.Vector3();
  const UP = new THREE.Vector3(0, 1, 0);
  const baseY = z => lerp(LAYOUT.yCol[z - 1], LAYOUT.yExp[z - 1], easeIO(explodeK));

  function layout(nowMs) {
    const k = easeIO(explodeK);
    slabs.forEach((g, i) => {
      const float = ambientOn() ? 0.05 * k * Math.sin(nowMs / 1000 * 0.9 + i * 2.1) : 0;
      g.position.y = baseY(i + 1) + float;
    });
    const lo = baseY(1) - LAYOUT.ring.below, hi = baseY(3) + LAYOUT.ring.above;
    ring.yc = (lo + hi) / 2;
    ring.amp = (hi - lo) / 2;
    const s = clamp(ring.amp / R, 0, 0.95), c = Math.sqrt(1 - s * s);
    _u.set(DL.x * c, -s, DL.z * c);
    _n.crossVectors(VV, _u);
    _m4.makeBasis(_u, _n, VV);
    ringG.quaternion.setFromRotationMatrix(_m4);
    ringG.position.set(0, ring.yc, 0);
    ringG.updateMatrixWorld(true);
    const y0 = slabs[0].position.y, y1 = slabs[2].position.y + COL.top;
    tube.position.set(COL.x, y0, COL.z);
    tube.scale.set(1, y1 - y0, 1);
    cap.position.set(COL.x, y1, COL.z);
    if (Math.abs(explodeK - boundK) > 1e-4) { rebuildBound(baseY(1) - 0.75, baseY(3) + 1.05); boundK = explodeK; }
    scene.updateMatrixWorld(true);
  }

  /* easing of unit colours, lift and dashes */
  let settling = false;
  function near(a, b) { return Math.abs(a - b) < 0.004; }
  function ease3(cur, tgt, f) { cur.lerp(tgt, f); return Math.abs(cur.r - tgt.r) + Math.abs(cur.g - tgt.g) + Math.abs(cur.b - tgt.b) < 0.01; }
  function easeNum(obj, k, tgt, f) { obj[k] = lerp(obj[k], tgt, f); if (near(obj[k], tgt)) { obj[k] = tgt; return true; } return false; }

  function updateUnits(dt) {
    const show = P.show;
    const f = RM.matches ? 1 : 1 - Math.exp(-dt / 150);
    const flowOn = show.active.size > 0;
    const restart = show.fx.has('restart');
    const restartDim = restart && (RM.matches || P.clock - show.fxAt < 2600);
    let still = true;
    for (const U of Object.values(units)) {
      const on = show.active.has(U.id);
      const hot = P.hoverId === U.id || P.pinnedId === U.id || P.kbId === U.id;
      const flaw = U.id === 'control' && show.fx.has('flaw');
      const dimmed = restartDim && (U.zone === 2 || U.zone === 3) && U.kind === 'unit' && !on;
      const face = on || flaw ? pal.on : pal.unit;
      const edgeC = on || flaw ? pal.signal : pal.edge;
      let edgeA = on || hot ? 1 : flowOn ? pal.edgeA * 0.55 : pal.edgeA;
      if (dimmed) edgeA = pal.edgeA * 0.22;
      still = ease3(U.mat.color, face, f) && still;
      still = ease3(U.edge.color, edgeC, f) && still;
      still = easeNum(U.edge, 'opacity', edgeA, f) && still;
      if (U.kind === 'unit' && U.id !== 'state') still = easeNum(U.grp.position, 'y', on ? 0.07 : hot ? 0.035 : 0, f) && still;
      if (U.eph) {
        still = easeNum(U.mat, 'opacity', on ? 1 : 0.55, f) && still;
        still = easeNum(U.edge, 'gapSize', on ? 0 : 0.06, f) && still;
      }
    }
    /* the canary revision: a dashed ghost until a release, red when it fails */
    const cOn = show.fx.has('canary'), cFlt = show.fx.has('fault');
    const appOn = show.active.has('app');
    still = ease3(canary.mat.color, cFlt ? pal.flt : cOn && appOn ? pal.on : pal.unit, f) && still;
    still = ease3(canary.edge.color, cFlt ? pal.fault : cOn && appOn ? pal.signal : pal.edge, f) && still;
    still = easeNum(canary.mat, 'opacity', cOn ? 1 : 0.28, f) && still;
    still = easeNum(canary.edge, 'opacity', cOn ? 1 : pal.edgeA * 0.6, f) && still;
    still = easeNum(canary.edge, 'gapSize', cOn ? 0 : 0.07, f) && still;
    canary.mesh.castShadow = cOn;
    /* the error-rate bar climbs while the issue is open, and settles when it is fixed */
    const err = show.fx.has('err');
    bars.forEach((b, i) => {
      const last = i === bars.length - 1;
      /* assembled, the floor above is close: the bar stays short enough to fit under it */
      still = easeNum(b.m.scale, 'y', last && err ? lerp(0.26, 0.85, easeIO(explodeK)) : b.h0, f) && still;
      still = ease3(b.mat.color, last && err ? pal.signal : pal.line, f) && still;
    });
    /* restart: the floors of Zones 2 and 3 blink while Zone 1 stays steady */
    [1, 2].forEach(i => {
      let a = pal.slabA;
      if (restart && !RM.matches && P.clock - show.fxAt < 2600) a = pal.slabA * (0.25 + 0.75 * (0.5 + 0.5 * Math.cos((P.clock - show.fxAt) / 1300 * Math.PI * 2)));
      else if (restartDim) a = pal.slabA * 0.35;
      M.slabEdge[i].opacity = a;
    });
    if (restart && P.clock - show.fxAt < 2600 && !RM.matches) still = false;
    /* the lock */
    const lk = show.fx.has('lock') ? 1 : 0;
    lock.s = RM.matches ? lk : lerp(lock.s, lk, f);
    if (near(lock.s, lk)) lock.s = lk; else still = false;
    lock.g.visible = lock.s > 0.01;
    lock.g.scale.setScalar(Math.max(0.001, lock.s * lock.g.userData.size));
    settling = !still;
  }

  /* the loop: bead, progress, chevrons, ghost, tethers, beam */
  function updateLoop(nowMs) {
    const show = P.show;
    const t = P.beadT();
    show.bead.t = t;
    const fault = show.beadKind === 'fault';
    const colr = fault ? pal.fault : pal.signal;
    M.bead.color.copy(colr);
    M.halo.color.copy(colr);
    M.ringOn.color.copy(colr);
    M.chevOn.color.copy(colr);
    bead.visible = show.beadOn;
    if (show.beadOn) {
      bead.position.copy(ringPoint(((t % 1) + 1) % 1));
      beadHalo.scale.setScalar(ambientOn() ? 1 + 0.12 * Math.sin(nowMs / 260) : 1);
    }
    const frac = show.beadOn ? clamp(t - progT0, 0, 1) : 0;
    ringProg.geometry.setDrawRange(0, Math.round(frac * RING_SEG) * 8 * 6);
    chevrons.forEach(c => {
      const ct = c.t >= progT0 ? c.t : c.t + 1;
      c.m.material = show.beadOn && ct <= t + 1e-6 && frac > 0 ? M.chevOn : M.chev;
    });
    /* counter-case: with thin evidence the work item slips back to watching */
    if (show.ghost && !RM.matches) {
      const p = (P.clock - show.ghost.t0) / show.ghost.dur;
      ghost.visible = p > 0 && p < 1;
      if (ghost.visible) {
        ghost.position.copy(ringPoint(show.ghost.from * (1 - easeIO(p))));
        M.ghost.opacity = 0.85 * (1 - Math.max(0, p - 0.7) / 0.3);
      }
    } else ghost.visible = false;
    /* tethers from the work item to the units at work */
    const ids = [...show.active].slice(0, 3);
    tethers.forEach((l, i) => {
      const U = units[ids[i]];
      l.visible = show.beadOn && !!U;
      if (!l.visible) return;
      l.material = fault ? M.tetherF : M.tether;
      unitAnchor(U, _b);
      const p = l.geometry.attributes.position.array;
      p[0] = bead.position.x; p[1] = bead.position.y; p[2] = bead.position.z;
      p[3] = _b.x; p[4] = _b.y; p[5] = _b.z;
      l.geometry.attributes.position.needsUpdate = true;
      l.computeLineDistances();
    });
    /* rollback: the last known good comes down from the supervisor to the app */
    const lkg = show.fx.has('lkg');
    beam.visible = lkg;
    if (lkg) {
      unitAnchor(units.supervisor, _a);
      unitAnchor(units.app, _b);
      const grow = RM.matches ? 1 : clamp((P.clock - show.fxAt) / 1100, 0, 1);
      _q.subVectors(_b, _a);
      const len = _q.length();
      beam.position.copy(_a);
      beam.quaternion.setFromUnitVectors(UP, _q.normalize());
      beam.scale.set(1, Math.max(0.001, len * easeIO(grow)), 1);
      if (grow < 1) settling = true;
    }
  }
  function unitAnchor(U, out) {
    out.copy(U.anchor);
    return U.grp.localToWorld(out);
  }

  /* user traffic: a slow heartbeat from the users to the edge, always on (Zone 1 keeps serving) */
  const _tp = new THREE.Vector3();
  function updateTraffic(nowMs) {
    traffic.forEach((m, i) => {
      if (!ambientOn()) { m.visible = false; return; }
      const ph = ((nowMs / 1000) + i * 1.4) % 2.8;
      m.visible = ph < 1.4;
      if (!m.visible) return;
      const p = easeIO(ph / 1.4);
      _tp.copy(trafficFrom).multiplyScalar((1 - p) * (1 - p)).addScaledVector(trafficMid, 2 * (1 - p) * p).addScaledVector(trafficTo, p * p);
      m.position.copy(_tp);
    });
  }

  /* camera: user orbit (OrbitControls), plus gentle reframing layered on top */
  const cam = {
    fit: 30,                                   // distance that frames the whole scene for this aspect
    focus: 0, focusFrom: 0, focusTo: 0, focusT0: 0, focusDur: 900,
    focusPt: new THREE.Vector3(),
    nudge: new THREE.Vector3(), nudgeTo: new THREE.Vector3(),
    reset: null,                               // { t0, dur, theta, phi }
    moving: false,
  };
  const _off = new THREE.Vector3(), _sph = new THREE.Spherical(), _tgt = new THREE.Vector3();
  function startFocus(to) {
    cam.focusFrom = cam.focus;
    cam.focusTo = to;
    cam.focusT0 = performance.now();
    cam.focusDur = RM.matches ? 0 : 900;
  }
  function startReset() {
    _off.copy(camera.position).sub(controls.target);
    _sph.setFromVector3(_off);
    cam.reset = { t0: performance.now(), dur: RM.matches ? 0 : 900, theta: _sph.theta, phi: _sph.phi };
    cam.nudge.set(0, 0, 0);
  }
  function updateCamera(nowMs, dt) {
    let moving = false;
    if (cam.focus !== cam.focusTo) {
      const p = cam.focusDur ? clamp((nowMs - cam.focusT0) / cam.focusDur, 0, 1) : 1;
      cam.focus = lerp(cam.focusFrom, cam.focusTo, easeIO(p));
      if (p >= 1) cam.focus = cam.focusTo; else moving = true;
    }
    if (P.pinnedId && units[P.pinnedId]) { unitAnchor(units[P.pinnedId], cam.focusPt); cam.focusPt.y -= 0.2; }
    if (!RM.matches) {
      cam.nudge.lerp(cam.nudgeTo, 1 - Math.exp(-dt / 700));
      if (cam.nudge.distanceTo(cam.nudgeTo) > 0.002) moving = true;
    } else cam.nudge.set(0, 0, 0);
    _tgt.set(0, ring.yc, 0).add(cam.nudge);
    if (cam.focus > 0) _tgt.lerp(cam.focusPt, cam.focus);
    _off.copy(camera.position).sub(controls.target);
    _sph.setFromVector3(_off);
    if (cam.reset) {
      const r = cam.reset, p = r.dur ? clamp((nowMs - r.t0) / r.dur, 0, 1) : 1, e = easeIO(p);
      _sph.theta = lerp(r.theta, AZ0, e);
      _sph.phi = lerp(r.phi, POLAR0, e);
      if (p >= 1) cam.reset = null; else moving = true;
    }
    _sph.radius = cam.fit * lerp(1, 0.58, cam.focus);
    _sph.makeSafe();
    controls.target.copy(_tgt);
    camera.position.copy(_tgt).add(_off.setFromSpherical(_sph));
    camera.lookAt(_tgt);
    cam.moving = moving;
  }
  function setNudge(ids) {
    cam.nudgeTo.set(0, 0, 0);
    if (!ids.length || MOB.matches) return;
    ids.forEach(id => { if (units[id]) cam.nudgeTo.add(unitAnchor(units[id], _a)); });
    cam.nudgeTo.divideScalar(ids.length).sub(_a.set(0, ring.yc, 0)).multiplyScalar(0.16);
  }

  /* ======================= 4. labels and picking ======================= */
  let W = 1, H = 1;                       // scene box size in CSS pixels
  const _sp = new THREE.Vector3(), _corner = new THREE.Vector3(), _np = new THREE.Vector3();
  function toScreen(v) {
    _sp.copy(v).project(camera);
    return { x: (_sp.x + 1) / 2 * W, y: (1 - _sp.y) / 2 * H, behind: _sp.z > 1 };
  }
  /* persistent labels: three zones, three actors */
  const zoneLbls = $$('[data-zone]', labelsEl).map(el => ({ el, z: +el.dataset.zone, w: 0, h: 0, x: 0, y: 0, away: false }));
  const actorLbls = $$('[data-actor]', labelsEl).map(el => ({ el, id: el.dataset.actor, w: 0, h: 0, x: 0, y: 0 }));
  const ACTOR_SIDE = { github: 'left', owner: 'right', users: 'below' };
  function measureLabels() {
    [...zoneLbls, ...actorLbls].forEach(L => { L.w = L.el.offsetWidth; L.h = L.el.offsetHeight; });
  }
  function placeLabels() {
    const focused = cam.focus > 0.05;     // zoomed in on one part: the zone names step aside
    for (const L of zoneLbls) {
      L.el.classList.toggle('is-away', focused);
      /* hang each zone name off whichever corner of its floor is leftmost on screen */
      const g = slabs[L.z - 1];
      let best = null;
      for (const [sx, sz] of [[-1, -1], [1, -1], [1, 1], [-1, 1]]) {
        _corner.set(sx * S.w / 2, -S.t / 2, sz * S.d / 2);
        const p = toScreen(g.localToWorld(_corner));
        if (!best || p.x < best.x) best = p;
      }
      /* a level marker in the left margin, level with its floor: clear of the ring on wide screens;
         on phones it becomes a small chip the ring passes behind */
      L.x = MOB.matches ? 6 : 16;
      L.y = clamp(best.y - L.h / 2, 4, H - L.h - 4);
      L.away = focused;
      L.el.style.transform = `translate(${Math.round(L.x)}px, ${Math.round(L.y)}px)`;
    }
    for (const L of actorLbls) {
      const p = toScreen(unitAnchor(units[L.id], _corner));
      const side = ACTOR_SIDE[L.id];
      let x = side === 'left' ? p.x - L.w - 14 : side === 'right' ? p.x + 14 : p.x - L.w / 2;
      let y = side === 'below' ? p.y + 14 : p.y - L.h / 2;
      if (x < 4) { x = p.x - L.w / 2; y = p.y - L.h - 14; }
      if (x + L.w > W - 4) { x = p.x - L.w / 2; y = side === 'below' ? y : p.y - L.h - 14; }
      L.x = clamp(x, 4, W - L.w - 4);
      L.y = clamp(y, 4, H - L.h - 4);
      L.el.style.transform = `translate(${Math.round(L.x)}px, ${Math.round(L.y)}px)`;
      L.el.classList.toggle('is-on', P.show.active.has(L.id) || P.pinnedId === L.id);
    }
  }
  const CANARY_AT = (() => { const p = LAYOUT.units.app.parts.find(q => q[5] === 'canary'); return new THREE.Vector3(p[0], p[4], p[1]); })();
  function anchorPx(id) {
    if (id === 'canary') return toScreen(units.app.grp.localToWorld(_np.copy(CANARY_AT)));
    return units[id] ? toScreen(unitAnchor(units[id], _np)) : { x: W / 2, y: H / 2 };
  }

  /* picking: the nearest thing under the pointer decides, so a floor in front blocks */
  const ray = new THREE.Raycaster();
  const ndc = new THREE.Vector2();
  const hitList = [...pickables, ...blockers];
  function pick(cx, cy) {
    const r = canvas.getBoundingClientRect();
    ndc.set((cx - r.left) / r.width * 2 - 1, -((cy - r.top) / r.height) * 2 + 1);
    ray.setFromCamera(ndc, camera);
    const hit = ray.intersectObjects(hitList, false)[0];
    return hit && hit.object.userData.unit ? hit.object.userData.unit : null;
  }
  let down = null, dragging = false;
  const listen = (el, type, fn) => { el.addEventListener(type, fn); cleanups.push(() => el.removeEventListener(type, fn)); };
  listen(canvas, 'pointermove', e => {
    if (e.pointerType !== 'mouse' || dragging) return;
    const id = pick(e.clientX, e.clientY);
    canvas.classList.toggle('is-hover', !!id);
    P.setHover(id);
  });
  listen(canvas, 'pointerleave', () => { canvas.classList.remove('is-hover'); P.setHover(null); });
  listen(canvas, 'pointerdown', e => { down = { x: e.clientX, y: e.clientY, t: performance.now() }; });
  listen(canvas, 'pointerup', e => {
    if (!down) return;
    const tap = Math.hypot(e.clientX - down.x, e.clientY - down.y) < 7 && performance.now() - down.t < 600;
    down = null;
    if (!tap) return;
    const id = pick(e.clientX, e.clientY);
    if (id) P.select(id); else P.closeFocus();
  });
  controls.addEventListener('start', () => { dragging = true; canvas.classList.add('is-drag'); wake(); });
  controls.addEventListener('end', () => { dragging = false; canvas.classList.remove('is-drag'); wake(); });
  /* the actor names are also a way in for the pointer */
  actorLbls.forEach(L => listen(L.el, 'click', () => P.select(L.id)));

  /* ======================= 5. render loop, sizing, view buttons, theme, lifecycle ======================= */
  let raf = 0, lastT = 0, lastRender = 0, dirty = true, camDamping = false;
  let explode = null;                       // { from, to, t0, dur } on the real clock
  const exBtn = $('[data-act="explode"]');

  /* Render on demand: full rate while something moves, about 30 fps for the idle drift and while a
     playing step holds for reading, nothing at all off-screen, in a hidden tab, or idle with reduced motion. */
  function busy() {
    return P.busy() || !!explode || cam.moving || !!cam.reset || dragging || camDamping || settling;
  }
  function frame(now) {
    raf = 0;
    if (!P.visible || document.hidden) return;
    if (!busy() && !dirty && now - lastRender < 32) { raf = requestAnimationFrame(frame); return; }
    const dt = Math.min(50, now - lastT);
    lastT = now;
    if (explode) {
      const p = explode.dur ? clamp((now - explode.t0) / explode.dur, 0, 1) : 1;
      explodeK = lerp(explode.from, explode.to, p);
      if (p >= 1) explode = null;
    }
    P.tick(dt);
    layout(now);
    camDamping = controls.update();
    updateCamera(now, dt);
    updateUnits(dt);
    updateLoop(now);
    updateTraffic(now);
    renderer.render(scene, camera);
    placeLabels();
    P.placeOverlays();
    dirty = false;
    lastRender = now;
    if (busy() || ambientOn() || P.playing) raf = requestAnimationFrame(frame);   // a playing clock must keep ticking
  }
  function wake() {
    dirty = true;
    if (!raf && P && P.visible && !document.hidden) { lastT = performance.now(); raf = requestAnimationFrame(frame); }
  }

  /* size: the camera distance frames the whole scene for the box's aspect */
  function fitDistance() {
    const vf = camera.fov * DEG / 2, hf = Math.atan(Math.tan(vf) * camera.aspect);
    const halfH = MOB.matches ? 5.0 : 4.65, halfW = MOB.matches ? 6.25 : 6.7;   // what the stack, ring and actors need
    return Math.max(halfH / Math.tan(vf), halfW / Math.tan(hf)) + 6;
  }
  function resize() {
    W = Math.max(1, Math.round(sceneEl.clientWidth));
    H = Math.max(1, Math.round(sceneEl.clientHeight));
    renderer.setSize(W, H, false);
    camera.aspect = W / H;
    camera.updateProjectionMatrix();
    cam.fit = fitDistance();
    measureLabels();
    P.measure();
    wake();
  }

  /* view buttons */
  function setExplode(to, animate) {
    explode = { from: explodeK, to, t0: performance.now(), dur: animate && !RM.matches ? 1100 * Math.abs(to - explodeK) : 0 };
    exBtn.setAttribute('aria-pressed', String(to === 1));
    wake();
  }
  listen(exBtn, 'click', () => setExplode(exBtn.getAttribute('aria-pressed') === 'true' ? 0 : 1, true));
  listen($('[data-act="reset"]'), 'click', () => { P.closeFocus(); startReset(); wake(); });

  /* theme: re-colour the scene from the tokens; follows the page toggle and the system */
  function applyPalette() {
    readPalette();
    hemi.intensity = pal.hemi;
    key.intensity = pal.key;
    M.slab.color.copy(pal.slab);
    M.slabEdge.forEach(m => { m.color.copy(pal.edge); m.opacity = pal.slabA; });
    M.grid.color.copy(pal.edge); M.grid.opacity = pal.slabA * 0.16;
    M.glass.color.copy(pal.glass); M.glass.opacity = pal.glassA;
    M.glassEdge.color.copy(pal.edge); M.glassEdge.opacity = pal.edgeA * 0.7;
    M.vaultEdge.color.copy(pal.edge); M.vaultEdge.opacity = pal.edgeA;
    M.shadow.color.copy(pal.shadow); M.shadow.opacity = pal.shadowA;
    [M.ring, M.chev, M.ghost, M.link, M.bound, M.dot].forEach(m => m.color.copy(pal.line));
    M.tether.color.copy(pal.signal);
    M.tetherF.color.copy(pal.fault);
    M.beam.color.copy(pal.fault);
    colMat.color.copy(pal.glass); colMat.opacity = pal.glassA * 1.3;
    lock.mat.color.copy(pal.on); lock.edge.color.copy(pal.signal); lock.shackle.material.color.copy(pal.signal);
    drawHatch();
    updateUnits(1e6);                       // snap every unit to its new colours: no fade on a theme switch
    wake();
  }
  const themeObs = new MutationObserver(applyPalette);
  themeObs.observe(root, { attributes: true, attributeFilter: ['data-theme'] });
  cleanups.push(() => themeObs.disconnect());
  listen(SYS_DARK, 'change', applyPalette);

  /* reduced motion can change while the page is open (the player re-enters the step itself) */
  listen(RM, 'change', () => {
    controls.enableDamping = !RM.matches;
    if (RM.matches && explode) { explodeK = explode.to; explode = null; }
    wake();
  });

  const sizeObs = new ResizeObserver(resize);
  cleanups.push(() => sizeObs.disconnect());
  listen(MOB, 'change', resize);

  /* lifecycle: free the GPU when the page goes away; if the context is lost, hand over to the drawing */
  function dispose() {
    cancelAnimationFrame(raf); raf = 0;
    controls.dispose();
    scene.traverse(o => {
      if (o.geometry) o.geometry.dispose();
      const ms = Array.isArray(o.material) ? o.material : o.material ? [o.material] : [];
      ms.forEach(m => { if (m.map) m.map.dispose(); m.dispose(); });
    });
  }
  cleanups.push(dispose);
  listen(window, 'pagehide', e => { if (!e.persisted) teardown(); });
  listen(canvas, 'webglcontextlost', e => {
    e.preventDefault();
    teardown();
    fallback('webgl context lost');
  });

  /* the flow player: it owns the words, the timing and the controls; this view draws */
  P = createPlayer(host, {
    wake,
    size: () => ({ W, H }),
    anchorPx,
    labelRects: () => [...actorLbls, ...zoneLbls.filter(L => !L.away)].map(L => ({ x: L.x, y: L.y, w: L.w, h: L.h })),
    focus: id => startFocus(id ? 1 : 0),
    focused: () => cam.focus > 0.05,
    home: () => { if (!dragging) startReset(); },
    onStep: setNudge,
    progress: setProgressStart,
    firstView: () => setExplode(1, true),     // the building lifts apart into its three zones, once
    autoplayDelay: 1500,
  });

  /* start: assembled and still; it lifts apart and plays when it scrolls into view */
  frameEl.insertBefore(canvas, labelsEl);
  explodeK = RM.matches ? 1 : 0;
  exBtn.setAttribute('aria-pressed', String(explodeK === 1));
  resize();
  sizeObs.observe(sceneEl);
  layout(performance.now());
  _sph.set(cam.fit, POLAR0, AZ0);
  controls.target.set(0, ring.yc, 0);
  camera.position.setFromSpherical(_sph).add(controls.target);
  camera.lookAt(controls.target);
  controls.update();
  P.start();
  updateUnits(1e6);
  updateLoop(performance.now());
  renderer.render(scene, camera);
  root.setAttribute('data-how', '3d');
  measureLabels();
  placeLabels();
  P.measure();
  P.placeOverlays();
  wake();
  if (document.fonts && document.fonts.ready) document.fonts.ready.then(() => { measureLabels(); P.measure(); wake(); });
  }
}
