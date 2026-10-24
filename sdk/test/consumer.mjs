import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { dirname, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import {
  ArcanaClient, ArcanaWSTransport, MemoryStorageAdapter,
  IndexedDBStorageAdapter, isViewTransportClient,
} from '@frankfmy/arcana-sdk';
import { createArcana, createSubscription } from '@frankfmy/arcana-sdk/adapters/svelte';

export const workspace = 'fixture-workspace';
export const refs = [{ table: 'items', id: 'row', fields: ['name', 'scope'] }];

// This file is copied into each installed app, so resolution starts in that app.
export function assertPeers(kind = 'core') {
  const require = createRequire(import.meta.url);
  const expected = new Set(kind === 'react' ? ['react', 'react-dom'] : kind === 'core' ? [] : [kind]);
  const modules = resolve(dirname(fileURLToPath(import.meta.url)), 'node_modules') + sep;
  for (const peer of ['react', 'react-dom', 'vue', 'svelte']) {
    let found;
    try { found = require.resolve(peer); }
    catch (error) { assert.equal(error.code, 'MODULE_NOT_FOUND'); }
    if (expected.has(peer)) assert.ok(found?.startsWith(modules), `${peer} must resolve inside this app`);
    else assert.equal(found, undefined, `${peer} must be absent from this app`);
  }
}

export async function waitFor(predicate, label, timeout = 10_000) {
  const deadline = Date.now() + timeout;
  do {
    if (await predicate()) return;
    await delay(20);
  } while (Date.now() < deadline);
  throw new Error(`Timed out waiting for ${label}`);
}

export async function waitForSubscriptions(url, count) {
  await waitFor(async () => {
    const response = await fetch(`${url}/fixture-stats`, { signal: AbortSignal.timeout(2_000) });
    assert.ok(response.ok);
    const stats = await response.json();
    assert.equal(stats.running, true);
    return stats.active_subscriptions === count && (count !== 0 || stats.seances_with_subs === 0);
  }, `${count} engine subscriptions`);
}

export function connection(url, mode = 'ws', storage) {
  const ws = new ArcanaWSTransport({
    url: `${url.replace(/^http/, 'ws')}/arcana/ws`, auth: () => 'fixture', requestTimeout: 8_000,
  });
  const requests = [];
  const delivered = { table_diff: 0, view_diff: 0 };
  const delivery = { paused: false };
  const syncModes = [];
  // Intentional base TransportClient bridge: HTTP supplies view operations.
  // ArcanaWSTransport broadcasts pushes to its listeners; route each frame once.
  const transport = mode === 'http' ? {
    connect: () => ws.connect(),
    disconnect: () => ws.disconnect(),
    subscribe(channel, handler) {
      assert.ok(channel === `workspace:${workspace}` || channel === `views:${ws.seanceId}`);
      return ws.subscribe(channel, message => {
        if ((channel === `workspace:${workspace}` && message.type === 'table_diff') ||
            (channel === `views:${ws.seanceId}` && message.type === 'view_diff')) {
          delivered[message.type]++;
          if (!delivery.paused) handler(message);
        }
      });
    },
  } : ws;
  assert.equal(isViewTransportClient(transport), mode !== 'http');
  const client = new ArcanaClient({
    apiUrl: `${url}/arcana`, transport, storage,
    fetchFn: async (input, init) => {
      assert.ok(ws.seanceId, 'HTTP requests must use the authenticated WebSocket session');
      const headers = new Headers(init?.headers);
      headers.set('Authorization', 'Bearer fixture');
      headers.set('X-Fixture-Seance', ws.seanceId);
      requests.push(new URL(input).pathname);
      const response = await fetch(input, { ...init, headers, signal: AbortSignal.timeout(8_000) });
      assert.ok(response.ok, `HTTP ${response.status} at ${input}`);
      if (new URL(input).pathname.endsWith('/sync')) {
        const result = await response.clone().json();
        assert.equal(result.ok, true);
        syncModes.push(...result.data.views.map(view => view.mode));
      }
      return response;
    },
  });
  return { client, ws, requests, delivered, delivery, syncModes };
}

async function memoryContract() {
  const storage = new MemoryStorageAdapter();
  assert.equal(await storage.load(), null);
  const snapshot = {
    workspaceId: workspace, timestamp: 1,
    tables: { items: { row: { name: 'Before', tags: ['original'] } } },
    views: { fixture: { refs: structuredClone(refs), version: 1, graphKey: 'items', params: { scope: 'a' } } },
  };
  await storage.save(snapshot);
  snapshot.tables.items.row.tags[0] = 'changed after save';
  snapshot.views.fixture.params.scope = 'changed after save';
  const loaded = await storage.load();
  assert.deepEqual(loaded.tables.items.row.tags, ['original']);
  assert.deepEqual(loaded.views.fixture.params, { scope: 'a' });
  loaded.tables.items.row.tags.push('changed after load');
  loaded.views.fixture.refs[0].fields.push('changed after load');
  assert.deepEqual((await storage.load()).tables.items.row.tags, ['original']);
  assert.deepEqual((await storage.load()).views.fixture.refs, refs);
  await storage.clear();
  assert.equal(await storage.load(), null);
}

async function main() {
  const mode = process.argv[2] ?? 'ws';
  assert.ok(['ws', 'http'].includes(mode));
  assertPeers();
  assert.equal(typeof globalThis.document, 'undefined');
  assert.equal(typeof globalThis.window, 'undefined');
  assert.equal(typeof globalThis.indexedDB, 'undefined');
  assert.equal(typeof globalThis.WebSocket, 'function', 'Node native WebSocket is required');
  for (const exported of [IndexedDBStorageAdapter, createArcana, createSubscription]) assert.equal(typeof exported, 'function');
  await memoryContract();
  const url = process.env.ARCANA_FIXTURE_URL;
  assert.ok(url);
  const storage = new MemoryStorageAdapter();
  const { client, ws, requests, delivered, delivery, syncModes } = connection(url, mode, storage);
  const statuses = [];
  const stopStatus = client.onStatusChange(status => statuses.push(status));
  let handle;
  let updates = 0;
  assert.equal(client.status, 'disconnected');
  assert.equal(client.isOffline, true);
  try {
    await client.connect(workspace);
    assert.equal(client.status, 'connected');
    assert.equal(client.isOffline, false);
    assert.ok(ws.seanceId);
    const params = { scope: 'a' };
    const pending = client.subscribe('items', params, { onUpdate: () => updates++ });
    const concurrent = client.subscribe('items', { scope: 'a' });
    params.scope = 'changed by caller';
    handle = await pending;
    const concurrentHandle = await concurrent;
    const activeHandle = await client.subscribe('items', { scope: 'a' });
    assert.deepEqual(concurrentHandle.data, refs);
    assert.deepEqual(activeHandle.data, refs);
    await waitForSubscriptions(url, 1);
    concurrentHandle.destroy();
    activeHandle.destroy();
    await waitForSubscriptions(url, 1);
    if (mode === 'http') assert.equal(requests.filter(path => path === '/arcana/subscribe').length, 1);
    assert.equal(handle.loading, false);
    assert.ok(Number.isInteger(handle.version) && handle.version > 0);
    assert.deepEqual(handle.data, refs);
    assert.deepEqual(client.getRow('items', 'row'), { name: 'Before', scope: 'a' });
    assert.equal(client.getRow('items', 'missing'), null);
    assert.equal(client.getTable('items').size, 1);
    await waitForSubscriptions(url, 1);
    const version = handle.version;
    assert.deepEqual(await client.mutate('rename', { name: 'After' }), { name: 'After' });
    await waitFor(() => client.getRow('items', 'row')?.name === 'After' && updates > 0, 'real mutation diff');
    assert.deepEqual(handle.data, refs);
    assert.ok(handle.version >= version);
    await client.persist();
    const snapshot = await storage.load();
    assert.equal(snapshot.workspaceId, workspace);
    assert.deepEqual(Object.values(snapshot.views).map(view => view.params), [{ scope: 'a' }]);
    assert.equal(snapshot.tables.items.row.name, 'After');
    if (mode === 'http') {
      assert.ok(delivered.table_diff > 0);
      assert.equal(updates, delivered.table_diff + delivered.view_diff, 'pushes must not be delivered twice');
    }
    handle.destroy();
    handle.destroy();
    await waitForSubscriptions(url, 0);
    const stoppedUpdates = updates;
    await client.mutate('rename', { name: 'Unsubscribed' });
    await delay(75);
    assert.equal(updates, stoppedUpdates, 'destroy must stop view callbacks');
    const ordered = await client.subscribe('ordered', {});
    assert.deepEqual(ordered.data, []);
    const beforeDiffs = delivered.view_diff;
    if (mode === 'http') delivery.paused = true;
    await client.mutate('expand', {});
    if (mode === 'http') {
      await waitFor(() => delivered.view_diff > beforeDiffs, 'withheld native refs patch');
      assert.deepEqual(ordered.data, []);
      await client.handleReconnect();
      assert.ok(syncModes.includes('catch_up'));
    } else await waitFor(() => ordered.data.length === 3, 'native multiple ref additions');
    assert.deepEqual(ordered.data.map(ref => ref.id), ['a', 'b', 'c']);
    assert.equal(client.getRow('ordered', 'a').value, 'expanded-3');
    if (mode === 'http') {
      await client.handleReconnect();
      assert.ok(syncModes.includes('snapshot'));
      assert.deepEqual(ordered.data.map(ref => ref.id), ['a', 'b', 'c']);
      delivery.paused = false;
    } else {
      const previousSession = ws.seanceId;
      // Controlled real socket loss, not the transport's permanent destroy().
      ws._ws.close();
      await waitFor(() => ws.seanceId !== previousSession && client.status === 'connected', 'native automatic reconnect');
      await waitForSubscriptions(url, 1);
      assert.deepEqual(ordered.data.map(ref => ref.id), ['a', 'b', 'c']);
    }
    ordered.destroy(); await waitForSubscriptions(url, 0);
    if (mode === 'http') {
      for (const endpoint of ['subscribe', 'mutate', 'unsubscribe', 'sync']) assert.ok(requests.includes(`/arcana/${endpoint}`));
    } else assert.deepEqual(requests, [], 'native WebSocket view operations must not fall back to HTTP');
  } finally {
    handle?.destroy();
    client.disconnect();
    stopStatus();
    await storage.clear();
  }
  assert.equal(client.status, 'disconnected');
  assert.equal(client.isOffline, true);
  assert.equal(client.getRow('items', 'row'), null);
  assert.equal(await storage.load(), null);
  assert.deepEqual(statuses, mode === 'ws'
    ? ['connecting', 'connected', 'connecting', 'connected', 'disconnected']
    : ['connecting', 'connected', 'disconnected']);
  await waitForSubscriptions(url, 0);
  console.log(`installed core ${mode === 'ws' ? 'WebSocket' : 'HTTP fallback'} contract: PASS`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
