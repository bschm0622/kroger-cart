'use strict';

// Local UI for assembling a weekly Kroger order. No framework and no build
// step on purpose: this page is served by the same binary that talks to Kroger,
// and a toolchain would be more moving parts than the page is worth.

const $ = (sel) => document.querySelector(sel);
const el = (tag, props = {}, kids = []) => {
  const n = Object.assign(document.createElement(tag), props);
  for (const k of [].concat(kids)) if (k !== null && k !== undefined && k !== '') n.append(k);
  return n;
};
const plural = (n, word) => `${n} ${word}${n === 1 ? '' : 's'}`;

// Lucide's paths, inlined: five icons aren't worth a dependency.
const ICONS = {
  plus: '<path d="M5 12h14"/><path d="M12 5v14"/>',
  minus: '<path d="M5 12h14"/>',
  trash: '<path d="M3 6h18"/><path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/><path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/>',
  chevron: '<path d="m9 18 6-6-6-6"/>',
  external: '<path d="M15 3h6v6"/><path d="M10 14 21 3"/><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/>',
};
function icon(name, cls = '') {
  const t = document.createElement('template');
  t.innerHTML = `<svg class="icon ${cls}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICONS[name]}</svg>`;
  return t.content.firstChild;
}

const state = {
  staples: [],
  draft: new Map(),
  cartLoaded: null, // null while loading, then true or false
  recipes: [],
  trips: [],
  tripNames: {},
  openTrips: new Set(),
  historyFilter: '',
  filter: '',
  found: [],
  findTerm: '',
  cart: { cartId: '', lines: [] },
  editing: null,
};

// --- transport -------------------------------------------------------------

let busyDepth = 0;

// banner is a floating toast rather than a bar, so "Working…" doesn't shove the
// page down and back up on every request. Confirmations fade on their own;
// errors stay until the next message, because you may need to read them twice.
let bannerTimer = 0;
function banner(msg, kind, action) {
  const b = $('#banner');
  clearTimeout(bannerTimer);
  if (!msg) { b.hidden = true; return; }
  b.hidden = false;
  b.className = kind || '';
  b.textContent = msg;
  if (action) {
    const btn = el('button', { type: 'button', className: 'btn outline sm', textContent: action.label });
    btn.onclick = () => { b.hidden = true; action.run(); };
    b.append(btn);
  }
  if (kind === 'ok' || !kind) bannerTimer = setTimeout(() => { b.hidden = true; }, action ? 8000 : 4000);
}

// api centralizes the one failure that actually matters here — an expired
// session — so every caller doesn't have to handle it. The server flags it with
// needsCookie, and the session box opens itself.
async function api(path, opts = {}) {
  busyDepth++;
  if (busyDepth === 1) banner('Working…', 'busy');
  try {
    const res = await fetch(path, {
      ...opts,
      headers: opts.body ? { 'Content-Type': 'application/json' } : undefined,
    });
    const text = await res.text();
    let data = {};
    try { data = text ? JSON.parse(text) : {}; } catch { data = { error: text }; }
    if (!res.ok) {
      if (data.needsCookie) showCookieBox();
      throw new Error(friendlyError(data.error || `${res.status} ${res.statusText}`));
    }
    return data;
  } finally {
    // Only clear our own "Working…"; a message set meanwhile should stay.
    if (--busyDepth === 0 && $('#banner').className === 'busy') banner('');
  }
}

// Kroger's bot protection answers bursts with a 429 and a JSON challenge, which
// as a raw error reads like the app broke.
function friendlyError(msg) {
  if (/HTTP 429/.test(msg)) return 'Kroger is limiting requests right now. Wait a minute and try again.';
  return msg;
}

async function run(fn, okMsg) {
  try {
    const out = await fn();
    if (okMsg) banner(okMsg, 'ok');
    return out;
  } catch (err) {
    banner(err.message, 'error');
    return null;
  }
}

const post = (path, body) => api(path, { method: 'POST', body: JSON.stringify(body ?? {}) });
const put = (path, body) => api(path, { method: 'PUT', body: JSON.stringify(body) });

// --- session ---------------------------------------------------------------

function showCookieBox() {
  $('#cookie-box').hidden = false;
}

$('#cookie-cancel').onclick = () => { $('#cookie-box').hidden = true; };

