import { ArcanaClient, type ViewHandle, type ConnectionStatus } from "../client.js";
import type { ArcanaConfig, Ref, TransportClient } from "../types.js";
import type { StorageAdapter } from "../storage.js";

/**
 * Creates an Arcana client instance for Svelte 5 applications.
 *
 * Usage:
 * ```svelte
 * <script lang="ts">
 *   import { createArcana } from "@frankfmy/arcana-sdk/adapters/svelte";
 *   import { IndexedDBStorageAdapter } from "@frankfmy/arcana-sdk";
 *
 *   const arcana = createArcana({
 *     apiUrl: "/arcana",
 *     transport,
 *     storage: new IndexedDBStorageAdapter(),
 *   });
 *
 *   let labors = $state<Ref[]>([]);
 *   let loading = $state(true);
 *   let status = $state<ConnectionStatus>("disconnected");
 *
 *   arcana.onStatusChange(s => status = s);
 *
 *   $effect(() => {
 *     const handle = arcana.subscribe("organization_labors_list", {
 *       organization_id: orgId,
 *       limit: 20,
 *     });
 *     handle.then(h => {
 *       labors = h.data;
 *       loading = false;
 *     });
 *
 *     return () => handle.then(h => h.destroy());
 *   });
 * </script>
 * ```
 */
export function createArcana(
  config: ArcanaConfig & { storage?: StorageAdapter }
): ArcanaClient {
  return new ArcanaClient(config);
}

/**
 * Reactive subscription helper for Svelte 5.
 *
 * Returns a Svelte store-compatible object plus direct getters.
 * Avoids Svelte runes in library code, so the adapter works from plain
 * TypeScript and compiled packages.
 */
export interface SubscriptionSnapshot {
  data: Ref[];
  version: number;
  loading: boolean;
  offline: boolean;
  error: unknown | null;
}

export interface SvelteSubscription extends SubscriptionSnapshot {
  subscribe(run: (snapshot: SubscriptionSnapshot) => void): () => void;
  destroy(): void;
}

export function createSubscription(
  client: ArcanaClient,
  graphKey: string,
  params: Record<string, unknown>
): SvelteSubscription {
  let state: SubscriptionSnapshot = {
    data: [] as Ref[],
    version: 0,
    loading: true,
    offline: client.isOffline,
    error: null,
  };

  let handle: ViewHandle | null = null;
  let destroyed = false;
  const listeners = new Set<(snapshot: SubscriptionSnapshot) => void>();

  function notify(next: Partial<SubscriptionSnapshot>) {
    state = { ...state, ...next };
    for (const listener of listeners) {
      listener(state);
    }
  }

  client
    .subscribe(graphKey, params, {
      onUpdate: () => {
        if (!destroyed && handle) {
          notify({
            data: [...handle.data],
            version: handle.version,
            offline: client.isOffline,
          });
        }
      },
    })
    .then((h) => {
      if (destroyed) {
        h.destroy();
        return;
      }
      handle = h;
      notify({
        data: [...h.data],
        version: h.version,
        loading: false,
        offline: client.isOffline,
        error: null,
      });
    })
    .catch((error) => {
      if (!destroyed) {
        notify({ loading: false, error });
      }
    });

  return {
    get data() {
      return state.data;
    },
    get version() {
      return state.version;
    },
    get loading() {
      return state.loading;
    },
    get offline() {
      return state.offline;
    },
    get error() {
      return state.error;
    },
    subscribe(run) {
      run(state);
      listeners.add(run);
      return () => {
        listeners.delete(run);
      };
    },
    destroy() {
      destroyed = true;
      handle?.destroy();
      listeners.clear();
    },
  };
}

export type { ViewHandle, ArcanaClient, ConnectionStatus };
export type { Ref, ArcanaConfig, TransportClient } from "../types.js";
export type { StorageAdapter } from "../storage.js";
