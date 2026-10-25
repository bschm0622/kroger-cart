import { test, expect, type Page } from '@playwright/test';
import { request as httpRequest } from 'node:http';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

// One test per way this could break for someone using it. The fake Kroger
// (fakekroger) holds: milk (in stock), bananas (low), olive oil (out of stock,
// and already in the cart), sourdough (in stock), coffee (out of stock), and
// no answer at all for a discontinued salsa.

const FAKE = 'http://127.0.0.1:18788';

async function fakeCart(): Promise<Record<string, number>> {
  return (await fetch(`${FAKE}/__cart`)).json();
}

test.beforeEach(async () => {
  await fetch(`${FAKE}/__reset`, { method: 'POST' });
});

const row = (page: Page, list: string, name: string) =>
  page.locator(`${list} .item`).filter({ hasText: name });

async function openStaples(page: Page) {
  await page.goto('/#staples');
  // Prices arrive with the background stock lookup.
  await expect(row(page, '#staples-list', 'Test Whole Milk')).toContainText('$3.49');
}

test('staples show price and picture, and what Kroger can’t sell sits under its own heading', async ({ page }) => {
  await openStaples(page);
  await expect(row(page, '#staples-list', 'Test Whole Milk').locator('img.thumb')).toBeVisible();

  const divider = page.locator('#staples-list .list-divider');
  await expect(divider).toHaveText('Unavailable right now · 2');

  const order = await page.locator('#staples-list > *').allInnerTexts();
  const at = (s: string) => order.findIndex((t) => t.includes(s));
  expect(at('Test Olive Oil')).toBeGreaterThan(at('Unavailable right now'));
  expect(at('Test Discontinued Salsa')).toBeGreaterThan(at('Unavailable right now'));
  expect(at('Test Whole Milk')).toBeLessThan(at('Unavailable right now'));

  await expect(row(page, '#staples-list', 'Test Discontinued Salsa')).toContainText('Not found at your store');
  // Olive oil is already in the cart, so it shows a stepper; the salsa can't be added at all.
  await expect(row(page, '#staples-list', 'Test Discontinued Salsa').getByRole('button', { name: 'Unavailable' })).toBeDisabled();
});

test('adding an in-stock staple and updating puts it in the Kroger cart', async ({ page }) => {
  await openStaples(page);
  await row(page, '#staples-list', 'Test Sourdough Bread').getByRole('button', { name: 'Add Test Sourdough Bread', exact: true }).click();
  await expect(row(page, '#basket-body', 'Test Sourdough Bread')).toContainText('New');

  await page.locator('#basket-send').click();
  await expect(page.locator('#banner')).toContainText('Kroger cart updated');
  expect((await fakeCart())['0000000000105']).toBe(1);
});

test('the server refuses an out-of-stock add the page didn’t catch', async ({ page }) => {
  // A pending change left over from before the item went out of stock.
  await page.addInitScript(() => {
    localStorage.setItem('kroger-cart:draft', JSON.stringify([{ upc: '0000000000103', name: 'Test Olive Oil', qty: 2 }]));
  });
  await openStaples(page);
  await page.locator('#basket-send').click();
  await expect(page.locator('#banner')).toContainText('Not added: Test Olive Oil');
  expect((await fakeCart())['0000000000103']).toBe(1);
});

test('an out-of-stock item already in the cart can be removed but not raised', async ({ page }) => {
  await openStaples(page);
  const line = row(page, '#basket-body', 'Test Olive Oil');
  await expect(line).toContainText('Item Out of Stock');
  await expect(line.getByRole('button', { name: /One more/ })).toBeDisabled();

  await line.getByRole('button', { name: /Remove Test Olive Oil/ }).click();
  await page.locator('#basket-send').click();
  await expect(page.locator('#banner')).toContainText('Kroger cart updated');
  expect(await fakeCart()).not.toHaveProperty('0000000000103');
});

test('search shows prices and marks what’s out of stock', async ({ page }) => {
  await page.goto('/#search');
  await page.locator('#find-q').fill('test');
  await page.locator('#find-q').press('Enter');
  await expect(row(page, '#find-list', 'Test Whole Milk')).toContainText('$3.49');
  await expect(row(page, '#find-list', 'Test Olive Oil')).toContainText('Item Out of Stock');
});

test('Add all on a past trip leaves out what’s out of stock, and says so', async ({ page }) => {
  await page.goto('/#history');
  await page.getByRole('button', { name: /^Add all/ }).click();
  await expect(page.locator('#banner')).toContainText('Left out: Test Coffee Beans');
  await expect(page.locator('#banner')).toContainText('Test Coffee Beans');
  await expect(row(page, '#basket-body', 'Test Sourdough Bread')).toContainText('New');
  await expect(row(page, '#basket-body', 'Test Coffee Beans')).toHaveCount(0);
});

test('nothing is wider than a phone screen', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await openStaples(page);
  const tooWide = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);
  expect(tooWide).toBe(false);
  await expect(page.locator('#basket-bar')).toBeVisible();
});

function rawRequest(method: string, path: string, headers: Record<string, string>): Promise<number> {
  return new Promise((resolve, reject) => {
    const req = httpRequest({ host: '127.0.0.1', port: 18787, method, path, headers }, (res) => {
      res.resume();
      resolve(res.statusCode ?? 0);
    });
    req.on('error', reject);
    req.end(method === 'POST' ? '{"items":[{"upc":"0000000000105","qty":1}]}' : undefined);
  });
}

