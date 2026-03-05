import { ArcanaClient, type ViewHandle, type ConnectionStatus } from "../client";
import type { ArcanaConfig, Ref, TransportClient } from "../types";
import type { StorageAdapter } from "../storage";

/**
 * Creates an Arcana client instance for Svelte 5 applications.
 *
 * Usage:
 * ```svelte
 * <script lang="ts">
 *   import { createArcana } from "@arcana/svelte";
 *   import { IndexedDBStorageAdapter } from "@arcana/sdk";
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
 * Returns an object with $state-compatible properties.
 * The onUpdate callback triggers Svelte reactivity.
 */
export function createSubscription(
  client: ArcanaClient,
  graphKey: string,
  params: Record<string, unknown>
): {
  data: Ref[];
  version: number;
  loading: boolean;
  offline: boolean;
  destroy: () => void;
} {
  let state = $state({
    data: [] as Ref[],
    version: 0,
    loading: true,
    offline: client.isOffline,
  });

  let handle: ViewHandle | null = null;

  client
    .subscribe(graphKey, params, {
      onUpdate: () => {
        if (handle) {
          state.data = [...handle.data];
          state.version = handle.version;
          state.offline = client.isOffline;
        }
      },
    })
    .then((h) => {
      handle = h;
      state.data = [...h.data];
      state.version = h.version;
      state.loading = false;
      state.offline = client.isOffline;
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
    destroy() {
      handle?.destroy();
    },
  };
}

export type { ViewHandle, ArcanaClient, ConnectionStatus };
export type { Ref, ArcanaConfig, TransportClient } from "../types";
export type { StorageAdapter } from "../storage";
