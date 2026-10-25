'use strict';

const $ = (sel) => document.querySelector(sel);
const el = (tag, props = {}, kids = []) => {
  const n = Object.assign(document.createElement(tag), props);
  for (const k of [].concat(kids)) if (k !== null && k !== undefined && k !== '') n.append(k);
  return n;
};
const plural = (n, word) => `${n} ${word}${n === 1 ? '' : 's'}`;
const money = (cents) => `$${(cents / 100).toFixed(2)}`;
const cents = (price) => {
  const m = /([\d.]+)/.exec(price || '');
  return m ? Math.round(parseFloat(m[1]) * 100) : null;
};

const ICONS = {
  plus: '<path d="M5 12h14"/><path d="M12 5v14"/>',
  minus: '<path d="M5 12h14"/>',
  trash: '<path d="M3 6h18"/><path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/><path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/>',
  chevron: '<path d="m9 18 6-6-6-6"/>',
  star: '<path d="M11.5 2.9a.6.6 0 0 1 1 0l2.6 5.3 5.8.8a.6.6 0 0 1 .3 1l-4.2 4.1 1 5.8a.6.6 0 0 1-.9.6L12 17.8l-5.2 2.7a.6.6 0 0 1-.9-.6l1-5.8-4.2-4.1a.6.6 0 0 1 .3-1l5.8-.8z"/>',
};
function icon(name, cls = '') {
  const t = document.createElement('template');
  t.innerHTML = `<svg class="icon ${cls}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICONS[name]}</svg>`;
  return t.content.firstChild;
}

function readJSON(key, fallback) {
  try {
    const v = JSON.parse(localStorage.getItem(key));
    return v ?? fallback;
  } catch {
    localStorage.removeItem(key);
    return fallback;
  }
}

const DRAFT_KEY = 'kroger-cart:draft';
const LOOKUP_KEY = 'kroger-cart:lookups';
const LOOKUP_TTL = 30 * 60 * 1000;
const NOT_FOUND = 'Not found at your store';

const state = {
  status: null,
  staples: [],
  staplesVersion: 0,
  staplesError: '',
  staplesLoaded: false,
  draft: new Map(),
  cart: { cartId: '', lines: [] },
  cartLoaded: null,
  cartError: null,
  stock: new Map(),
  details: new Map(),
  recipes: [],
  trips: [],
  tripItems: {},
  openTrips: new Set(),
  historyFilter: '',
  filter: '',
  found: [],
  findTerm: '',
  editing: null,
  sending: false,
};

function loadDraft() {
  const rows = readJSON(DRAFT_KEY, []);
  state.draft = new Map((Array.isArray(rows) ? rows : []).filter((x) => x?.upc).map((x) => [x.upc, x]));
}
const saveDraft = () => localStorage.setItem(DRAFT_KEY, JSON.stringify([...state.draft.values()]));
loadDraft();

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
  if (kind === 'ok') bannerTimer = setTimeout(() => { b.hidden = true; }, action ? 8000 : 4000);
}

class ApiError extends Error {
  constructor(msg, kind) { super(msg); this.kind = kind; }
}

let busyDepth = 0;
async function api(path, { quiet, busyText, ...opts } = {}) {
  if (!quiet && ++busyDepth === 1) banner(busyText || 'Working…', 'busy');
  try {
    let res;
    try {
      res = await fetch(path, { ...opts, headers: opts.body ? { 'Content-Type': 'application/json' } : undefined });
    } catch {
      throw new ApiError('Kroger Cart isn’t running. Double-click Kroger Cart (or run kroger-pp-cli serve), then reload this page.', 'offline');
    }
    const text = await res.text();
    let data = {};
    try { data = text ? JSON.parse(text) : {}; } catch { data = { error: text }; }
    if (!res.ok) {
      const err = new ApiError(data.error || `${res.status} ${res.statusText}`, data.kind || 'other');
      if (err.kind === 'auth') onAuthFailure();
      if (err.kind === 'no_store') showStoreBox();
      throw err;
    }
    return data;
  } finally {
    if (!quiet && --busyDepth === 0 && $('#banner').className === 'busy') banner('');
  }
}

const tone = (err) => (err.kind === 'rate_limited' || err.kind === 'staples_changed' ? 'warn' : 'error');

async function run(fn) {
  try {
    return await fn();
  } catch (err) {
    if (err.kind !== 'auth') banner(err.message, tone(err));
    return null;
  }
}

const post = (path, body, opts) => api(path, { method: 'POST', body: JSON.stringify(body ?? {}), ...opts });
const put = (path, body) => api(path, { method: 'PUT', body: JSON.stringify(body) });

