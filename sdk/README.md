# Arcana TypeScript SDK

Reactive client for the [Arcana](https://github.com/FrankFMY/arcana) sync engine. Manages graph subscriptions, applies real-time diffs, and provides a normalized in-memory store with offline persistence.

## Installation

```bash
npm install @arcana/sdk
# or
pnpm add @arcana/sdk
```

## Quick Start

```typescript
import { ArcanaClient, IndexedDBStorageAdapter } from "@arcana/sdk";

// 1. Create a transport (Centrifugo example)
const transport: TransportClient = {
  async connect() { /* connect to Centrifugo */ },
  disconnect() { /* disconnect */ },
  subscribe(channel, handler) {
    // Subscribe to Centrifugo channel, call handler on messages
    return () => { /* unsubscribe */ };
  },
};

// 2. Create client
const arcana = new ArcanaClient({
  apiUrl: "/arcana",
  transport,
  storage: new IndexedDBStorageAdapter(), // optional: offline persistence
});

// 3. Connect to a workspace
await arcana.connect("workspace-uuid");

// 4. Subscribe to a graph
const handle = await arcana.subscribe("user_list", {
  org_id: "550e8400-e29b-41d4-a716-446655440000",
  limit: 20,
});

// 5. Access data
console.log(handle.data);    // Ref[] — view structure
console.log(handle.version); // current version number

// 6. Get normalized row data
const user = arcana.getRow("users", "user-uuid");
console.log(user); // { id: "...", name: "Alice", email: "alice@..." }

// 7. Clean up
handle.destroy();
arcana.disconnect();
```

## Architecture

```
Browser
  ├── ArcanaClient
  │   ├── tableStore (Map<table, Map<id, fields>>)  — normalized rows
  │   ├── viewStore (Map<paramsHash, {refs, version}>)  — view structures
  │   └── storage? (IndexedDB / Memory)  — offline persistence
  │
  ├── Transport (Centrifugo / WebSocket / SSE)
  │   ├── workspace:{id} channel → table_diff messages
  │   └── views:{seanceID} channel → view_diff messages
  │
  └── HTTP API (/arcana/subscribe, /unsubscribe, /sync)
```

### Data Flow

1. **Subscribe** — HTTP POST to `/arcana/subscribe` returns initial `refs` + `tables`
2. **Real-time updates** — Transport delivers `table_diff` (row changes) and `view_diff` (structural changes)
3. **Client applies patches** — JSON Patch (RFC 6902) operations update the local store
4. **Callbacks fire** — `onUpdate` notifies the UI framework to re-render
5. **Offline persistence** — State snapshots are saved to IndexedDB (debounced)

## API Reference

### `ArcanaClient`

```typescript
const client = new ArcanaClient(config: ArcanaConfig & { storage?: StorageAdapter });
```

#### Configuration

```typescript
interface ArcanaConfig {
  apiUrl: string;           // Base URL for Arcana HTTP endpoints (e.g., "/arcana")
  transport: TransportClient; // Real-time transport
  fetchFn?: typeof fetch;   // Custom fetch (for SSR or auth injection)
}
```

#### Methods

| Method | Description |
|--------|-------------|
| `connect(workspaceId)` | Connect to transport, subscribe to workspace channel |
| `disconnect()` | Disconnect and clean up all subscriptions |
| `subscribe(graphKey, params, opts?)` | Subscribe to a graph view, returns `Promise<ViewHandle>`. `opts?: { onUpdate?: () => void }` |
| `getRow(table, id)` | Get a normalized row from the local store |
| `getTable(table)` | Get all rows for a table |
| `persist()` | Force save state to storage adapter |
| `clearCache()` | Clear offline cached data |
| `onStatusChange(callback)` | Listen for connection status changes, returns unsubscribe fn |

#### Properties

| Property | Type | Description |
|----------|------|-------------|
| `status` | `ConnectionStatus` | `"connected" \| "connecting" \| "disconnected" \| "offline"` |
| `isOffline` | `boolean` | Whether operating from cached data |

### `ViewHandle`

Returned by `subscribe()`. Provides reactive access to view data.

```typescript
interface ViewHandle<T = Ref[]> {
  readonly data: T;           // Current refs (view structure)
  readonly version: number;   // Current version
  readonly loading: boolean;  // Whether initial load is in progress
  destroy(): void;            // Unsubscribe and clean up
}

type OnUpdateCallback = () => void;
type ConnectionStatus = "connected" | "connecting" | "disconnected" | "offline";
type StatusCallback = (status: ConnectionStatus) => void;
```

### Types

```typescript
interface Ref {
  table: string;
  id: string;
  fields: string[];
  nested?: Record<string, Ref>;
}

interface PatchOp {
  op: "add" | "remove" | "replace";
  path: string;
  value?: unknown;
}

interface TransportMessage {
  type: "table_diff" | "view_diff" | "view_snapshot";
  data: unknown;
}

interface TableDiff {
  table: string;
  id: string;
  ver: number;
  patch: PatchOp[];
}

interface ViewDiff {
  view: string;
  params_hash: string;
  version: number;
  refs_patch: PatchOp[];
  tables: Record<string, Record<string, Record<string, unknown>>>;
}

interface SubscribeResponse {
  params_hash: string;
  version: number;
  refs: Ref[];
  tables: Record<string, Record<string, Record<string, unknown>>>;
}
```

## Offline Persistence

The SDK supports offline-first operation with pluggable storage adapters:

```typescript
import { ArcanaClient, IndexedDBStorageAdapter } from "@arcana/sdk";

const client = new ArcanaClient({
  apiUrl: "/arcana",
  transport,
  storage: new IndexedDBStorageAdapter("my-app"), // browser persistence
});
```

### How It Works

1. **On subscribe** — State is saved to storage (debounced 500ms)
2. **On update** — State is saved after each table_diff / view_diff
3. **On connect failure** — Client loads cached data and enters `"offline"` status
4. **On reconnect** — Client calls `/sync` to catch up missed changes

### Storage Adapters

| Adapter | Use Case |
|---------|----------|
| `IndexedDBStorageAdapter` | Browser — persists across page reloads |
| `MemoryStorageAdapter` | SSR / tests — in-memory only |

Custom adapter:

```typescript
interface StorageAdapter {
  save(data: ArcanaSnapshot): Promise<void>;
  load(): Promise<ArcanaSnapshot | null>;
  clear(): Promise<void>;
}
```

## Svelte 5 Adapter

```bash
import { createArcana, createSubscription } from "@arcana/sdk/adapters/svelte";
```

### `createArcana`

Creates an `ArcanaClient` instance:

```svelte
<script lang="ts">
  import { createArcana } from "@arcana/sdk/adapters/svelte";
  import { IndexedDBStorageAdapter } from "@arcana/sdk";

  const arcana = createArcana({
    apiUrl: "/arcana",
    transport: centrifugoTransport,
    storage: new IndexedDBStorageAdapter(),
  });

  $effect(() => {
    arcana.connect(workspaceId);
    return () => arcana.disconnect();
  });
</script>
```

### `createSubscription`

Reactive subscription helper that integrates with Svelte 5's `$state`:

```svelte
<script lang="ts">
  import { createArcana, createSubscription } from "@arcana/sdk/adapters/svelte";

  const arcana = createArcana({ apiUrl: "/arcana", transport });
  const users = createSubscription(arcana, "user_list", { org_id: orgId, limit: 20 });
</script>

{#if users.loading}
  <p>Loading...</p>
{:else if users.offline}
  <p>Offline — showing cached data</p>
{/if}

{#each users.data as ref}
  {@const user = arcana.getRow(ref.table, ref.id)}
  <p>{user?.name} — {user?.email}</p>
{/each}
```

### `createSubscription` Return Type

```typescript
{
  data: Ref[];        // Reactive refs array
  version: number;    // Current version
  loading: boolean;   // Initial load in progress
  offline: boolean;   // Operating from cache
  destroy(): void;    // Clean up
}
```

## Transport Implementation

The SDK expects a `TransportClient` interface. Here's an example with Centrifugo:

```typescript
import { Centrifuge } from "centrifuge";

function createCentrifugoTransport(url: string, token: string): TransportClient {
  const client = new Centrifuge(url, { token });
  const subscriptions = new Map<string, any>();

  return {
    async connect() {
      client.connect();
      await new Promise<void>((resolve) => {
        client.on("connected", () => resolve());
      });
    },

    disconnect() {
      client.disconnect();
    },

    subscribe(channel, handler) {
      const sub = client.newSubscription(channel);
      sub.on("publication", (ctx) => {
        handler(ctx.data);
      });
      sub.subscribe();
      subscriptions.set(channel, sub);

      return () => {
        sub.unsubscribe();
        subscriptions.delete(channel);
      };
    },
  };
}
```

## Exports

```typescript
// Main client
export { ArcanaClient } from "./client";
export type { ViewHandle, OnUpdateCallback, ConnectionStatus, StatusCallback } from "./client";

// Types
export type {
  PatchOp, Ref, TransportMessage, TableDiff, ViewDiff,
  SubscribeResponse, ApiResponse, ArcanaConfig, TransportClient,
  ViewMap, TableMap,
} from "./types";

// Storage
export { MemoryStorageAdapter, IndexedDBStorageAdapter } from "./storage";
export type { StorageAdapter, ArcanaSnapshot } from "./storage";
```
