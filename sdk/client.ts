import type {
  ArcanaConfig,
  ApiResponse,
  MutateResponse,
  SubscribeResponse,
  TableDiff,
  ViewDiff,
  TransportMessage,
  TransportClient,
  Ref,
  PatchOp,
} from "./types";
import { isViewTransportClient } from "./types";
import type { StorageAdapter, ArcanaSnapshot } from "./storage";

/** Reactive handle returned by subscribe(). */
export interface ViewHandle<T = Ref[]> {
  /** Current refs data. */
  readonly data: T;
  /** Current version. */
  readonly version: number;
  /** Whether initial load is in progress. */
  readonly loading: boolean;
  /** Unsubscribe and clean up. */
  destroy(): void;
}

/** Callback for view updates. */
export type OnUpdateCallback = () => void;

/** Connection status for offline awareness. */
export type ConnectionStatus = "connected" | "connecting" | "disconnected" | "offline";

/** Status change callback. */
export type StatusCallback = (status: ConnectionStatus) => void;

/**
 * ArcanaClient manages subscriptions, applies diffs, and provides
 * reactive access to synchronized data. Supports offline persistence.
 */
export class ArcanaClient {
  private tableStore: Map<string, Map<string, Record<string, unknown>>> =
    new Map();
  private viewStore: Map<
    string,
    {
      refs: Ref[];
      version: number;
      graphKey: string;
      params: Record<string, unknown>;
      onUpdate?: OnUpdateCallback;
    }
  > = new Map();
  private transport: TransportClient;
  private apiUrl: string;
  private fetchFn: typeof fetch;
  private unsubscribers: Map<string, () => void> = new Map();
  private workspaceUnsub: (() => void) | null = null;
  private seanceUnsub: (() => void) | null = null;
  private seanceId: string | null = null;
  private workspaceId: string | null = null;
  private storage: StorageAdapter | null;
  private _status: ConnectionStatus = "disconnected";
  private statusListeners: Set<StatusCallback> = new Set();
  private persistDebounce: ReturnType<typeof setTimeout> | null = null;

  constructor(config: ArcanaConfig & { storage?: StorageAdapter }) {
    this.apiUrl = config.apiUrl;
    this.transport = config.transport;
    this.fetchFn = config.fetchFn ?? fetch.bind(globalThis);
    this.storage = config.storage ?? null;
  }

  /** Current connection status. */
  get status(): ConnectionStatus {
    return this._status;
  }

  /** Whether operating from cached offline data. */
  get isOffline(): boolean {
    return this._status === "offline" || this._status === "disconnected";
  }

  /** Register a status change listener. Returns unsubscribe function. */
  onStatusChange(cb: StatusCallback): () => void {
    this.statusListeners.add(cb);
    return () => this.statusListeners.delete(cb);
  }

  private setStatus(status: ConnectionStatus): void {
    if (this._status === status) return;
    this._status = status;
    for (const cb of this.statusListeners) {
      cb(status);
    }
  }

  /** Connect to the transport and start listening for updates. */
  async connect(workspaceId: string): Promise<void> {
    this.workspaceId = workspaceId;
    this.setStatus("connecting");

    try {
      await this.transport.connect();

      // Subscribe to workspace channel for table_diff
      this.workspaceUnsub = this.transport.subscribe(
        `workspace:${workspaceId}`,
        (msg) => this.handleMessage(msg)
      );

      if (isViewTransportClient(this.transport)) {
        // WS transport: single pipe delivers all messages, no separate seance channel needed.
        // Learn seanceId from auth and wire reconnect handler.
        if (this.transport.seanceId) {
          this.seanceId = this.transport.seanceId;
        }
        if (this.transport.setReconnectHandler) {
          this.transport.setReconnectHandler(() => this.handleReconnect());
        }
      } else {
        // Channel-based transport (Centrifugo): subscribe seance channel separately
        if (this.seanceId) {
          this.subscribeSeanceChannel();
        }
      }

      this.setStatus("connected");

      // If we have cached data, sync to get missed updates
      if (this.viewStore.size > 0) {
        await this.syncCachedViews();
      }
    } catch {
      // Failed to connect — try to load from cache
      await this.loadFromCache();
      if (this.tableStore.size > 0) {
        this.setStatus("offline");
      } else {
        this.setStatus("disconnected");
      }
    }
  }