// The server reads Chrome's own cookie database, so the jar never travels
// through the page — nothing to paste and nothing to leave in the DOM.
$('#cookie-chrome').onclick = () => run(async () => {
  const out = await post('/api/cookie/chrome', {});
  $('#cookie-box').hidden = true;
  banner(`Signed in again from Chrome (${out.profile}).`, 'ok');
  await refreshCart();
  await loadTab(currentTab, true);
});

$('#cookie-save').onclick = () => run(async () => {
  const cookie = $('#cookie-input').value;
  if (!cookie.trim()) throw new Error('Paste the Cookie header first.');
  const out = await post('/api/cookie', { cookie });
  // Clear the box immediately: the value is bearer-equivalent and there's no
  // reason for it to sit in the DOM after it's been saved.
  $('#cookie-input').value = '';
  $('#cookie-box').hidden = true;
  banner(`Saved ${out.cookieCount} cookies. Session refreshed.`, 'ok');
  await refreshCart();
  await loadTab(currentTab, true);
});

async function checkStatus() {
  const st = await run(() => api('/api/status'));
  if (!st) return;
  if (!st.ok) {
    banner(st.detail || 'Session needs refreshing.', 'error');
    showCookieBox();
  }
}

// --- the kroger cart ---------------------------------------------------------

async function refreshCart() {
  const cart = await run(() => api('/api/cart'));
  state.cartLoaded = !!cart;
  if (cart) {
    state.cart = cart;
    adoptOldList();
    // An edit that now matches the cart (changed on kroger.com, or just sent)
    // is no longer a change.
    for (const d of [...state.draft.values()]) if (d.qty === inCart(d.upc)) state.draft.delete(d.upc);
    saveDraft();
  }
  // Every stepper on the page shows the cart, so all of them redraw.
  renderStaples();
  if (loaded.has('search')) renderFind();
  if (loaded.has('history')) renderHistory();
  renderBasket();
}

// The cart can change on kroger.com in another tab, so look again whenever
// you come back to this one.
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible' && state.cartLoaded !== null) refreshCart();
});

// --- tabs ------------------------------------------------------------------

let currentTab = 'staples';
const loaded = new Set();

for (const btn of document.querySelectorAll('#tabs button')) {
  btn.onclick = () => showTab(btn.dataset.tab);
}

// The tab lives in the hash so a reload comes back where you were, which
// matters most after refreshing the session from the History tab.
function showTab(tab) {
  currentTab = tab;
  for (const b of document.querySelectorAll('#tabs button')) {
    b.classList.toggle('active', b.dataset.tab === tab);
  }
  for (const s of document.querySelectorAll('.tab')) s.hidden = s.id !== `tab-${tab}`;
  if (location.hash.slice(1) !== tab) location.hash = tab;
  // Focus the box on every visit to Search, not just the first: you came
  // here to type.
  if (tab === 'search') $('#find-q').focus();
  loadTab(tab);
}

async function loadTab(tab, force) {
  if (loaded.has(tab) && !force) return;
  loaded.add(tab);
  if (tab === 'staples') await loadStaples();
  if (tab === 'recipes') await loadRecipes();
  if (tab === 'history') await loadHistory();
  if (tab === 'search') renderFind();
}

// --- the cart panel --------------------------------------------------------
//
// The panel is your Kroger cart, and every stepper on the page shows what that
// item will be in it. A change is held as a pending edit — state.draft maps a
// UPC to the quantity it should end up at, 0 meaning remove — until "Update
// Kroger cart" sends them all at once. Edits are kept in the browser, so a
// reload doesn't lose them.
//
// "Your usual" is the staples marked in staples.tsv (the same marks the
// weekly-cart script reads). "Add your usual" raises the cart to it; "Save as
// usual" replaces it with what the cart will hold.

const DRAFT_KEY = 'kroger-cart:draft';
state.draft = new Map(JSON.parse(localStorage.getItem(DRAFT_KEY) || '[]').map((x) => [x.upc, x]));
const saveDraft = () => localStorage.setItem(DRAFT_KEY, JSON.stringify([...state.draft.values()]));

const stapleFor = (upc) => state.staples.find((s) => s.upc === upc);
const usual = () => state.staples.filter((s) => s.selected);
const inCart = (upc) => state.cart.lines.reduce((n, l) => n + (l.upc === upc ? l.quantity : 0), 0);
const nameFor = (upc) => state.draft.get(upc)?.name
  || state.cart.lines.find((l) => l.upc === upc && l.name)?.name
  || stapleFor(upc)?.name || state.tripNames[upc] || '';

