import {
  createContext,
  createElement,
  useContext,
  useEffect,
  useState,
  useSyncExternalStore,
} from "react";
import type { ReactNode } from "react";
import type { ArcanaClient, ViewHandle, ConnectionStatus } from "../client";
import type { Ref } from "../types";

const ArcanaContext = createContext<ArcanaClient | null>(null);

/**
 * Provides ArcanaClient to the React component tree.
 *
 * Usage:
 * ```tsx
 * const client = new ArcanaClient({ apiUrl: "/arcana", transport });
 * <ArcanaProvider client={client}>
 *   <App />
 * </ArcanaProvider>
 * ```
 */
export function ArcanaProvider({
  client,
  children,
}: {
  client: ArcanaClient;
  children: ReactNode;
}): ReactNode {
  return createElement(ArcanaContext.Provider, { value: client }, children);
}

/** Returns the ArcanaClient from the nearest ArcanaProvider. */
export function useArcana(): ArcanaClient {
  const client = useContext(ArcanaContext);
  if (!client) {
    throw new Error("useArcana must be used within <ArcanaProvider>");
  }
  return client;
}

export interface UseViewResult {
  data: Ref[];
  version: number;
  loading: boolean;
}

/**
 * Subscribes to a view and returns reactive state.
 *
 * Re-subscribes automatically when graphKey or params change.
 * Params are compared by value (JSON.stringify), not by reference.
 *
 * Usage:
 * ```tsx
 * const { data, version, loading } = useView("users_list", { limit: 20 });
 * ```
 */
export function useView(
  graphKey: string,
  params: Record<string, unknown> = {}
): UseViewResult {
  const client = useArcana();
  const [state, setState] = useState<UseViewResult>({
    data: [],
    version: 0,
    loading: true,
  });

  const paramsKey = JSON.stringify(params);

  useEffect(() => {
    let destroyed = false;
    const ref: { handle: ViewHandle | null } = { handle: null };

    setState((prev) => (prev.loading ? prev : { ...prev, loading: true }));

    client
      .subscribe(graphKey, JSON.parse(paramsKey), {
        onUpdate: () => {
          if (!destroyed && ref.handle) {
            setState({
              data: [...ref.handle.data],
              version: ref.handle.version,
              loading: false,
            });
          }
        },
      })
      .then((h) => {
        if (destroyed) {
          h.destroy();
          return;
        }
        ref.handle = h;
        setState({
          data: [...h.data],
          version: h.version,
          loading: false,
        });
      })
      .catch(() => {
        if (!destroyed) {
          setState((prev) => ({ ...prev, loading: false }));
        }
      });

    return () => {
      destroyed = true;
      ref.handle?.destroy();
    };
  }, [client, graphKey, paramsKey]);

  return state;
}

/**
 * Reads a row from the normalized table store.
 *
 * Returns fresh data on each render. For automatic updates,
 * use within a component that re-renders via useView().
 */
export function useRow(
  table: string,
  id: string
): Record<string, unknown> | null {
  const client = useArcana();
  return client.getRow(table, id);
}

/**
 * Returns the current connection status, updating reactively.
 *
 * Usage:
 * ```tsx
 * const status = useStatus(); // "connected" | "connecting" | "disconnected" | "offline"
 * ```
 */
export function useStatus(): ConnectionStatus {
  const client = useArcana();
  return useSyncExternalStore(
    (cb) => client.onStatusChange(cb),
    () => client.status,
    () => "disconnected" as ConnectionStatus
  );
}

export interface UseMutationResult<T = unknown> {
  mutate: (params?: Record<string, unknown>) => Promise<T | undefined>;
  loading: boolean;
}

/**
 * Returns a mutation function for a registered action.
 *
 * Usage:
 * ```tsx
 * const { mutate, loading } = useMutation<{ id: string }>("create_task");
 * const result = await mutate({ title: "Fix bug" });
 * ```
 */
export function useMutation<T = unknown>(action: string): UseMutationResult<T> {
  const client = useArcana();
  const [loading, setLoading] = useState(false);
  const mountedRef = { current: true };

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const mutate = async (
    params: Record<string, unknown> = {}
  ): Promise<T | undefined> => {
    if (mountedRef.current) setLoading(true);
    try {
      return await client.mutate<T>(action, params);
    } finally {
      if (mountedRef.current) setLoading(false);
    }
  };

  return { mutate, loading };
}

export type { ViewHandle, ArcanaClient, ConnectionStatus };
export type { Ref, ArcanaConfig, TransportClient } from "../types";
export type { StorageAdapter } from "../storage";
