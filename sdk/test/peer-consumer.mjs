import assert from 'node:assert/strict';
import { writeFile } from 'node:fs/promises';
import { assertPeers, connection, waitFor, waitForSubscriptions, workspace } from './consumer.mjs';

const kind = process.argv[2];
assert.ok(['react', 'vue', 'svelte'].includes(kind));
assertPeers(kind);
const url = process.env.ARCANA_FIXTURE_URL;
assert.ok(url);
const nativeWebSocket = globalThis.WebSocket;
assert.equal(typeof nativeWebSocket, 'function');
const { JSDOM, VirtualConsole } = await import('jsdom');
const domErrors = [];
const virtualConsole = new VirtualConsole();
virtualConsole.on('jsdomError', error => domErrors.push(String(error)));
const dom = new JSDOM('<!doctype html><html><body><div id="app"></div></body></html>', {
  url, pretendToBeVisual: true, virtualConsole,
});
const descriptors = new Map();
function setGlobal(name, value) {
  descriptors.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
  Object.defineProperty(globalThis, name, { configurable: true, writable: true, value });
}
// Install DOM globals before importing any framework runtime. Keep Node's
// WebSocket/fetch and timers: the connection must reach the real Go fixture.
for (const name of [
  'window', 'document', 'navigator', 'Node', 'Element', 'HTMLElement', 'SVGElement',
  'Document', 'DocumentFragment', 'Text', 'Comment', 'CustomEvent',
  'HTMLInputElement', 'HTMLTextAreaElement', 'HTMLSelectElement', 'MutationObserver',
]) setGlobal(name, dom.window[name]);
for (const name of ['getComputedStyle', 'requestAnimationFrame', 'cancelAnimationFrame']) {
  setGlobal(name, dom.window[name].bind(dom.window));
}
assert.equal(globalThis.WebSocket, nativeWebSocket);
const { client } = connection(url);
const target = document.getElementById('app');
const expected = name => `1:${name}:a`;
let unmount;
try {
  await client.connect(workspace);
  assert.equal(client.status, 'connected');
  if (kind === 'react') {
    setGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    const { act, createElement } = await import('react');
    const { createRoot } = await import('react-dom/client');
    const { ArcanaProvider, useView, useRow } = await import('@frankfmy/arcana-sdk/adapters/react');
    function View() {
      const view = useView('items', { scope: 'a' });
      const row = useRow('items', 'row');
      return createElement('p', { 'data-version': view.version },
        view.loading ? 'loading' : `${view.data.length}:${row?.name}:${row?.scope}`);
    }
    const root = createRoot(target);
    unmount = async () => { await act(async () => root.unmount()); };
    // Commit effects first. Wait for client state inside act, then inspect DOM
    // after act flushes; waiting for a pending DOM render inside act can deadlock.
    act(() => root.render(createElement(ArcanaProvider, { client, children: createElement(View) })));
    await act(async () => {
      await waitFor(() => client.getRow('items', 'row')?.name === 'Before', 'React client row');
    });
    assert.equal(target.textContent, expected('Before'));
    await waitForSubscriptions(url, 1);
    await act(async () => {
      assert.deepEqual(await client.mutate('rename', { name: 'After React' }), { name: 'After React' });
      await waitFor(() => client.getRow('items', 'row')?.name === 'After React', 'React mutation diff');
    });
    assert.equal(target.textContent, expected('After React'));
  } else if (kind === 'vue') {
    const { createApp, defineComponent, h, nextTick } = await import('vue');
    const { provideArcana, useView, useRow } = await import('@frankfmy/arcana-sdk/adapters/vue');
    const Child = defineComponent({ setup() {
      const view = useView('items', () => ({ scope: 'a' }));
      const row = useRow('items', 'row', view.version);
      return () => h('p', { 'data-version': view.version.value }, view.loading.value ? 'loading'
        : `${view.data.value.length}:${row.value?.name}:${row.value?.scope}`);
    } });
    const app = createApp({ setup() {
      provideArcana(client);
      return () => h(Child);
    } });
    unmount = async () => { app.unmount(); await nextTick(); };
    app.mount(target);
    await waitFor(() => client.getRow('items', 'row')?.name === 'Before', 'Vue client row');
    await nextTick();
    assert.equal(target.textContent, expected('Before'));
    await waitForSubscriptions(url, 1);
    assert.deepEqual(await client.mutate('rename', { name: 'After Vue' }), { name: 'After Vue' });
    await waitFor(() => client.getRow('items', 'row')?.name === 'After Vue', 'Vue mutation diff');
    await nextTick();
    assert.equal(target.textContent, expected('After Vue'));
  } else {
    const { compile } = await import('svelte/compiler');
    const compiled = compile(`
      <script>
        import { getContext, onDestroy } from 'svelte';
        import { createSubscription } from '@frankfmy/arcana-sdk/adapters/svelte';
        const client = getContext('arcana');
        const view = createSubscription(client, 'items', { scope: 'a' });
        onDestroy(() => view.destroy());
        $: row = $view.data.length ? client.getRow('items', 'row') : null;
      </script>
      <p data-version={$view.version}>{$view.loading ? 'loading' : $view.data.length}:{row?.name ?? 'missing'}:{row?.scope ?? ''}</p>
    `, { filename: 'Consumer.svelte', generate: 'client', dev: true });
    assert.deepEqual(compiled.warnings, [], 'the Svelte fixture must compile without warnings');
    const output = new URL('./compiled-consumer.mjs', import.meta.url);
    await writeFile(output, compiled.js.code);
    const { mount, unmount: unmountSvelte, tick } = await import('svelte');
    const { default: Component } = await import(output.href);
    const app = mount(Component, { target, context: new Map([['arcana', client]]) });
    unmount = async () => { await unmountSvelte(app); await tick(); };
    await tick();
    await waitFor(() => client.getRow('items', 'row')?.name === 'Before', 'Svelte client row');
    await tick();
    assert.equal(target.textContent, expected('Before'));
    await waitForSubscriptions(url, 1);
    assert.deepEqual(await client.mutate('rename', { name: 'After Svelte' }), { name: 'After Svelte' });
    await waitFor(() => client.getRow('items', 'row')?.name === 'After Svelte', 'Svelte mutation diff');
    await tick();
    assert.equal(target.textContent, expected('After Svelte'));
  }
  await unmount();
  unmount = undefined;
  assert.equal(target.textContent, '');
  await waitForSubscriptions(url, 0);
} finally {
  try { await unmount?.(); }
  finally {
    client.disconnect();
    dom.window.close();
    for (const [name, descriptor] of descriptors) {
      if (descriptor) Object.defineProperty(globalThis, name, descriptor);
      else delete globalThis[name];
    }
  }
}
assert.equal(client.status, 'disconnected');
assert.equal(globalThis.WebSocket, nativeWebSocket);
assert.deepEqual(domErrors, []);
await waitForSubscriptions(url, 0);
console.log(`installed ${kind} mount/update/unmount contract: PASS`);