// qtyOnList is what the cart will hold once pending edits are sent.
const qtyOnList = (upc) => (state.draft.has(upc) ? state.draft.get(upc).qty : inCart(upc));

// The page before this one kept a separate list of things to add. Whatever was
// on it becomes pending additions, once, so nothing on it is lost.
function adoptOldList() {
  const raw = localStorage.getItem('kroger-cart:list') || localStorage.getItem('kroger-cart:extras');
  if (!raw) return;
  for (const x of JSON.parse(raw)) {
    if (x.qty > inCart(x.upc)) state.draft.set(x.upc, { upc: x.upc, name: x.name || '', qty: x.qty });
  }
  localStorage.removeItem('kroger-cart:list');
  localStorage.removeItem('kroger-cart:extras');
  saveDraft();
}

function stage(upc, qty, name) {
  if (qty === inCart(upc)) state.draft.delete(upc); // back to what the cart has
  else state.draft.set(upc, { upc, name: name || nameFor(upc), qty });
}

// setQty is the single way one item's quantity changes.
function setQty(item, qty) {
  stage(item.upc, qty, item.name);
  saveDraft();
  refreshControls(item.upc);
  renderBasket();
}

// addMany raises a batch — a recipe, a past trip, your usual — to at least the
// quantities given, with one save. It returns how many lines it raised.
function addMany(items) {
  let n = 0;
  for (const it of items) {
    const want = it.qty || 1;
    if (qtyOnList(it.upc) >= want) continue;
    stage(it.upc, want, it.name);
    n++;
  }
  saveDraft();
  for (const it of items) refreshControls(it.upc);
  renderBasket();
  return n;
}

// replaceDraft swaps all pending edits (discard, undo, empty) and redraws every
// control either set touched.
function replaceDraft(next) {
  const touched = new Set([...state.draft.keys(), ...next.keys()]);
  state.draft = next;
  saveDraft();
  for (const upc of touched) refreshControls(upc);
  renderBasket();
}

const changes = () => [...state.draft.values()].filter((d) => d.qty !== inCart(d.upc));

// What the cart will hold: its current lines with edits applied, then anything new.
function cartToBe() {
  const out = new Map();
  for (const l of state.cart.lines) {
    if (!out.has(l.upc)) out.set(l.upc, { upc: l.upc, name: nameFor(l.upc), qty: qtyOnList(l.upc) });
  }
  for (const d of state.draft.values()) if (!out.has(d.upc)) out.set(d.upc, { ...d });
  return [...out.values()].filter((l) => l.qty > 0);
}

// Every rendered Add/stepper registers here, so a change made in one place (the
// panel, a tab) updates every copy without rebuilding the tab.
const controls = new Map();

function listControl(item, addLabel) {
  const slot = el('span', { className: 'qty-slot' });
  slot.item = item;
  slot.addLabel = addLabel;
  if (!controls.has(item.upc)) controls.set(item.upc, new Set());
  controls.get(item.upc).add(slot);
  fillControl(slot);
  return slot;
}

function fillControl(slot, keepFocus) {
  const { item } = slot;
  const qty = qtyOnList(item.upc);
  const label = item.name || nameFor(item.upc) || item.upc;
  slot.textContent = '';
  slot.closest('.item')?.classList.toggle('on', qty > 0);
  if (!qty) {
    const word = slot.addLabel || 'Add';
    const add = el('button', { type: 'button', className: 'btn outline sm', ariaLabel: `${word} ${label}` },
      [icon('plus'), word]);
    // Adding back something you'd marked for removal restores what the cart has.
    add.onclick = () => setQty(item, inCart(item.upc) || item.qty || 1);
    slot.append(add);
    if (keepFocus) add.focus();
    return;
  }
  const minus = el('button', { type: 'button', ariaLabel: qty === 1 ? `Remove ${label}` : `One fewer ${label}` },
    [icon('minus')]);
  minus.onclick = () => setQty(item, qty - 1);
  const plus = el('button', { type: 'button', ariaLabel: `One more ${label}` }, [icon('plus')]);
  plus.onclick = () => setQty(item, qty + 1);
  slot.append(el('span', { className: 'stepper' }, [
    minus, el('output', { textContent: String(qty), ariaLabel: 'Quantity' }), plus,
  ]));
  if (keepFocus) plus.focus();
}