function showCookieBox(message) {
  const box = $('#cookie-box');
  const first = !state.status?.hasCookie;
  $('#cookie-title').textContent = first ? 'Sign in to Kroger' : 'Your Kroger session has expired';
  $('#cookie-message').hidden = !message;
  $('#cookie-message').textContent = message || '';
  const wasHidden = box.hidden;
  box.hidden = false;
  if (wasHidden) {
    box.scrollIntoView({ block: 'start' });
    $('#cookie-title').focus();
  }
}

let triedAutoImport = false;
async function onAuthFailure() {
  if (triedAutoImport) { showCookieBox(); return; }
  triedAutoImport = true;
  if (await importFromChrome({ quiet: true })) return;
  showCookieBox();
}

async function importFromChrome({ quiet } = {}) {
  let out;
  try {
    out = await post('/api/cookie/chrome', {}, { quiet, busyText: 'Signing in from Chrome…' });
  } catch (err) {
    if (!quiet) showCookieBox(err.message);
    return false;
  }
  if (!out.working) {
    if (!quiet) {
      showCookieBox(out.kind === 'rate_limited' ? out.error
        : 'Chrome’s Kroger session isn’t signed in either. Open kroger.com in Chrome, sign in, wait about 30 seconds for Chrome to save it, then press Import again.');
    }
    return false;
  }
  if (state.status) state.status.hasCookie = true;
  $('#cookie-box').hidden = true;
  banner(`Signed in from Chrome (${out.profile}).`, 'ok');
  await afterSignIn();
  return true;
}

async function afterSignIn() {
  await refreshCart();
  for (const tab of loaded) if (tab !== 'staples' && tab !== 'search') loadTab(tab, true);
}

$('#cookie-cancel').onclick = () => { $('#cookie-box').hidden = true; };
$('#cookie-chrome').onclick = () => importFromChrome();

$('#cookie-save').onclick = () => run(async () => {
  const cookie = $('#cookie-input').value;
  if (!cookie.trim()) throw new ApiError('Paste the Cookie header first.', 'bad_request');
  const out = await post('/api/cookie', { cookie });
  $('#cookie-input').value = '';
  if (!out.working) { showCookieBox(out.error || 'That session didn’t work. Copy it again from a request whose Status reads 200.'); return; }
  if (state.status) state.status.hasCookie = true;
  $('#cookie-box').hidden = true;
  banner('Session saved.', 'ok');
  await afterSignIn();
});

function showStoreBox() { $('#store-box').hidden = false; }

function renderStore() {
  const st = state.status;
  const chip = $('#store-chip');
  if (!st?.hasStore) { chip.hidden = true; showStoreBox(); return; }
  $('#store-box').hidden = true;
  const how = (st.store?.modality || 'PICKUP').toLowerCase().replace(/^\w/, (c) => c.toUpperCase());
  chip.hidden = false;
  chip.textContent = [how, st.store?.name].filter(Boolean).join(' · ');
  chip.title = 'Your store. To change it, run kroger-pp-cli auth set-store again.';
}

const storeKey = () => state.status?.store?.id || 'default';

const stapleFor = (upc) => state.staples.find((s) => s.upc === upc);
const usual = () => state.staples.filter((s) => s.selected);
const inCart = (upc) => state.cart.lines.reduce((n, l) => n + (l.upc === upc ? l.quantity : 0), 0);
const targetQty = (upc) => (state.draft.has(upc) ? state.draft.get(upc).qty : inCart(upc));
const nameFor = (upc) => state.draft.get(upc)?.name
  || state.cart.lines.find((l) => l.upc === upc && l.name)?.name
  || state.details.get(upc)?.name || stapleFor(upc)?.name || '';

let lastCartRead = 0;
let cartReading = null;
function refreshCart(opts) {
  cartReading = readCart(opts).finally(() => { cartReading = null; });
  return cartReading;
}
async function readCart({ quiet } = {}) {
  lastCartRead = Date.now();
  try {
    const cart = await api('/api/cart', { quiet });
    state.cart = cart;
    state.cartLoaded = true;
    state.cartError = null;
    noteStock(cart.lines.filter((l) => l.name).map((l) => ({ upc: l.upc, name: l.name, unavailable: l.unavailable || '' })));
    rebaseDraft();
  } catch (err) {
    state.cartLoaded = false;
    state.cartError = err;
  }
  renderEverything();
}

// A pending change remembers the cart it was made against. If the cart changed
// elsewhere, an increase becomes "at least this many"; a decrease is dropped.
function rebaseDraft() {
  let adjusted = 0;
  for (const d of [...state.draft.values()]) {
    const now = inCart(d.upc);
    if (d.from === undefined) d.from = now;
    if (now !== d.from) {
      adjusted++;
      if (d.qty > d.from) { d.qty = Math.max(d.qty, now); d.from = now; } else state.draft.delete(d.upc);
    }
    if (state.draft.has(d.upc) && d.qty === now) state.draft.delete(d.upc);
  }
  saveDraft();
  if (adjusted) banner(`Your Kroger cart changed somewhere else, so ${plural(adjusted, 'pending change')} ${adjusted === 1 ? 'was' : 'were'} adjusted. Check the cart panel before you send.`, 'warn');
}