  /** Disconnect and clean up all subscriptions. */
  disconnect(): void {
    if (this.persistDebounce) {
      clearTimeout(this.persistDebounce);
      this.persistDebounce = null;
    }

    this.workspaceUnsub?.();
    this.workspaceUnsub = null;
    this.seanceUnsub?.();
    this.seanceUnsub = null;

    for (const unsub of this.unsubscribers.values()) {
      unsub();
    }
    this.unsubscribers.clear();
    this.viewStore.clear();
    this.tableStore.clear();
    this.transport.disconnect();
    this.setStatus("disconnected");
  }

  /**
   * Subscribe to a view. Returns a reactive handle.
   * In offline mode, returns cached data if available.
   */
  async subscribe(
    graphKey: string,
    params: Record<string, unknown>,
    opts?: { onUpdate?: OnUpdateCallback }
  ): Promise<ViewHandle> {
    // If offline, return cached data
    if (this.isOffline) {
      return this.offlineSubscribe(graphKey, params, opts);
    }

    let subResponse: SubscribeResponse;

    if (isViewTransportClient(this.transport)) {
      subResponse = await this.transport.subscribeView(graphKey, params);
    } else {
      const resp = await this.apiCall<SubscribeResponse>("subscribe", {
        view: graphKey,
        params,
      });
      if (!resp.ok || !resp.data) {
        throw new Error(resp.error ?? "Subscribe failed");
      }
      subResponse = resp.data;
    }

    // Learn seanceId from first successful subscribe
    if (subResponse.seance_id && !this.seanceId) {
      this.seanceId = subResponse.seance_id;
      // Only subscribe seance channel for channel-based transports (not WS)
      if (this._status === "connected" && !isViewTransportClient(this.transport)) {
        this.subscribeSeanceChannel();
      }
    }

    const { params_hash, version, refs, tables } = subResponse;

    // Populate table store
    this.mergeTables(tables);

    // Store view state
    this.viewStore.set(params_hash, {
      refs,
      version,
      graphKey,
      params,
      onUpdate: opts?.onUpdate,
    });

    this.schedulePersist();

    const store = this.viewStore;
    const handle: ViewHandle = {
      get data() {
        return store.get(params_hash)?.refs ?? refs;
      },
      get version() {
        return store.get(params_hash)?.version ?? version;
      },
      loading: false,
      destroy: () => {
        this.unsubscribeView(params_hash);
      },
    };

    return handle;
  }

  /** Execute a registered mutation. Views update reactively after completion. */
  async mutate<T = unknown>(
    action: string,
    params: Record<string, unknown> = {}
  ): Promise<T | undefined> {
    if (isViewTransportClient(this.transport) && this.transport.mutateView) {
      const resp = await this.transport.mutateView(action, params);
      return resp.data as T | undefined;
    }

    const resp = await this.apiCall<T>("mutate", { action, params });
    if (!resp.ok) {
      throw new Error(resp.error ?? "Mutation failed");
    }
    return resp.data;
  }

  /** Get a row from the normalized table store. */
  getRow(table: string, id: string): Record<string, unknown> | null {
    return this.tableStore.get(table)?.get(id) ?? null;
  }

  /** Get all rows for a table. */
  getTable(table: string): Map<string, Record<string, unknown>> | undefined {
    return this.tableStore.get(table);
  }

  /** Force persist current state to storage. */
  async persist(): Promise<void> {
    if (!this.storage) return;
    await this.storage.save(this.createSnapshot());
  }

  /** Clear cached offline data. */
  async clearCache(): Promise<void> {
    if (!this.storage) return;
    await this.storage.clear();
  }

  /** Re-subscribe channels and sync cached views after reconnect. */
  async handleReconnect(): Promise<void> {
    // Update seanceId from transport if available
    if (isViewTransportClient(this.transport) && this.transport.seanceId) {
      this.seanceId = this.transport.seanceId;
    }

    // Re-subscribe workspace channel
    if (this.workspaceId) {
      this.workspaceUnsub?.();
      this.workspaceUnsub = this.transport.subscribe(
        `workspace:${this.workspaceId}`,
        (msg) => this.handleMessage(msg)
      );
    }

    // Re-subscribe seance channel (only for channel-based transports)
    if (this.seanceId && !isViewTransportClient(this.transport)) {
      this.subscribeSeanceChannel();
    }

    this.setStatus("connected");
    await this.syncCachedViews();
  }