function refreshControls(upc) {
  const slots = controls.get(upc);
  if (!slots) return;
  const active = document.activeElement;
  for (const slot of [...slots]) {
    if (!slot.isConnected) { slots.delete(slot); continue; }
    // Keep focus inside the control you just used, so + + + works from the
    // keyboard and a click doesn't drop you back to the top of the page.
    fillControl(slot, slot.contains(active));
  }
}

// itemRow is the one row shape used by every list on the page.
function itemRow(item, { title, meta, thumb, after } = {}) {
  const label = title || item.name || item.upc;
  const row = el('div', { className: `item${qtyOnList(item.upc) ? ' on' : ''}` }, [
    thumb ? el('img', { className: 'thumb', src: thumb, alt: '', loading: 'lazy' }) : null,
    el('span', { className: 'name' }, [
      el('b', { textContent: label, title: `${label}\nUPC ${item.upc}` }),
      meta ? el('span', { className: 'meta' }, meta) : null,
    ]),
    listControl(item),
    after || null,
  ]);
  return row;
}

const emptyRow = (text) => el('div', { className: 'item empty', textContent: text });

function panelLine(l, note) {
  const kids = [el('span', { textContent: l.name || nameFor(l.upc) || l.upc, title: l.name || l.upc })];
  if (note) kids.push(el('div', { className: `line-note ${note.kind}`, textContent: note.text }));
  if (!stapleFor(l.upc) && note && note.kind === 'new') {
    const promote = el('button', { type: 'button', className: 'make-staple', textContent: 'Make it a staple' });
    promote.onclick = () => makeStaple({ ...l, qty: qtyOnList(l.upc) || l.qty });
    kids.push(el('div', {}, [promote]));
  }
  const item = { upc: l.upc, name: l.name, qty: inCart(l.upc) || l.qty };
  return el('div', { className: 'item' }, [
    el('span', { className: 'name' }, kids),
    listControl(item, note?.kind === 'gone' ? 'Keep' : 'Add'),
  ]);
}

function renderBasket() {
  const pending = changes();
  const pendingSet = new Set(pending.map((d) => d.upc));
  const toBe = cartToBe();
  const units = toBe.reduce((n, l) => n + l.qty, 0);
  const body = $('#basket-body');
  body.textContent = '';

  if (state.cartLoaded === null) {
    body.append(el('p', { className: 'muted empty', textContent: 'Loading your Kroger cart…' }));
  } else if (!state.cartLoaded) {
    const retry = el('button', { type: 'button', className: 'btn outline sm', textContent: 'Try again' });
    retry.onclick = refreshCart;
    body.append(el('p', { className: 'muted empty', textContent: 'Couldn’t read your Kroger cart. Changes you make here are kept until it’s back.' }), retry);
  }

  if (pending.length) {
    body.append(el('h3', { textContent: 'Changes to send' }));
    const box = el('div', { className: 'basket-lines' });
    for (const d of pending) {
      const had = inCart(d.upc);
      const note = !had ? { kind: 'new', text: 'New' }
        : !d.qty ? { kind: 'gone', text: `Removing · was ${had}` }
        : { kind: 'changed', text: `Was ${had}` };
      box.append(panelLine(d, note));
    }
    body.append(box);
  }

  const settled = toBe.filter((l) => !pendingSet.has(l.upc));
  if (settled.length) {
    body.append(el('h3', { textContent: pending.length ? 'Already in your cart' : 'In your cart' }));
    const box = el('div', { className: 'basket-lines' });
    for (const l of settled) box.append(panelLine(l));
    body.append(box);
  }

  if (state.cartLoaded && !pending.length && !settled.length) {
    body.append(el('p', { className: 'muted empty', textContent: 'Your cart is empty. Press Add next to anything you want.' }));
    if (usual().length) {
      const b = el('button', { type: 'button', className: 'btn outline sm' }, [icon('plus'), `Add your usual · ${plural(usual().length, 'item')}`]);
      b.onclick = addUsual;
      body.append(b);
    }
  }

  const send = $('#basket-send');
  // With nothing to send, the next step is checking out, so that takes the spot.
  send.hidden = !pending.length;
  send.disabled = state.cartLoaded !== true;
  send.textContent = `Update Kroger cart · ${plural(pending.length, 'change')}`;
  $('#basket-checkout').hidden = !!pending.length || !toBe.length;
  $('#basket-discard').hidden = !pending.length;
  $('#basket-count').textContent = units ? String(units) : '';
  $('#basket-empty').hidden = !toBe.length;
  $('#basket-usual').hidden = !usual().length || (!toBe.length && !pending.length);
  $('#basket-to-usual').hidden = !toBe.length;
  $('#basket-to-recipe').hidden = !toBe.length;
  $('#basket-bar').textContent = `Kroger cart · ${units ? plural(units, 'item') : 'empty'}${pending.length ? ` · ${plural(pending.length, 'change')}` : ''}`;
}