document.addEventListener('visibilitychange', () => {
  if (document.visibilityState !== 'visible' || state.cartLoaded === null) return;
  if (Date.now() - lastCartRead < 60 * 1000) return;
  refreshCart({ quiet: true });
});

window.addEventListener('storage', (e) => {
  if (e.key !== DRAFT_KEY) return;
  loadDraft();
  renderEverything();
});

function stage(upc, qty, name) {
  const now = inCart(upc);
  if (qty === now) { state.draft.delete(upc); return; }
  const prev = state.draft.get(upc);
  state.draft.set(upc, { upc, name: name || nameFor(upc), qty, from: prev?.from ?? now });
}

function setQty(item, qty) {
  stage(item.upc, qty, item.name);
  saveDraft();
  refreshControls(item.upc);
  renderBasket();
}

// 'atLeast' raises to a quantity (your usual, a past trip); 'add' adds on top (a recipe).
function addMany(items, mode = 'atLeast') {
  let n = 0;
  for (const it of items) {
    const want = it.qty || 1;
    const now = targetQty(it.upc);
    const next = mode === 'add' ? now + want : Math.max(now, want);
    if (next === now) continue;
    stage(it.upc, next, it.name);
    n++;
  }
  saveDraft();
  for (const it of items) refreshControls(it.upc);
  renderBasket();
  return n;
}

async function addChecked(items, what, mode) {
  if (cartReading) await cartReading;
  if (state.cartLoaded !== true) { banner('Your Kroger cart hasn’t loaded yet, so nothing was added.', 'warn'); return; }
  await lookUp(items.map((it) => it.upc));
  const out = items.filter((it) => unavailableFor(it.upc));
  const n = addMany(items.filter((it) => !out.includes(it)), mode);
  const left = out.length ? ` Left out: ${out.map((it) => `${it.name || nameFor(it.upc)} (${unavailableFor(it.upc).toLowerCase()})`).join(', ')}.` : '';
  banner(n ? `Added ${plural(n, 'change')} from ${what}. Press Update Kroger cart to send them.${left}`
    : `Nothing new to add from ${what}.${left}`, out.length ? 'warn' : 'ok');
}

function replaceDraft(next) {
  const touched = new Set([...state.draft.keys(), ...next.keys()]);
  state.draft = next;
  saveDraft();
  for (const upc of touched) refreshControls(upc);
  renderBasket();
}

const changes = () => [...state.draft.values()].filter((d) => d.qty !== inCart(d.upc));

function cartToBe() {
  const out = new Map();
  for (const l of state.cart.lines) {
    if (!out.has(l.upc)) out.set(l.upc, { upc: l.upc, name: nameFor(l.upc), qty: targetQty(l.upc) });
  }
  for (const d of state.draft.values()) if (!out.has(d.upc)) out.set(d.upc, { ...d });
  return [...out.values()].filter((l) => l.qty > 0);
}

function noteStock(items) {
  for (const it of items) {
    if (!it?.upc) continue;
    const s = { ...(state.stock.get(it.upc) || {}) };
    if ('unavailable' in it) s.unavailable = it.unavailable || '';
    if (it.stock) s.stock = it.stock;
    if (it.maxQty) s.maxQty = it.maxQty;
    state.stock.set(it.upc, s);
    if (it.image || it.price || it.name) {
      const d = { ...(state.details.get(it.upc) || {}) };
      for (const k of ['image', 'price', 'wasPrice', 'name', 'size']) if (it[k]) d[k] = it[k];
      state.details.set(it.upc, d);
    }
  }
}
const unavailableFor = (upc) => state.stock.get(upc)?.unavailable || '';

function stockBadge(upc) {
  const s = state.stock.get(upc);
  if (s?.unavailable) return el('span', { className: 'badge danger', textContent: s.unavailable });
  if (s?.stock === 'LOW') return el('span', { className: 'badge', textContent: 'Low stock' });
  return null;
}

function priceBits(upc) {
  const d = state.details.get(upc);
  if (!d?.price) return [];
  return d.wasPrice
    ? [el('span', { className: 'sale', textContent: d.price, title: `On sale, usually ${d.wasPrice}` }), el('span', { className: 'badge sale-badge', textContent: 'Sale' })]
    : [d.price];
}

