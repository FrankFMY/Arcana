import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { assertPeers, waitForSubscriptions } from './consumer.mjs';

assertPeers();
const browserName = process.argv[2];
assert.ok(['chromium', 'webkit'].includes(browserName));
const url = process.env.ARCANA_FIXTURE_URL;
assert.ok(url);
// Playwright is test tooling from the SDK; the browser app installs only the
// exact SDK artifact, and serves that installed package through the Go fixture.
const playwright = await import(process.env.ARCANA_PLAYWRIGHT_MODULE);
const browser = await playwright[browserName].launch({ headless: true });
const errors = [];
let context;
try {
  context = await browser.newContext();
  const page = await context.newPage();
  page.setDefaultTimeout(15_000);
  page.on('pageerror', error => errors.push(`pageerror: ${error.message}`));
  page.on('console', message => {
    if (message.type() === 'error') errors.push(`console: ${message.text()}`);
  });
  const response = await page.goto(url);
  assert.ok(response.ok());
  const dbName = `arcana-contract-${browserName}-${randomUUID()}`;
  const savedVersion = await page.evaluate(async ({ dbName }) => {
    const { ArcanaClient, ArcanaWSTransport, IndexedDBStorageAdapter } = await import('/package/dist/index.js');
    const check = (condition, message) => { if (!condition) throw new Error(message); };
    const storage = new IndexedDBStorageAdapter(dbName);
    check(await storage.load() === null, 'owned IndexedDB store must start empty');
    const client = new ArcanaClient({
      apiUrl: `${location.origin}/arcana`, storage,
      transport: new ArcanaWSTransport({
        url: `${location.origin.replace(/^http/, 'ws')}/arcana/ws`, auth: () => 'fixture', requestTimeout: 8_000,
      }),
    });
    let handle;
    let updates = 0;
    try {
      await client.connect('fixture-workspace');
      check(client.status === 'connected', 'browser WebSocket must authenticate');
      const params = { scope: 'a' };
      const pending = client.subscribe('items', params, { onUpdate: () => updates++ });
      params.scope = 'changed by caller';
      handle = await pending;
      check(handle.data.length === 1 && handle.data[0].id === 'row', 'real initial refs');
      check(client.getRow('items', 'row')?.name === 'Before', 'real initial row');
      check(client.getRow('items', 'row')?.scope === 'a', 'JSON parameters must be captured');
      const result = await client.mutate('rename', { name: 'After browser' });
      check(result?.name === 'After browser', 'real mutation response');
      const deadline = Date.now() + 10_000;
      while (client.getRow('items', 'row')?.name !== 'After browser' || updates === 0) {
        check(Date.now() < deadline, 'browser mutation diff timed out');
        await new Promise(resolve => setTimeout(resolve, 20));
      }
      await client.persist();
      const snapshot = await storage.load();
      check(snapshot?.workspaceId === 'fixture-workspace', 'persisted workspace identity');
      check(snapshot.tables.items.row.name === 'After browser', 'persisted real row');
      const views = Object.values(snapshot.views);
      check(views.length === 1 && views[0].graphKey === 'items' && views[0].params.scope === 'a', 'persisted graph/parameter identity');
      return handle.version;
    } finally {
      // Disconnect cancels pending persistence without saving an empty view set.
      client.disconnect();
      handle?.destroy();
    }
  }, { dbName });
  await waitForSubscriptions(url, 0);
  const beforeReload = page.url();
  await page.reload();
  assert.equal(page.url(), beforeReload, 'reload must retain the context and origin');
  await page.evaluate(async ({ dbName, savedVersion }) => {
    const { ArcanaClient, IndexedDBStorageAdapter } = await import('/package/dist/index.js');
    const check = (condition, message) => { if (!condition) throw new Error(message); };
    const storage = new IndexedDBStorageAdapter(dbName);
    check((await storage.load())?.tables.items.row.name === 'After browser', 'IndexedDB must survive reload');
    // Declared failure injection: no online responses are fabricated. These
    // base TransportClient instances intentionally fail to connect after reload.
    const offlineTransport = () => ({
      async connect() { throw new Error('Intentional offline persistence fixture'); },
      subscribe() { return () => {}; },
      disconnect() {},
    });
    const config = { apiUrl: `${location.origin}/arcana`, storage };
    const client = new ArcanaClient({ ...config, transport: offlineTransport() });
    const other = new ArcanaClient({ ...config, transport: offlineTransport() });
    const handles = [];
    try {
      await client.connect('fixture-workspace');
      check(client.status === 'offline' && client.isOffline, 'same workspace must restore offline');
      const correct = await client.subscribe('items', { scope: 'a' });
      handles.push(correct);
      check(correct.data.length === 1 && correct.data[0].table === 'items' && correct.data[0].id === 'row', 'same parameters must restore refs');
      check(!correct.loading && correct.version === savedVersion, 'cached handle semantics');
      check(client.getRow('items', 'row')?.name === 'After browser', 'restored normalized row');
      const wrong = await client.subscribe('items', { scope: 'wrong' });
      handles.push(wrong);
      check(wrong.data.length === 0 && wrong.version === 0, 'wrong parameters must not restore a view');
      await other.connect('other-fixture-workspace');
      check(other.status === 'disconnected', 'other workspace has no offline cache');
      check(other.getRow('items', 'row') === null && other.getTable('items') === undefined, 'other workspace must not expose rows');
      const missing = await other.subscribe('items', { scope: 'a' });
      handles.push(missing);
      check(missing.data.length === 0, 'other workspace must not expose refs');
    } finally {
      for (const handle of handles) handle.destroy();
      client.disconnect();
      other.disconnect();
      await storage.clear();
      check(await storage.load() === null, 'clear only the owned IndexedDB store');
    }
  }, { dbName, savedVersion });
  await waitForSubscriptions(url, 0);
} finally {
  try { await context?.close(); }
  finally { await browser.close(); }
}
assert.deepEqual(errors, [], 'unexpected browser errors');
console.log(`installed ${browserName} WebSocket/IndexedDB reload contract: PASS`);
