import {
  inject,
  provide,
  ref,
  shallowRef,
  readonly,
  watch,
  onUnmounted,
  type InjectionKey,
  type Ref as VueRef,
  type DeepReadonly,
} from "vue";
import type { ArcanaClient, ViewHandle, ConnectionStatus } from "../client";
import type { Ref } from "../types";

const ArcanaKey: InjectionKey<ArcanaClient> = Symbol("arcana");

/**
 * Provides ArcanaClient to descendant components.
 *
 * Call in the root component's setup():
 * ```ts
 * provideArcana(client);
 * ```
 */
export function provideArcana(client: ArcanaClient): void {
  provide(ArcanaKey, client);
}

/**
 * Returns the ArcanaClient from the nearest provideArcana() ancestor.
 *
 * Throws if not within a provider.
 */
export function useArcana(): ArcanaClient {
  const client = inject(ArcanaKey);
  if (!client) {
    throw new Error("useArcana must be used within a component that calls provideArcana()");
  }
  return client;
}

export interface UseViewReturn {
  data: DeepReadonly<VueRef<Ref[]>>;
  version: DeepReadonly<VueRef<number>>;
  loading: DeepReadonly<VueRef<boolean>>;
}

/**
 * Subscribes to a view and returns reactive refs.
 *
 * Re-subscribes automatically when graphKey or params change.
 *
 * Usage:
 * ```ts
 * const { data, version, loading } = useView("users_list", () => ({ limit: 20 }));
 * ```
 */
export function useView(
  graphKey: string | VueRef<string>,
  params: VueRef<Record<string, unknown>> | (() => Record<string, unknown>) = () => ({})
): UseViewReturn {
  const client = useArcana();
  const data = shallowRef<Ref[]>([]);
  const version = ref(0);
  const loading = ref(true);

  const resolveParams = typeof params === "function" ? params : () => params.value;
  const resolveKey = typeof graphKey === "string" ? () => graphKey : () => graphKey.value;

  let handle: ViewHandle | null = null;
  let destroyed = false;

  function cleanup() {
    destroyed = true;
    handle?.destroy();
    handle = null;
  }

  watch(
    [resolveKey, () => JSON.stringify(resolveParams())],
    () => {
      cleanup();
      destroyed = false;
      loading.value = true;

      const currentKey = resolveKey();
      const currentParams = resolveParams();
      const refBox: { handle: ViewHandle | null } = { handle: null };

      client
        .subscribe(currentKey, currentParams, {
          onUpdate: () => {
            if (!destroyed && refBox.handle) {
              data.value = [...refBox.handle.data];
              version.value = refBox.handle.version;
              loading.value = false;
            }
          },
        })
        .then((h) => {
          if (destroyed) {
            h.destroy();
            return;
          }
          handle = h;
          refBox.handle = h;
          data.value = [...h.data];
          version.value = h.version;
          loading.value = false;
        })
        .catch(() => {
          if (!destroyed) {
            loading.value = false;
          }
        });
    },
    { immediate: true }
  );

  onUnmounted(cleanup);

  return {
    data: readonly(data),
    version: readonly(version),
    loading: readonly(loading),
  };
}

/**
 * Reads a row from the normalized table store.
 *
 * Pass a version ref (from useView) to re-read when data updates.
 * Without a trigger, the returned ref only updates when id changes.
 *
 * Usage:
 * ```ts
 * const { data, version } = useView("tasks", () => ({ project_id: id }));
 * const row = useRow("tasks", "t1", version); // re-reads when version changes
 * ```
 */
export function useRow(
  table: string,
  id: string | VueRef<string>,
  trigger?: VueRef<number>
): VueRef<Record<string, unknown> | null> {
  const client = useArcana();
  const result = shallowRef<Record<string, unknown> | null>(null);

  const resolveId = typeof id === "string" ? () => id : () => id.value;

  const sources = trigger
    ? [resolveId, () => trigger.value] as const
    : [resolveId] as const;

  watch(
    sources,
    () => {
      result.value = client.getRow(table, resolveId());
    },
    { immediate: true }
  );

  return readonly(result) as VueRef<Record<string, unknown> | null>;
}

/**
 * Returns the current connection status as a reactive ref.
 *
 * Usage:
 * ```ts
 * const status = useStatus(); // "connected" | "connecting" | "disconnected" | "offline"
 * ```
 */
export function useStatus(): DeepReadonly<VueRef<ConnectionStatus>> {
  const client = useArcana();
  const status = ref<ConnectionStatus>(client.status);

  const unsubscribe = client.onStatusChange((s) => {
    status.value = s;
  });

  onUnmounted(unsubscribe);

  return readonly(status);
}

export interface UseMutationReturn<T = unknown> {
  mutate: (params?: Record<string, unknown>) => Promise<T | undefined>;
  loading: DeepReadonly<VueRef<boolean>>;
}

/**
 * Returns a mutation function for a registered action.
 *
 * Usage:
 * ```ts
 * const { mutate, loading } = useMutation<{ id: string }>("create_task");
 * const result = await mutate({ title: "Fix bug" });
 * ```
 */
export function useMutation<T = unknown>(action: string): UseMutationReturn<T> {
  const client = useArcana();
  const loading = ref(false);

  const mutate = async (
    params: Record<string, unknown> = {}
  ): Promise<T | undefined> => {
    loading.value = true;
    try {
      return await client.mutate<T>(action, params);
    } finally {
      loading.value = false;
    }
  };

  return { mutate, loading: readonly(loading) };
}

export type { ViewHandle, ArcanaClient, ConnectionStatus };
export type { Ref, ArcanaConfig, TransportClient } from "../types";
export type { StorageAdapter } from "../storage";