// Lookups are cached for 30 minutes; the server re-checks stock before adding anything.
async function lookUp(upcs) {
  const todo = [...new Set(upcs)].filter(Boolean);
  if (!todo.length) return;
  const key = `${LOOKUP_KEY}:${storeKey()}`;
  const cache = readJSON(key, {});
  const now = Date.now();
  const fresh = todo.filter((upc) => cache[upc] && now - cache[upc].t < LOOKUP_TTL);
  noteStock(fresh.map((upc) => cache[upc].item || { upc, unavailable: NOT_FOUND }));

  const ask = todo.filter((upc) => !fresh.includes(upc));
  if (!ask.length) return;
  let out;
  try {
    out = await api('/api/products', { method: 'POST', body: JSON.stringify({ upcs: ask }), quiet: true });
  } catch {
    return;
  }
  const items = out.items || {};
  const unchecked = new Set(out.unchecked || []);
  const answered = ask.filter((upc) => !unchecked.has(upc));
  noteStock(answered.map((upc) => items[upc] || { upc, unavailable: NOT_FOUND }));
  for (const upc of answered) {
    const it = items[upc];
    cache[upc] = { t: now, item: it ? { upc, name: it.name, size: it.size, image: it.image, price: it.price, wasPrice: it.wasPrice, stock: it.stock, unavailable: it.unavailable || '', maxQty: it.maxQty } : null };
  }
  for (const [upc, v] of Object.entries(cache)) if (now - v.t >= LOOKUP_TTL) delete cache[upc];
  try { localStorage.setItem(key, JSON.stringify(cache)); } catch { /* storage full */ }
}

const controls = new Map();

function qtyControl(item, addLabel) {
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
  const qty = targetQty(item.upc);
  const label = item.name || nameFor(item.upc) || item.upc;
  const unavailable = unavailableFor(item.upc);
  slot.textContent = '';
  slot.closest('.item')?.classList.toggle('on', qty > 0);
  if (!qty) {
    if (state.cartLoaded !== true) {
      slot.append(el('button', { type: 'button', className: 'btn outline sm', disabled: true, textContent: 'Add',
        title: 'Waiting for your Kroger cart' }));
      return;
    }
    if (unavailable && !inCart(item.upc)) {
      slot.append(el('button', { type: 'button', className: 'btn outline sm', disabled: true, textContent: 'Unavailable', title: unavailable }));
      return;
    }
    const word = slot.addLabel || 'Add';
    const add = el('button', { type: 'button', className: 'btn outline sm', ariaLabel: `${word} ${label}` }, [icon('plus'), word]);
    add.onclick = () => setQty(item, inCart(item.upc) || item.qty || 1);
    slot.append(add);
    if (keepFocus) add.focus();
    return;
  }
  const minus = el('button', { type: 'button', ariaLabel: qty === 1 ? `Remove ${label}` : `One fewer ${label}` }, [icon('minus')]);
  minus.onclick = () => setQty(item, qty - 1);
  const plus = el('button', { type: 'button', ariaLabel: `One more ${label}` }, [icon('plus')]);
  plus.onclick = () => setQty(item, qty + 1);
  const max = state.stock.get(item.upc)?.maxQty;
  if (max && qty >= max) { plus.disabled = true; plus.title = `Kroger limits this to ${max} per order`; }
  if (unavailable && qty >= inCart(item.upc)) { plus.disabled = true; plus.title = unavailable; }
  slot.append(el('span', { className: 'stepper' }, [minus, el('output', { textContent: String(qty), ariaLabel: 'Quantity' }), plus]));
  if (keepFocus) (plus.disabled ? minus : plus).focus();
}

function refreshControls(upc) {
  const slots = controls.get(upc);
  if (!slots) return;
  const active = document.activeElement;
  for (const slot of [...slots]) {
    if (!slot.isConnected) { slots.delete(slot); continue; }
    fillControl(slot, slot.contains(active));
  }
}

function itemRow(item, { meta, after } = {}) {
  const label = item.name || nameFor(item.upc) || item.upc;
  const image = state.details.get(item.upc)?.image || item.image;
  return el('div', { className: `item${targetQty(item.upc) ? ' on' : ''}` }, [
    image ? el('img', { className: 'thumb', src: image, alt: '', loading: 'lazy' }) : el('span', { className: 'thumb' }),
    el('span', { className: 'name' }, [
      el('b', { textContent: label, title: label }),
      meta?.length ? el('span', { className: 'meta' }, meta) : null,
    ]),
    qtyControl(item),
    after || null,
  ]);
}

const emptyRow = (text) => el('div', { className: 'item empty', textContent: text });

function keepingFocus(container, render) {
  const active = document.activeElement;
  const label = container.contains(active) ? active.getAttribute('aria-label') : null;
  render();
  if (label) [...container.querySelectorAll('button')].find((b) => b.getAttribute('aria-label') === label)?.focus();
}

function renderEverything() {
  renderStaples();
  if (loaded.has('search')) renderFind();
  if (loaded.has('history')) renderHistory();
  renderBasket();
}

let currentTab = 'staples';
const loaded = new Set();

for (const btn of document.querySelectorAll('#tabs button')) btn.onclick = () => showTab(btn.dataset.tab);