function addUsual() {
  const n = addMany(usual().map((s) => ({ upc: s.upc, name: s.name, qty: s.qty })));
  banner(n ? `Your usual adds ${plural(n, 'change')}. Update the cart to send them.` : 'Your cart already has your usual.', 'ok');
}
$('#basket-usual').onclick = addUsual;

// makeStaple adds a one-off to the staples list. times stays 0: it counts trips
// seen by the seeder, and a made-up number would distort the ordering.
function makeStaple(x) {
  state.staples.push({ upc: x.upc, qty: x.qty, times: 0, name: x.name, selected: false });
  saveStaples();
  renderStaples();
  renderBasket();
  if (state.found.length) renderFind();
  banner(`${x.name || 'That item'} is now a staple.`, 'ok');
}

$('#basket-bar').onclick = () => $('#basket').classList.add('open');
$('#basket-close').onclick = () => $('#basket').classList.remove('open');

// Neither Empty nor Discard asks first: both only change what's pending, and
// both offer Undo.
function withUndo(msg, next) {
  const before = new Map(state.draft);
  replaceDraft(next);
  banner(msg, 'ok', { label: 'Undo', run: () => replaceDraft(before) });
}
$('#basket-discard').onclick = () => withUndo('Changes discarded.', new Map());
$('#basket-empty').onclick = () => {
  const next = new Map();
  for (const l of state.cart.lines) next.set(l.upc, { upc: l.upc, name: nameFor(l.upc), qty: 0 });
  withUndo('Everything is marked for removal. Update the cart to send it.', next);
};

$('#basket-send').onclick = () => run(async () => {
  const items = changes().map((d) => ({ upc: d.upc, qty: d.qty }));
  if (!items.length) return;
  await post('/api/cart/update', { items });
  // Re-read the cart rather than assuming: it's the truth, and edits that now
  // match it drop out on their own.
  await refreshCart();
  $('#basket').classList.remove('open');
  banner(`Kroger cart updated: ${plural(items.length, 'change')}.`, 'ok');
});

$('#basket-to-recipe').onclick = () => {
  openRecipe({
    slug: '', name: '', notes: '',
    ingredients: cartToBe().map((l) => ({ upc: l.upc, qty: l.qty, name: l.name })),
  });
};

// Save as usual replaces the marks in staples.tsv with exactly what the cart
// will hold, adding anything that isn't a staple yet: your usual is staples.
$('#basket-to-usual').onclick = () => {
  const lines = cartToBe();
  if (!confirm(`Make these ${plural(lines.length, 'item')} your usual?\n\n“Add your usual” will bring your cart up to exactly these.`)) return;
  const by = new Map(lines.map((l) => [l.upc, l]));
  for (const s of state.staples) {
    const l = by.get(s.upc);
    s.selected = !!l;
    if (l) s.qty = l.qty;
  }
  for (const l of lines) {
    if (!stapleFor(l.upc)) state.staples.push({ upc: l.upc, qty: l.qty, times: 0, name: l.name, selected: true });
  }
  saveStaples();
  renderStaples();
  renderBasket();
  banner(`Saved ${plural(lines.length, 'item')} as your usual.`, 'ok');
};

// --- staples ---------------------------------------------------------------

async function loadStaples() {
  const out = await run(() => api('/api/staples'));
  if (!out) return;
  state.staples = out.staples || [];
  renderStaples();
  renderBasket();
}

// saveStaples persists the whole list on every change. The file is small and
// this keeps the UI and the TSV in step, so the CLI script and the page never
// disagree about what a staple is.
const saveStaples = () => run(() => put('/api/staples', { staples: state.staples }));

