# Arcana TypeScript SDK

An ESM client for the [Arcana Go server](https://github.com/FrankFMY/Arcana): normalized rows, live graph subscriptions, mutation calls and optional offline persistence. React, Vue and Svelte integrations live in separate export paths; importing the core does not import a UI framework.

Package identity: [`@frankfmy/arcana-sdk`](https://www.npmjs.com/package/@frankfmy/arcana-sdk). Repository: [FrankFMY/Arcana, sdk/](https://github.com/FrankFMY/Arcana/tree/main/sdk). This is a client of that server protocol, not a standalone database or a replacement for server authorization.

## Build and install from source

The source path works independently of npm version availability:

```sh
git clone https://github.com/FrankFMY/Arcana.git
cd Arcana/sdk
npm ci
npm run build
npm pack --pack-destination ../..
cd ../..
mkdir arcana-sdk-example
cd arcana-sdk-example
npm init -y
npm install ../frankfmy-arcana-sdk-0.1.0.tgz
```

You can also install an available exact registry version with `npm install @frankfmy/arcana-sdk@0.1.0`. Check the [version page](https://www.npmjs.com/package/@frankfmy/arcana-sdk/v/0.1.0); a staged placeholder is not that version.

## Connect to your server

Use an application-issued credential and the graph/action keys actually registered by your Go server. The caller owns authentication, storage policy, connection lifetime and errors. For Node use an absolute HTTP(S) server URL. Browser applications should normally use same-origin HTTPS/WSS.

```ts
import {
  ArcanaClient,
  ArcanaWSTransport,
  MemoryStorageAdapter,
} from '@frankfmy/arcana-sdk';

export async function openClient(
  serverUrl: string,
  workspaceId: string,
  auth: () => string | Promise<string>,
) {
  const base = serverUrl.replace(/\/$/, '');
  const transport = new ArcanaWSTransport({
    url: base.replace(/^http/, 'ws') + '/arcana/ws',
    auth,
  });
  const client = new ArcanaClient({
    apiUrl: base + '/arcana',
    transport,
    storage: new MemoryStorageAdapter(),
  });
  await client.connect(workspaceId);
  if (client.status !== 'connected') {
    client.disconnect();
    throw new Error('The server connection is not available');
  }
  return client;
}
```

Await the initial connection before creating framework subscriptions. Create a new client for another workspace or authenticated principal; a client cannot be rebound to another workspace while retaining rows. `connect` is initialization, not an action to repeatedly invoke during rendering; the transport handles reconnects.

```ts
export async function inspectGraph(client: ArcanaClient, graphKey: string) {
  const view = await client.subscribe(graphKey, {});
  try {
    for (const ref of view.data) {
      console.log(ref.table, ref.id, client.getRow(ref.table, ref.id));
    }
  } finally {
    view.destroy();
  }
}
```

`subscribe(graphKey, params, { onUpdate })` resolves to a handle with live `data` references, `version`, `loading` and `destroy()`. Rows are stored separately: read them through `getRow(table, id)` or `getTable(table)`. Treat returned store data as read-only. `mutate<T>(action, params)` returns the server mutation data; it does not manufacture optimistic success. Inputs must be JSON-compatible objects. Parameters are detached at request entry so later caller edits do not change the submitted/offline identity.

The built-in `ArcanaWSTransport` uses WebSocket RPC for subscriptions/mutations. A supplied `TransportClient` may instead deliver channel publications while the client uses authenticated HTTP endpoints; supply `fetchFn` with the server's required authentication. Do not put privileged server credentials in browser code.

## Framework adapters and peer isolation

| Export | Runtime requirement | Main API |
| --- | --- | --- |
| `@frankfmy/arcana-sdk` | Native fetch/WebSocket; optional storage | `ArcanaClient`, `ArcanaWSTransport`, storage adapters and protocol types |
| `@frankfmy/arcana-sdk/adapters/react` | React `^19.2.4` | `ArcanaProvider`, `useArcana`, `useView`, `useRow`, `useStatus`, `useMutation` |
| `@frankfmy/arcana-sdk/adapters/vue` | Vue `^3.5.42` | `provideArcana`, `useArcana`, `useView`, `useRow`, `useStatus`, `useMutation` |
| `@frankfmy/arcana-sdk/adapters/svelte` | Standard Svelte store contract; no imported Svelte runtime | `createSubscription`, `getRow` |

React and Vue are optional peers: install only the one you use. The package does not bundle those frameworks. The Svelte adapter is runtime-free and is qualified with Svelte5.57.2; it does not impose an unrelated framework version cap.

React: pass an already connected `client` to `ArcanaProvider`; call hooks in descendants, and disconnect when the owning application closes. Vue: call `provideArcana(client)` in a parent setup function, not the same setup that calls `useArcana`/`useView`. Pass a getter or ref for reactive parameters. `useRow` takes the view's version ref as a trigger when rows need to follow updates. Late responses from replaced parameter generations are destroyed rather than replacing current state.

In Svelte, use the returned object's **store** value (`$view`), not plain getters as reactive state. For example, in a component below an application that provided an already connected client through context:

```svelte
<script>
  import { getContext, onDestroy } from 'svelte';
  import { createSubscription } from '@frankfmy/arcana-sdk/adapters/svelte';
  const client = getContext('arcana');
  const view = createSubscription(client, 'your_registered_graph', {});
  onDestroy(() => view.destroy());
</script>

{#if $view.loading}
  <p>Loading…</p>
{:else}
  <p>{$view.data.length} references</p>
{/if}
```

The graph name is application-defined. Context provisioning and connection/authentication belong to the application. The stores are not a substitute for initializing a client.

## Offline state and security

`MemoryStorageAdapter` is SSR-safe in-memory storage. `IndexedDBStorageAdapter(databaseName)` uses real browser IndexedDB and must only be operated in a browser. Call `await client.persist()` for a durable snapshot and `client.disconnect()` to release transport/listeners. The offline adapter is optional.

Snapshots are scoped to the connected workspace and the submitted JSON parameter identity; ambiguous legacy snapshots are ignored. Offline data is a cache, **not an authorization decision**. It is unencrypted, has no automatic expiration or revocation guarantee, and can outlive a server session. Give each principal/workspace its own storage database, discard old client instances on account changes and clear only your own adapter once its work is quiescent. Avoid durable storage when that policy cannot be met. Reauthorize all real operations on the server.

## Runtime and development

Qualification targets Node22 and24, real Chromium/WebKit core/IndexedDB behavior, React19.2.4, Vue3.5.42 and Svelte5.57.2. Other runtimes/OS/browser versions are not implied tested. No CommonJS output. Core imports are safe without a browser or UI peers, but browser-only persistence and rendered subscriptions are not SSR work.

From `sdk/`, run `npm ci`, `npm run verify`. The installed-package suite needs Go1.24+ for the repository's loopback fixture and the pinned Playwright Chromium/WebKit assets. Provision the matching browsers with `npx playwright install chromium webkit` and the platform dependencies described by Playwright. The suite installs the actual packed artifact into isolated consumers and exercises real Arcana engine/handler/WebSocket paths with an in-memory factory, not PostgreSQL CDC, production authentication, a crypto audit or an independent deployment. [Security boundaries](SECURITY.md) · [Changes](CHANGELOG.md).

## Author and license

Created and maintained by **Artem Prianishnikov / FrankFMY**.

- Email: [Pryanishnikovartem@gmail.com](mailto:Pryanishnikovartem@gmail.com) · [frankfmy@proton.me](mailto:frankfmy@proton.me)
- [Telegram](https://t.me/FrankFMY) · [LinkedIn](https://www.linkedin.com/in/frankfmy/) · [X](https://x.com/FrankFMY)
- [Website](https://frankfmy.com) · [GitHub](https://github.com/FrankFMY) · [npm](https://www.npmjs.com/~frankfmy)

MIT, with the complete original grant in [LICENSE](LICENSE). Frameworks and the server retain their own licenses.