function showTab(tab) {
  currentTab = tab;
  for (const b of document.querySelectorAll('#tabs button')) {
    const on = b.dataset.tab === tab;
    b.classList.toggle('active', on);
    b.setAttribute('aria-selected', String(on));
  }
  for (const s of document.querySelectorAll('.tab')) s.hidden = s.id !== `tab-${tab}`;
  if (location.hash.slice(1) !== tab) location.hash = tab;
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

function panelLine(l, note) {
  const qty = targetQty(l.upc);
  const each = cents(state.details.get(l.upc)?.price);
  const kids = [el('span', { textContent: l.name || nameFor(l.upc) || l.upc, title: l.name || l.upc })];
  const sub = [note ? el('span', { className: `line-note ${note.kind}`, textContent: note.text }) : null,
    each !== null && qty > 0 ? el('span', { className: 'line-note', textContent: money(each * qty) }) : null].filter(Boolean);
  if (sub.length) kids.push(el('div', { className: 'line-sub' }, sub));
  if (unavailableFor(l.upc) && qty > 0) {
    kids.push(el('div', { className: 'line-note gone', textContent: `${unavailableFor(l.upc)}. Kroger may not be able to fill this.` }));
  }
  if (!stapleFor(l.upc) && note?.kind === 'new' && state.staplesLoaded) {
    const promote = el('button', { type: 'button', className: 'make-staple', textContent: 'Make it a staple' });
    promote.onclick = () => makeStaple({ ...l, qty: qty || l.qty });
    kids.push(el('div', {}, [promote]));
  }
  const item = { upc: l.upc, name: l.name, qty: inCart(l.upc) || l.qty };
  return el('div', { className: 'item' }, [el('span', { className: 'name' }, kids), qtyControl(item, note?.kind === 'gone' ? 'Keep' : 'Add')]);
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
    const err = state.cartError;
    const msg = err?.kind === 'auth' ? 'Sign in to Kroger (above) to see your cart.'
      : err?.kind === 'no_store' ? 'Set your store (above) to see your cart.'
        : err?.message || 'Couldn’t read your Kroger cart.';
    body.append(el('p', { className: 'muted empty', textContent: msg }));
    if (err?.kind !== 'auth' && err?.kind !== 'offline') {
      const retry = el('button', { type: 'button', className: 'btn outline sm', textContent: 'Try again' });
      retry.onclick = () => refreshCart();
      body.append(retry);
    }
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

  let total = 0;
  let unpriced = 0;
  for (const l of toBe) {
    const c = cents(state.details.get(l.upc)?.price);
    if (c === null) unpriced++; else total += c * l.qty;
  }
  const totalEl = $('#basket-total');
  totalEl.hidden = !toBe.length || total === 0;
  totalEl.textContent = `Estimated ${money(total)}${unpriced ? ` · ${plural(unpriced, 'item')} without a price yet` : ''}`;

  const send = $('#basket-send');
  send.hidden = !pending.length;
  send.disabled = state.cartLoaded !== true || state.sending;
  send.textContent = state.sending ? 'Sending…' : `Update Kroger cart · ${plural(pending.length, 'change')}`;
  $('#basket-checkout').hidden = !!pending.length || !toBe.length;
  $('#basket-discard').hidden = !pending.length;
  $('#basket-count').textContent = units ? String(units) : '';
  $('#basket-empty').hidden = !toBe.length;
  $('#basket-usual').hidden = !usual().length || (!toBe.length && !pending.length);
  $('#basket-to-usual').hidden = !toBe.length || !state.staplesLoaded;
  $('#basket-to-recipe').hidden = !toBe.length;
  $('#basket-bar').textContent = `Kroger cart · ${units ? plural(units, 'item') : 'empty'}${pending.length ? ` · ${plural(pending.length, 'change')}` : ''}`;
}

function addUsual() {
  return addChecked(usual().map((s) => ({ upc: s.upc, name: s.name, qty: s.qty })), 'your usual', 'atLeast');
}
$('#basket-usual').onclick = addUsual;

function makeStaple(x) {
  if (!state.staplesLoaded) return;
  state.staples.push({ upc: x.upc, qty: x.qty, times: 0, name: x.name, selected: false });
  saveStaples();
  renderStaples();
  renderBasket();
  if (state.found.length) renderFind();
  banner(`${x.name || 'That item'} is now a staple.`, 'ok');
}

function openSheet(open) {
  $('#basket').classList.toggle('open', open);
  $('#basket-bar').setAttribute('aria-expanded', String(open));
  if (open) $('#basket-close').focus(); else $('#basket-bar').focus();
}
$('#basket-bar').onclick = () => openSheet(true);
$('#basket-close').onclick = () => openSheet(false);
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && $('#basket').classList.contains('open')) openSheet(false);
});