  private subscribeSeanceChannel(): void {
    if (!this.seanceId) return;
    this.seanceUnsub?.();
    this.seanceUnsub = this.transport.subscribe(
      `views:${this.seanceId}`,
      (msg) => this.handleMessage(msg)
    );
  }

  private offlineSubscribe(
    graphKey: string,
    _params: Record<string, unknown>,
    opts?: { onUpdate?: OnUpdateCallback }
  ): ViewHandle {
    // Find cached view matching graphKey
    for (const [hash, view] of this.viewStore) {
      if (view.graphKey === graphKey) {
        if (opts?.onUpdate) {
          view.onUpdate = opts.onUpdate;
        }
        const store = this.viewStore;
        return {
          get data() {
            return store.get(hash)?.refs ?? [];
          },
          get version() {
            return store.get(hash)?.version ?? 0;
          },
          loading: false,
          destroy: () => {
            this.viewStore.delete(hash);
          },
        };
      }
    }

    // No cached data for this view
    return {
      data: [],
      version: 0,
      loading: false,
      destroy: () => {},
    };
  }

  private async syncCachedViews(): Promise<void> {
    const views: Array<{ view: string; params_hash: string; version: number }> =
      [];
    for (const [hash, view] of this.viewStore) {
      views.push({
        view: view.graphKey,
        params_hash: hash,
        version: view.version,
      });
    }

    if (views.length === 0) return;

    try {
      const resp = await this.apiCall<{
        views: Record<
          string,
          {
            version: number;
            refs_patch: PatchOp[];
            tables: Record<string, Record<string, Record<string, unknown>>>;
          }
        >;
      }>("sync", { views });

      if (resp.ok && resp.data) {
        for (const [hash, diff] of Object.entries(resp.data.views)) {
          const view = this.viewStore.get(hash);
          if (!view) continue;

          if (diff.tables) {
            this.mergeTables(diff.tables);
          }
          if (diff.refs_patch) {
            applyRefsPatch(view.refs, diff.refs_patch);
          }
          view.version = diff.version;
          view.onUpdate?.();
        }
        this.schedulePersist();
      }
    } catch {
      // Sync failed — stale data is still better than nothing
    }
  }

  private async loadFromCache(): Promise<void> {
    if (!this.storage) return;
    const snapshot = await this.storage.load();
    if (!snapshot) return;

    // Restore table store
    this.tableStore.clear();
    for (const [tableName, rows] of Object.entries(snapshot.tables)) {
      const tableMap = new Map<string, Record<string, unknown>>();
      for (const [rowId, fields] of Object.entries(rows)) {
        tableMap.set(rowId, fields);
      }
      this.tableStore.set(tableName, tableMap);
    }

    // Restore view store
    this.viewStore.clear();
    for (const [hash, view] of Object.entries(snapshot.views)) {
      this.viewStore.set(hash, {
        refs: view.refs,
        version: view.version,
        graphKey: view.graphKey,
        params: {},
      });
    }
  }

  private createSnapshot(): ArcanaSnapshot {
    const tables: ArcanaSnapshot["tables"] = {};
    for (const [tableName, rows] of this.tableStore) {
      tables[tableName] = {};
      for (const [rowId, fields] of rows) {
        tables[tableName][rowId] = { ...fields };
      }
    }

    const views: ArcanaSnapshot["views"] = {};
    for (const [hash, view] of this.viewStore) {
      views[hash] = {
        refs: view.refs,
        version: view.version,
        graphKey: view.graphKey,
      };
    }

    return { tables, views, timestamp: Date.now() };
  }

  private schedulePersist(): void {
    if (!this.storage) return;
    if (this.persistDebounce) clearTimeout(this.persistDebounce);
    this.persistDebounce = setTimeout(() => {
      this.persist();
    }, 500);
  }