function renderStaples() {
  const list = $('#staples-list');
  list.textContent = '';
  const filter = state.filter.toLowerCase();
  const shown = state.staples.filter((s) =>
    !filter || (s.name || '').toLowerCase().includes(filter) || s.upc.includes(filter));

  if (!state.staples.length) {
    list.append(emptyRow('No staples yet. Rebuild the list from your purchase history below.'));
  } else if (!shown.length) {
    list.append(emptyRow(`None of your staples match “${state.filter}”. Try Search.`));
  }

  for (const s of shown) {
    const label = s.name || s.upc;
    const forget = el('button', { type: 'button', className: 'btn icon-only',
      title: 'Remove from staples', ariaLabel: `Remove ${label} from your staples` }, [icon('trash')]);
    forget.onclick = () => {
      if (!confirm(`Remove “${label}” from your staples?\n\nRebuilding from purchase history can bring it back.`)) return;
      state.staples = state.staples.filter((x) => x.upc !== s.upc);
      renderStaples();
      renderBasket();
      saveStaples();
    };
    list.append(itemRow(s, {
      meta: [
        s.times ? `Bought ${s.times} times` : 'Added by you',
        s.selected ? el('span', { className: 'badge', textContent: 'Usual', title: 'Part of “Add your usual”' }) : null,
      ].filter(Boolean),
      after: forget,
    }));
  }
}

$('#staples-filter').oninput = (e) => { state.filter = e.target.value; renderStaples(); };

$('#staples-seed').onclick = () => run(async () => {
  const out = await post('/api/staples/seed', { trips: 100, minTimes: 2 });
  state.staples = out.staples || [];
  renderStaples();
  renderBasket();
  banner(`Rebuilt from ${out.trips} trips: ${state.staples.length} items. Your list and quantities were kept.`, 'ok');
});

// --- search ----------------------------------------------------------------
//
// Buying something new is a first-class thing to want, not an edit to the
// staples list. Results go on the list as "just this time"; the list offers to
// make them staples if the answer turns out to be "every week".

function renderFind() {
  const list = $('#find-list');
  list.textContent = '';
  if (!state.found.length) {
    if (state.findTerm) list.append(emptyRow(`Nothing on kroger.com matched “${state.findTerm}”.`));
    return;
  }
  for (const item of state.found) {
    const meta = [item.size, item.price].filter(Boolean).join(' · ');
    const kids = [meta || null];
    // Say when a result is already a staple: silently offering something that's
    // on your list is the most confusing thing search can do.
    if (stapleFor(item.upc)) kids.push(el('span', { className: 'badge', textContent: 'Staple' }));
    list.append(itemRow(item, { meta: kids.filter(Boolean), thumb: item.image }));
  }
}

$('#find-form').onsubmit = (e) => {
  e.preventDefault();
  const term = $('#find-q').value.trim();
  if (!term) return;
  run(async () => {
    const out = await api(`/api/search?q=${encodeURIComponent(term)}&size=24`);
    state.found = out.items || [];
    state.findTerm = term;
    renderFind();
  });
};

// --- recipes ---------------------------------------------------------------

async function loadRecipes() {
  const out = await run(() => api('/api/recipes'));
  if (!out) return;
  state.recipes = out.recipes || [];
  renderRecipes();
}

function renderRecipes() {
  const list = $('#recipes-list');
  list.textContent = '';
  if (!state.recipes.length) {
    list.append(emptyRow('No recipes yet. Start one with New recipe, or build a list and save it as a recipe.'));
  }
  for (const r of state.recipes) {
    const ing = r.ingredients || [];
    const edit = el('button', { type: 'button', className: 'btn ghost sm', textContent: 'Edit' });
    edit.onclick = () => openRecipe(r);
    const add = el('button', { type: 'button', className: 'btn outline sm' }, [icon('plus'), 'Add to list']);
    add.disabled = !ing.length;
    add.onclick = () => {
      const n = addMany(ing.map((i) => ({ upc: i.upc, name: i.name, qty: i.qty || 1 })));
      banner(n ? `Added ${plural(n, 'ingredient')} from ${r.name} to your list.` : `Everything in ${r.name} is already on your list.`, 'ok');
    };
    list.append(el('div', { className: 'item' }, [
      el('span', { className: 'name' }, [
        el('b', { textContent: r.name }),
        el('span', { className: 'meta', textContent: [plural(ing.length, 'ingredient'), r.notes].filter(Boolean).join(' · ') }),
      ]),
      edit,
      add,
    ]));
  }
}