function withUndo(msg, next) {
  const before = new Map(state.draft);
  replaceDraft(next);
  banner(msg, 'ok', { label: 'Undo', run: () => replaceDraft(before) });
}
$('#basket-discard').onclick = () => withUndo('Changes discarded.', new Map());
$('#basket-empty').onclick = () => {
  const next = new Map();
  for (const l of state.cart.lines) next.set(l.upc, { upc: l.upc, name: nameFor(l.upc), qty: 0, from: inCart(l.upc) });
  withUndo('Everything is marked for removal. Press Update Kroger cart to send it.', next);
};

$('#basket-send').onclick = async () => {
  const items = changes().map((d) => ({ upc: d.upc, qty: d.qty }));
  if (!items.length || state.sending) return;
  state.sending = true;
  renderBasket();
  let out = null;
  try {
    out = await post('/api/cart/update', { items });
  } catch (err) {
    if (err.kind !== 'auth') banner(err.message, tone(err));
  } finally {
    state.sending = false;
  }
  const skipped = out?.skipped || [];
  noteStock(skipped.map((x) => ({ upc: x.upc, unavailable: x.reason })));
  for (const x of skipped) state.draft.delete(x.upc);
  saveDraft();
  await refreshCart({ quiet: true });
  if (!out) return;
  if ($('#basket').classList.contains('open')) openSheet(false);
  const sent = items.length - skipped.length;
  const notAdded = skipped.map((x) => `${x.name || nameFor(x.upc)} (${x.reason.toLowerCase()})`).join(', ');
  if (!skipped.length) banner(`Kroger cart updated: ${plural(sent, 'change')}.`, 'ok');
  else if (!sent) banner(`Nothing was sent. Not added: ${notAdded}.`, 'warn');
  else banner(`Kroger cart updated: ${plural(sent, 'change')}. Not added: ${notAdded}.`, 'warn');
};

$('#basket-to-recipe').onclick = () => {
  openRecipe({ slug: '', name: '', notes: '', ingredients: cartToBe().map((l) => ({ upc: l.upc, qty: l.qty, name: l.name })) });
};

$('#basket-to-usual').onclick = () => {
  const lines = cartToBe();
  if (!confirm(`Make these ${plural(lines.length, 'item')} your usual?\n\n“Add your usual” will bring your cart up to exactly these. You can also star staples one at a time.`)) return;
  const by = new Map(lines.map((l) => [l.upc, l]));
  for (const s of state.staples) {
    const l = by.get(s.upc);
    s.selected = !!l;
    if (l) s.qty = l.qty;
  }
  for (const l of lines) if (!stapleFor(l.upc)) state.staples.push({ upc: l.upc, qty: l.qty, times: 0, name: l.name, selected: true });
  saveStaples();
  renderStaples();
  renderBasket();
  banner(`Saved ${plural(lines.length, 'item')} as your usual.`, 'ok');
};

async function loadStaples() {
  try {
    const out = await api('/api/staples');
    state.staples = out.staples || [];
    state.staplesVersion = out.version || 0;
    state.staplesLoaded = true;
    state.staplesError = '';
  } catch (err) {
    state.staples = [];
    state.staplesLoaded = false;
    state.staplesError = err.message;
  }
  renderStaples();
  renderBasket();
  if (state.staplesLoaded) lookUp(state.staples.map((s) => s.upc)).then(renderEverything);
}

// Saves run one at a time and carry the file version, so hand edits are never overwritten.
let saveChain = Promise.resolve();
function saveStaples() {
  if (!state.staplesLoaded) return saveChain;
  saveChain = saveChain.then(async () => {
    try {
      const out = await put('/api/staples', { staples: state.staples, version: state.staplesVersion });
      state.staplesVersion = out.version;
    } catch (err) {
      banner(err.message, tone(err));
      if (err.kind === 'staples_changed') await loadStaples();
    }
  });
  return saveChain;
}

function renderStaples() {
  const list = $('#staples-list');
  keepingFocus(list, () => {
    list.textContent = '';
    $('#staples-seed').disabled = !state.staplesLoaded;
    if (state.staplesError) {
      list.append(emptyRow(`Your staples file couldn’t be read, so it’s left untouched: ${state.staplesError}. Fix that line, then reload.`));
      return;
    }
    const filter = state.filter.toLowerCase();
    const shown = state.staples.filter((s) => !filter || (s.name || '').toLowerCase().includes(filter) || s.upc.includes(filter));
    if (!state.staples.length) {
      list.append(emptyRow('No staples yet. Rebuild from your purchase history below, or find things in Search and use “Make it a staple” in the cart panel.'));
    } else if (!shown.length) {
      list.append(emptyRow(`None of your staples match “${state.filter}”. Try Search.`));
    }
    const buyable = shown.filter((s) => !unavailableFor(s.upc));
    const unavailable = shown.filter((s) => unavailableFor(s.upc));
    for (const s of buyable) list.append(stapleRow(s));
    if (unavailable.length) {
      list.append(el('div', { className: 'list-divider', textContent: `Unavailable right now · ${unavailable.length}` }));
      for (const s of unavailable) list.append(stapleRow(s));
    }
  });
}

