import { ArcanaClient, type ViewHandle } from "../client";
import type { ArcanaConfig, Ref } from "../types";

/**
 * Creates an Arcana client instance for Svelte 5 applications.
 *
 * Usage:
 * ```svelte
 * <script lang="ts">
 *   import { createArcana } from "@arcana/svelte";
 *
 *   const arcana = createArcana({ apiUrl: "/arcana", transport });
 *
 *   let labors = $state<Ref[]>([]);
 *   let loading = $state(true);
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
export function createArcana(config: ArcanaConfig): ArcanaClient {
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
  destroy: () => void;
} {
  let state = $state({
    data: [] as Ref[],
    version: 0,
    loading: true,
  });

  let handle: ViewHandle | null = null;

  client
    .subscribe(graphKey, params, {
      onUpdate: () => {
        if (handle) {
          state.data = [...handle.data];
          state.version = handle.version;
        }
      },
    })
    .then((h) => {
      handle = h;
      state.data = [...h.data];
      state.version = h.version;
      state.loading = false;
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
    destroy() {
      handle?.destroy();
    },
  };
}

export type { ViewHandle, ArcanaClient };
export type { Ref, ArcanaConfig, TransportClient } from "../types";