$('#recipe-new').onclick = () => openRecipe({ slug: '', name: '', notes: '', ingredients: [] });

function openRecipe(recipe) {
  // Deep-copy so Cancel really cancels; editing the live object would leave
  // half-made changes in the list.
  state.editing = JSON.parse(JSON.stringify(recipe));
  $('#recipe-title').textContent = recipe.slug ? 'Edit recipe' : 'New recipe';
  $('#recipe-name').value = recipe.name || '';
  $('#recipe-notes').value = recipe.notes || '';
  $('#recipe-results').textContent = '';
  $('#recipe-search').value = '';
  $('#recipe-delete').hidden = !recipe.slug;
  renderIngredients();
  $('#recipe-dialog').showModal();
  $('#recipe-name').focus();
}

function renderIngredients() {
  const list = $('#recipe-ingredients');
  list.textContent = '';
  const ing = state.editing.ingredients || [];
  if (!ing.length) list.append(emptyRow('No ingredients yet. Search below to add some.'));
  ing.forEach((i, idx) => {
    const minus = el('button', { type: 'button', ariaLabel: 'One fewer' }, [icon('minus')]);
    minus.onclick = () => {
      if ((i.qty || 1) <= 1) state.editing.ingredients.splice(idx, 1); else i.qty = (i.qty || 1) - 1;
      renderIngredients();
    };
    const plus = el('button', { type: 'button', ariaLabel: 'One more' }, [icon('plus')]);
    plus.onclick = () => { i.qty = (i.qty || 1) + 1; renderIngredients(); };
    list.append(el('div', { className: 'item' }, [
      el('span', { className: 'name' }, [el('b', { textContent: i.name || i.upc })]),
      el('span', { className: 'qty-slot' }, [el('span', { className: 'stepper' }, [
        minus, el('output', { textContent: String(i.qty || 1) }), plus,
      ])]),
    ]));
  });
}

const runRecipeSearch = () => run(async () => {
  const term = $('#recipe-search').value.trim();
  if (!term) return;
  const out = await api(`/api/search?q=${encodeURIComponent(term)}&size=24`);
  const box = $('#recipe-results');
  box.textContent = '';
  if (!out.items.length) { box.append(emptyRow('No matches.')); return; }
  for (const item of out.items) {
    const already = (state.editing.ingredients || []).some((i) => i.upc === item.upc);
    const add = el('button', { type: 'button', className: 'btn outline sm' }, already ? ['Added'] : [icon('plus'), 'Add']);
    add.disabled = already;
    add.onclick = () => {
      state.editing.ingredients = state.editing.ingredients || [];
      state.editing.ingredients.push({ upc: item.upc, qty: 1, name: item.name });
      add.textContent = 'Added';
      add.disabled = true;
      renderIngredients();
    };
    box.append(el('div', { className: 'item' }, [
      item.image ? el('img', { className: 'thumb', src: item.image, alt: '', loading: 'lazy' }) : null,
      el('span', { className: 'name' }, [
        el('b', { textContent: item.name || item.upc }),
        el('span', { className: 'meta', textContent: [item.size, item.price].filter(Boolean).join(' · ') }),
      ]),
      add,
    ]));
  }
});
$('#recipe-search-go').onclick = runRecipeSearch;
$('#recipe-search').onkeydown = (e) => { if (e.key === 'Enter') { e.preventDefault(); runRecipeSearch(); } };

$('#recipe-cancel').onclick = () => { $('#recipe-dialog').close(); state.editing = null; };

$('#recipe-save').onclick = () => run(async () => {
  const name = $('#recipe-name').value.trim();
  if (!name) throw new Error('A recipe needs a name.');
  const payload = {
    slug: state.editing.slug || '',
    name,
    notes: $('#recipe-notes').value.trim(),
    ingredients: state.editing.ingredients || [],
  };
  await put('/api/recipes', payload);
  $('#recipe-dialog').close();
  state.editing = null;
  await loadRecipes();
  banner(`Saved ${name}.`, 'ok');
});

$('#recipe-delete').onclick = () => run(async () => {
  if (!confirm(`Delete ${state.editing.name}? The recipe file is removed.`)) return;
  await post('/api/recipes/delete', { slug: state.editing.slug });
  $('#recipe-dialog').close();
  state.editing = null;
  await loadRecipes();
  banner('Recipe deleted.', 'ok');
});