function stapleRow(s) {
  const label = s.name || s.upc;
  const star = el('button', {
    type: 'button', className: `btn icon-only star${s.selected ? ' on' : ''}`,
    ariaLabel: s.selected ? `Remove ${label} from your usual` : `Add ${label} to your usual`,
    title: s.selected ? 'In your usual' : 'Add to your usual',
  }, [icon('star')]);
  star.setAttribute('aria-pressed', String(!!s.selected));
  star.onclick = () => {
    s.selected = !s.selected;
    saveStaples();
    renderStaples();
    renderBasket();
  };
  const forget = el('button', { type: 'button', className: 'btn icon-only forget', title: 'Remove from staples', ariaLabel: `Remove ${label} from your staples` }, [icon('trash')]);
  forget.onclick = () => {
    if (!confirm(`Remove “${label}” from your staples?\n\nRebuilding from purchase history can bring it back.`)) return;
    state.staples = state.staples.filter((x) => x.upc !== s.upc);
    saveStaples();
    renderStaples();
    renderBasket();
  };
  const meta = [
    ...priceBits(s.upc),
    s.times ? `Bought ${s.times} times` : 'Added by you',
    stockBadge(s.upc),
  ].filter(Boolean);
  const row = itemRow(s, { meta, after: forget });
  row.prepend(star);
  return row;
}

$('#staples-filter').oninput = (e) => { state.filter = e.target.value; renderStaples(); };

$('#staples-seed').onclick = () => run(async () => {
  const out = await post('/api/staples/seed', { trips: 100, minTimes: 2 },
    { busyText: 'Rebuilding from your last 100 trips. This can take a minute…' });
  state.staples = out.staples || [];
  state.staplesVersion = out.version || 0;
  renderStaples();
  renderBasket();
  if (!out.trips) {
    banner('Kroger has no order history for this account yet. Use Search, then “Make it a staple” to build your list.', 'warn');
    return;
  }
  banner(`Rebuilt from ${plural(out.trips, 'trip')}: ${plural(state.staples.length, 'staple')}. Your usual and quantities were kept.`, 'ok');
  lookUp(state.staples.map((s) => s.upc)).then(renderEverything);
});

function renderFind() {
  const list = $('#find-list');
  keepingFocus(list, () => {
    list.textContent = '';
    if (!state.found.length) {
      if (state.findTerm) list.append(emptyRow(`Nothing on kroger.com matched “${state.findTerm}”.`));
      return;
    }
    for (const item of state.found) {
      const meta = [item.size, ...priceBits(item.upc),
        stapleFor(item.upc) ? el('span', { className: 'badge', textContent: 'Staple' }) : null,
        stockBadge(item.upc)].filter(Boolean);
      list.append(itemRow(item, { meta }));
    }
  });
}

$('#find-form').onsubmit = (e) => {
  e.preventDefault();
  const term = $('#find-q').value.trim();
  if (!term) return;
  run(async () => {
    const out = await api(`/api/search?q=${encodeURIComponent(term)}&size=24`);
    state.found = out.items || [];
    noteStock(state.found);
    state.findTerm = term;
    renderFind();
  });
};

async function loadRecipes() {
  const out = await run(() => api('/api/recipes'));
  if (!out) return;
  state.recipes = out.recipes || [];
  renderRecipes();
}