  private async unsubscribeView(paramsHash: string): Promise<void> {
    const view = this.viewStore.get(paramsHash);
    if (!view) return;

    this.viewStore.delete(paramsHash);

    const unsub = this.unsubscribers.get(paramsHash);
    unsub?.();
    this.unsubscribers.delete(paramsHash);

    if (!this.isOffline) {
      if (isViewTransportClient(this.transport)) {
        this.transport.unsubscribeView(paramsHash);
      } else {
        await this.apiCall("unsubscribe", {
          view: view.graphKey,
          params_hash: paramsHash,
        });
      }
    }

    this.schedulePersist();
  }

  private handleMessage(msg: TransportMessage): void {
    switch (msg.type) {
      case "table_diff":
        this.handleTableDiff(msg.data as TableDiff);
        break;
      case "view_diff":
        this.handleViewDiff(msg.data as ViewDiff);
        break;
    }
  }

  private handleTableDiff(diff: TableDiff): void {
    let tableMap = this.tableStore.get(diff.table);
    if (!tableMap) {
      tableMap = new Map();
      this.tableStore.set(diff.table, tableMap);
    }

    let row = tableMap.get(diff.id);
    if (!row) {
      row = {};
      tableMap.set(diff.id, row);
    }

    applyPatch(row, diff.patch);

    // Notify all views that reference this table
    for (const [, view] of this.viewStore) {
      view.onUpdate?.();
    }

    this.schedulePersist();
  }

  private handleViewDiff(diff: ViewDiff): void {
    const view = this.viewStore.get(diff.params_hash);
    if (!view) return;

    // Merge new table data
    if (diff.tables) {
      this.mergeTables(diff.tables);
    }

    // Apply refs patch
    applyRefsPatch(view.refs, diff.refs_patch);
    view.version = diff.version;

    view.onUpdate?.();
    this.schedulePersist();
  }

  private mergeTables(
    tables: Record<string, Record<string, Record<string, unknown>>>
  ): void {
    for (const [tableName, rows] of Object.entries(tables)) {
      let tableMap = this.tableStore.get(tableName);
      if (!tableMap) {
        tableMap = new Map();
        this.tableStore.set(tableName, tableMap);
      }
      for (const [rowId, fields] of Object.entries(rows)) {
        tableMap.set(rowId, { ...tableMap.get(rowId), ...fields });
      }
    }
  }

  private async apiCall<T>(
    endpoint: string,
    body: unknown
  ): Promise<ApiResponse<T>> {
    const resp = await this.fetchFn(`${this.apiUrl}/${endpoint}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });

    return (await resp.json()) as ApiResponse<T>;
  }
}

/** Apply JSON Patch operations to an object. */
function applyPatch(
  obj: Record<string, unknown>,
  patch: PatchOp[]
): void {
  for (const op of patch) {
    const key = op.path.replace(/^\//, "");

    switch (op.op) {
      case "add":
      case "replace":
        obj[key] = op.value;
        break;
      case "remove":
        delete obj[key];
        break;
    }
  }
}

/** Apply JSON Patch operations to a refs array. */
function applyRefsPatch(refs: Ref[], patch: PatchOp[]): void {
  // Sort: removes/soft-deletes first (descending index) to not shift indices
  const sorted = [...patch].sort((a, b) => {
    const aIsRemove = a.op === "remove" || (a.op === "replace" && a.value === null);
    const bIsRemove = b.op === "remove" || (b.op === "replace" && b.value === null);
    const idxA = parseInt(a.path.replace(/^\//, ""), 10);
    const idxB = parseInt(b.path.replace(/^\//, ""), 10);
    if (aIsRemove && !bIsRemove) return -1;
    if (bIsRemove && !aIsRemove) return 1;
    return idxB - idxA;
  });

  for (const op of sorted) {
    const idx = parseInt(op.path.replace(/^\//, ""), 10);

    switch (op.op) {
      case "add":
        if (idx >= refs.length) {
          refs.push(op.value as Ref);
        } else {
          refs.splice(idx, 0, op.value as Ref);
        }
        break;
      case "remove":
        if (idx < refs.length) {
          refs.splice(idx, 1);
        }
        break;
      case "replace":
        if (idx < refs.length) {
          if (op.value === null) {
            // Soft-delete: remove the ref from the array
            refs.splice(idx, 1);
          } else {
            refs[idx] = op.value as Ref;
          }
        }
        break;
    }
  }
}