// --- past trips --------------------------------------------------------------

async function loadHistory() {
  const out = await run(() => api('/api/history?size=25'));
  if (!out) return;
  state.trips = out.trips || [];
  state.tripNames = out.names || {};
  renderHistory();
}

$('#history-reload').onclick = () => loadHistory();
$('#history-filter').oninput = (e) => {
  state.historyFilter = e.target.value;
  // Clearing the filter goes back to the collapsed overview. Otherwise every
  // trip the filter had opened stays open, which is a wall of items.
  if (!state.historyFilter) state.openTrips.clear();
  renderHistory();
};

const tripKey = (t) => t.receiptKey || `${t.date}·${t.total}`;

// "2026-07-28" → "Tue, Jul 28" (with the year when it isn't this one).
function tripDate(s) {
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(s || '');
  if (!m) return s || 'Unknown date';
  const d = new Date(+m[1], +m[2] - 1, +m[3]);
  const opts = { weekday: 'short', month: 'short', day: 'numeric' };
  if (d.getFullYear() !== new Date().getFullYear()) opts.year = 'numeric';
  return d.toLocaleDateString(undefined, opts);
}

function renderHistory() {
  const list = $('#history-list');
  list.textContent = '';
  const filter = state.historyFilter.toLowerCase();
  const matches = (upc) =>
    !filter || upc.includes(filter) || (state.tripNames[upc] || '').toLowerCase().includes(filter);

  let any = false;
  for (const t of state.trips) {
    const shown = t.upcs.filter(matches);
    // A filter that no item in this trip matches hides the trip entirely,
    // rather than leaving a row that opens onto nothing.
    if (filter && !shown.length) continue;
    any = true;

    // Which trips are open is state, not just a DOM flag, so reloading or
    // changing the filter leaves them where you had them. A filter opens
    // everything it matched, because you filtered in order to see items.
    const key = tripKey(t);
    if (filter) state.openTrips.add(key);
    const items = el('div', { className: 'trip-items' });
    const toItem = (upc) => ({ upc, name: state.tripNames[upc] || stapleFor(upc)?.name || '', qty: 1 });

    // Items render on first open: 25 trips of 30-odd items is a lot of rows
    // nobody looks at.
    const fill = () => {
      if (items.childElementCount) return;
      for (const upc of shown) items.append(itemRow(toItem(upc)));
    };

    const where = t.type === 'IN_STORE' ? 'In store' : (t.type || '').toLowerCase().replace(/^\w/, (c) => c.toUpperCase());
    const head = el('button', { type: 'button', className: 'trip-head' }, [
      icon('chevron', 'chev'),
      el('span', { className: 'name' }, [
        el('b', { textContent: tripDate(t.date) }),
        el('span', { className: 'meta', textContent: [where, plural(shown.length, 'item')].filter(Boolean).join(' · ') }),
      ]),
      t.total ? el('span', { className: 'trip-total', textContent: `$${t.total}` }) : null,
    ]);
    const addAll = el('button', { type: 'button', className: 'btn outline sm' }, [icon('plus'), 'Add all']);
    addAll.onclick = () => {
      const n = addMany(shown.map(toItem));
      banner(n ? `Added ${plural(n, 'item')} from ${tripDate(t.date)} to your list.` : 'Everything from that trip is already on your list.', 'ok');
    };

    const row = el('div', { className: 'item trip' }, [head, addAll]);
    const setOpen = (open) => {
      row.classList.toggle('open', open);
      head.ariaExpanded = String(open);
      items.hidden = !open;
      if (open) { fill(); state.openTrips.add(key); } else state.openTrips.delete(key);
    };
    head.onclick = () => setOpen(items.hidden);
    setOpen(state.openTrips.has(key));

    list.append(row, items);
  }
  if (!state.trips.length) list.append(emptyRow('No trips found.'));
  else if (!any) list.append(emptyRow(`Nothing you've bought matches “${state.historyFilter}”.`));
}

// --- boot ------------------------------------------------------------------

(async function boot() {
  renderBasket();
  // Staples first, and before any other tab renders: the list needs them to
  // tell a staple from a one-off, and they come from a local file, so it's fast.
  loaded.add('staples');
  await loadStaples();
  const wanted = location.hash.slice(1);
  showTab(document.getElementById(`tab-${wanted}`) ? wanted : 'staples');
  await checkStatus();
  await refreshCart();
})();