function renderRecipes() {
  const list = $('#recipes-list');
  list.textContent = '';
  if (!state.recipes.length) list.append(emptyRow('No recipes yet. Start one with New recipe, or fill your cart and use “Save as recipe”.'));
  for (const r of state.recipes) {
    const ing = r.ingredients || [];
    const edit = el('button', { type: 'button', className: 'btn ghost sm', textContent: 'Edit', ariaLabel: `Edit ${r.name}` });
    edit.onclick = () => openRecipe(r);
    const add = el('button', { type: 'button', className: 'btn outline sm', ariaLabel: `Add ${r.name} to your cart` }, [icon('plus'), 'Add to cart']);
    add.disabled = !ing.length;
    add.onclick = () => addChecked(ing.map((i) => ({ upc: i.upc, name: i.name, qty: i.qty || 1 })), r.name, 'add');
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
    const name = i.name || i.upc;
    const minus = el('button', { type: 'button', ariaLabel: `One fewer ${name}` }, [icon('minus')]);
    minus.onclick = () => {
      if ((i.qty || 1) <= 1) state.editing.ingredients.splice(idx, 1); else i.qty = (i.qty || 1) - 1;
      renderIngredients();
    };
    const plus = el('button', { type: 'button', ariaLabel: `One more ${name}` }, [icon('plus')]);
    plus.onclick = () => { i.qty = (i.qty || 1) + 1; renderIngredients(); };
    list.append(el('div', { className: 'item' }, [
      el('span', { className: 'name' }, [el('b', { textContent: name })]),
      el('span', { className: 'qty-slot' }, [el('span', { className: 'stepper' }, [minus, el('output', { textContent: String(i.qty || 1) }), plus])]),
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
    const add = el('button', { type: 'button', className: 'btn outline sm', ariaLabel: `Add ${item.name}` }, already ? ['Added'] : [icon('plus'), 'Add']);
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
  if (!name) throw new ApiError('A recipe needs a name.', 'bad_request');
  const saved = await put('/api/recipes', {
    slug: state.editing.slug || '',
    name,
    notes: $('#recipe-notes').value.trim(),
    ingredients: state.editing.ingredients || [],
  });
  $('#recipe-dialog').close();
  const renamed = !state.editing.slug && saved.slug !== name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
  state.editing = null;
  await loadRecipes();
  banner(renamed ? `Saved ${name}. You already had a recipe with that name, so both are kept.` : `Saved ${name}.`, 'ok');
});

$('#recipe-delete').onclick = () => run(async () => {
  if (!confirm(`Delete ${state.editing.name}? The recipe file is removed.`)) return;
  await post('/api/recipes/delete', { slug: state.editing.slug });
  $('#recipe-dialog').close();
  state.editing = null;
  await loadRecipes();
  banner('Recipe deleted.', 'ok');
});

async function loadHistory() {
  const out = await run(() => api('/api/history?size=25'));
  if (!out) return;
  state.trips = out.trips || [];
  state.tripItems = out.items || {};
  const unchecked = new Set(out.unchecked || []);
  const all = [...new Set(state.trips.flatMap((t) => t.upcs))].filter((u) => !unchecked.has(u));
  noteStock(all.map((upc) => ({ upc, unavailable: '', ...(state.tripItems[upc] || { unavailable: NOT_FOUND }) })));
  renderHistory();
}

$('#history-reload').onclick = () => loadHistory();
$('#history-filter').oninput = (e) => {
  state.historyFilter = e.target.value;
  if (!state.historyFilter) state.openTrips.clear();
  renderHistory();
};

const tripKey = (t) => t.receiptKey || `${t.date}·${t.total}`;

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
  keepingFocus(list, () => {
    list.textContent = '';
    const filter = state.historyFilter.toLowerCase();
    const matches = (upc) => !filter || upc.includes(filter) || nameFor(upc).toLowerCase().includes(filter);
    let any = false;
    for (const t of state.trips) {
      const shown = t.upcs.filter(matches);
      if (filter && !shown.length) continue;
      any = true;
      const key = tripKey(t);
      if (filter) state.openTrips.add(key);
      const items = el('div', { className: 'trip-items' });
      const count = (upc) => t.counts?.[upc] || 1;
      const toItem = (upc) => ({ upc, name: nameFor(upc), qty: count(upc) });
      const fill = () => {
        if (items.childElementCount) return;
        for (const upc of shown) {
          const meta = [count(upc) > 1 ? `Bought ${count(upc)}` : null, ...priceBits(upc), stockBadge(upc)].filter(Boolean);
          items.append(itemRow(toItem(upc), { meta }));
        }
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
      const addAll = el('button', { type: 'button', className: 'btn outline sm', ariaLabel: `Add all from ${tripDate(t.date)}` }, [icon('plus'), 'Add all']);
      addAll.onclick = () => addChecked(shown.map(toItem), tripDate(t.date), 'atLeast');
      const row = el('div', { className: 'item trip' }, [head, addAll]);
      const setOpen = (open) => {
        row.classList.toggle('open', open);
        head.setAttribute('aria-expanded', String(open));
        items.hidden = !open;
        if (open) { fill(); state.openTrips.add(key); } else state.openTrips.delete(key);
      };
      head.onclick = () => setOpen(items.hidden);
      setOpen(state.openTrips.has(key));
      list.append(row, items);
    }
    if (!state.trips.length) list.append(emptyRow('No past trips on this account yet.'));
    else if (!any) list.append(emptyRow(`Nothing you've bought matches “${state.historyFilter}”.`));
  });
}

(async function boot() {
  renderBasket();
  try {
    state.status = await api('/api/status', { quiet: true });
  } catch (err) {
    banner(err.message, 'error');
    return;
  }
  renderStore();
  loaded.add('staples');
  await loadStaples();
  const wanted = location.hash.slice(1);
  showTab(document.getElementById(`tab-${wanted}`) ? wanted : 'staples');
  if (!state.status.hasCookie) {
    await onAuthFailure();
    if (!state.status.hasCookie) { state.cartLoaded = false; state.cartError = new ApiError('', 'auth'); renderEverything(); }
    return;
  }
  await refreshCart();
})();