test('the server only answers this machine’s own page', async () => {
  // DNS rebinding: another site's name pointed at 127.0.0.1.
  expect(await rawRequest('GET', '/api/history', { Host: 'evil.example:18787' })).toBe(403);
  // Another site posting to it from your browser.
  expect(await rawRequest('POST', '/api/cart/update', { Origin: 'https://evil.example', 'Content-Type': 'text/plain' })).toBe(403);
  expect(await fakeCart()).not.toHaveProperty('0000000000105');
});

test('set-store keeps only the store headers from a Copy as cURL paste', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'kroger-e2e-'));
  const config = join(dir, 'config.toml');
  writeFileSync(config, 'access_token = "keep=me"\n');
  const paste = `curl 'https://www.kroger.com/atlas/v1/search/v1/products-search?filter.query=milk' \\
  -H 'accept: application/json' \\
  -b 'secret-session=abc123' \\
  -H 'x-laf-object: [{"modality":{"type":"PICKUP"}}]' \\
  -H 'x-modality: {"type":"PICKUP","locationId":"00000"}'`;
  const out = spawnSync(join(__dirname, '..', '.tmp', 'kroger-pp-cli'), ['--config', config, 'auth', 'set-store', '-'], { input: paste });
  expect(out.status, out.stderr.toString()).toBe(0);
  const saved = readFileSync(config, 'utf8');
  expect(saved).toContain('x-laf-object');
  expect(saved).toContain('x-modality');
  expect(saved).toContain('keep=me'); // the existing sign-in is untouched
  expect(saved).not.toContain('secret-session');
});

const TMP = join(__dirname, '..', '.tmp');
const setFakeCart = (cart: Record<string, number>) => fetch(`${FAKE}/__cart`, { method: 'POST', body: JSON.stringify(cart) });
const failFake = (path: string, status: number, times: number) =>
  fetch(`${FAKE}/__fail`, { method: 'POST', body: JSON.stringify({ path, status, times }) });

test('a pending change doesn’t lower a cart that changed somewhere else', async ({ page }) => {
  // Milk was added here when the cart had none; since then the Kroger app put 3 in.
  await page.addInitScript(() => {
    localStorage.setItem('kroger-cart:draft', JSON.stringify([{ upc: '0000000000101', name: 'Test Whole Milk', qty: 1, from: 0 }]));
  });
  await setFakeCart({ '0000000000103': 1, '0000000000101': 3 });
  await openStaples(page);
  await expect(page.locator('#banner')).toContainText('changed somewhere else');
  await expect(page.locator('#basket-send')).toBeHidden();
  expect((await fakeCart())['0000000000101']).toBe(3);
});

test('an expired session asks you to sign in, and a working one brings the cart back', async ({ page }) => {
  await failFake('carts', 401, 1);
  await page.goto('/#staples');
  await expect(page.locator('#cookie-title')).toHaveText('Your Kroger session has expired');
  await page.getByText('Not using Chrome? Paste the session by hand').click();
  await page.locator('#cookie-input').fill('bm_sz=fresh; _abck=fresh');
  await page.getByRole('button', { name: 'Save pasted session' }).click();
  await expect(page.locator('#cookie-box')).toBeHidden();
  await expect(row(page, '#basket-body', 'Test Olive Oil')).toBeVisible();
});

test('a staples file that can’t be read is left untouched', async ({ page }) => {
  const file = join(TMP, 'staples.tsv');
  const original = readFileSync(file, 'utf8');
  const broken = `${original}not-a-number\t0000000000999\t1x\tBroken line\n`;
  writeFileSync(file, broken);
  try {
    await page.goto('/#staples');
    await expect(page.locator('#staples-list')).toContainText('couldn’t be read');
    await expect(page.locator('#staples-seed')).toBeDisabled();
    expect(readFileSync(file, 'utf8')).toBe(broken);
  } finally {
    writeFileSync(file, original);
  }
});

test('kroger-weekly-cart never lowers what’s already in the cart', async () => {
  await setFakeCart({ '0000000000101': 3 });
  const out = spawnSync(join(__dirname, '..', '..', 'scripts', 'kroger-weekly-cart'), [], {
    env: {
      ...process.env,
      HOME: join(TMP, 'home'),
      KROGER_CLI: join(TMP, 'kroger-pp-cli'),
      KROGER_CONFIG: join(TMP, 'config.toml'),
      KROGER_STAPLES: join(TMP, 'staples.tsv'),
    },
  });
  expect(out.status, out.stderr.toString()).toBe(0);
  expect((await fakeCart())['0000000000101']).toBe(3);
});

// Last: after a rate limit the server pauses its Kroger requests for a minute.
test('when Kroger rate-limits, nothing is marked missing and you aren’t asked to sign in', async ({ page }) => {
  await failFake('products', 429, 10);
  await page.goto('/#staples');
  await expect(row(page, '#basket-body', 'Test Olive Oil')).toBeVisible();
  await expect(page.locator('#staples-list .list-divider')).toHaveCount(0);
  await expect(row(page, '#staples-list', 'Test Discontinued Salsa')).not.toContainText('Not found');
  await expect(page.locator('#cookie-box')).toBeHidden();

  await page.getByRole('tab', { name: 'Search' }).click();
  await page.locator('#find-q').fill('milk');
  await page.locator('#find-q').press('Enter');
  await expect(page.locator('#banner')).toContainText('limiting requests');
});
