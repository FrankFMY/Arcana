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
} from "./types.js";
import { isViewTransportClient } from "./types.js";
import type { StorageAdapter, ArcanaSnapshot } from "./storage.js";

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
  private connectionGeneration = 0;
  private pendingViews = new Map<string, Set<symbol>>();
  private physicalViews = new Map<string, Promise<SubscribeResponse>>();
  private retiringViews = new Map<string, Promise<void>>();
  private viewSubscribers = new Map<string, Map<symbol, OnUpdateCallback | undefined>>();
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
    if (this.workspaceId !== null && this.workspaceId !== workspaceId) {
      throw new Error("Create a new ArcanaClient for another workspace");
    }
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
          this.transport.setReconnectHandler(() => {
            // handleReconnect owns generation-bound status; an old rejection
            // must not turn an explicitly disconnected client back offline.
            void this.handleReconnect().catch(() => {});
          });
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
    this.connectionGeneration++;
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
    this.viewSubscribers.clear();
    this.pendingViews.clear();
    this.physicalViews.clear();
    this.retiringViews.clear();
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
    const generation = this.connectionGeneration;
    // Bind the exact JSON request before awaiting transport or caller mutations.
    params = JSON.parse(JSON.stringify(params)) as Record<string, unknown>;
    // If offline, return cached data
    if (this.isOffline) {
      return this.offlineSubscribe(graphKey, params, opts);
    }

    const identity = this.viewIdentity(graphKey, params);
    const pending = this.pendingViews.get(identity) ?? new Set<symbol>();
    const request = Symbol();
    pending.add(request);
    this.pendingViews.set(identity, pending);
    try {
      // The server admits one physical subscription per session/identity.
      // Concurrent and active owners share it, but each gets its own handle.
      let physical = this.physicalViews.get(identity);
      if (!physical) {
        physical = (async () => {
          const retirement = this.retiringViews.get(identity);
          if (retirement) await retirement;
          if (generation !== this.connectionGeneration) throw new Error("Client disconnected during subscription");
          if (isViewTransportClient(this.transport)) {
            return this.transport.subscribeView(graphKey, params);
          }
          const resp = await this.apiCall<SubscribeResponse>("subscribe", {
            view: graphKey,
            params,
          });
          if (!resp.ok || !resp.data) throw new Error(resp.error ?? "Subscribe failed");
          return resp.data;
        })();
        this.physicalViews.set(identity, physical);
        void physical.catch(() => {
          if (this.physicalViews.get(identity) === physical) this.physicalViews.delete(identity);
        });
      }
      const subResponse = await physical;

      if (generation !== this.connectionGeneration) throw new Error("Client disconnected during subscription");

      // Learn seanceId from first successful subscribe
      if (subResponse.seance_id && !this.seanceId) {
        this.seanceId = subResponse.seance_id;
        // Only subscribe seance channel for channel-based transports (not WS)
        if (this._status === "connected" && !isViewTransportClient(this.transport)) {
          this.subscribeSeanceChannel();
        }
      }

      const { params_hash, version, refs, tables } = subResponse;

      const previous = this.viewStore.get(params_hash);
      if (previous && (previous.graphKey !== graphKey || parameterKey(previous.params) !== parameterKey(params))) {
        throw new Error("Conflicting subscription identity from server");
      }
      const newer = !previous || version > previous.version;
      if (newer) {
        this.mergeTables(tables);
        this.viewStore.set(params_hash, { refs, version, graphKey, params });
      }
      const handle = this.createViewHandle(params_hash, opts?.onUpdate);
      if (previous && newer) this.viewStore.get(params_hash)?.onUpdate?.();
      this.schedulePersist();
      return handle;
    } finally {
      pending.delete(request);
      if (pending.size === 0 && this.pendingViews.get(identity) === pending) {
        this.pendingViews.delete(identity);
        for (const [hash, view] of this.viewStore) {
          if (this.viewIdentity(view.graphKey, view.params) === identity && !this.viewSubscribers.get(hash)?.size) {
            void this.unsubscribeView(hash);
          }
        }
      }
    }
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

    if (isViewTransportClient(this.transport)) {
      // The native server removes a WebSocket session's subscriptions on close.
      // Cached promises are not physical ownership in the new session.
      const generation = ++this.connectionGeneration;
      const retained = [...this.viewStore];
      this.physicalViews.clear();
      this.retiringViews.clear();
      this.pendingViews.clear();
      this.setStatus("connecting");
      try {
        await Promise.all(retained.map(async ([hash, view]) => {
          this.viewStore.delete(hash);
          try {
            const restored = await this.subscribe(view.graphKey, view.params);
            this.viewStore.get(hash)?.onUpdate?.();
            restored.destroy();
          } catch (error) {
            if (generation === this.connectionGeneration && !this.viewStore.has(hash) && this.viewSubscribers.get(hash)?.size) {
              this.viewStore.set(hash, view);
            }
            throw error;
          }
        }));
        if (generation === this.connectionGeneration) this.setStatus("connected");
      } catch (error) {
        if (generation === this.connectionGeneration) this.setStatus("offline");
        throw error;
      }
    } else {
      this.setStatus("connected");
      await this.syncCachedViews();
    }
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
    params: Record<string, unknown>,
    opts?: { onUpdate?: OnUpdateCallback }
  ): ViewHandle {
    // Find cached view matching graphKey
    for (const [hash, view] of this.viewStore) {
      if (view.graphKey === graphKey && parameterKey(view.params) === parameterKey(params)) {
        return this.createViewHandle(hash, opts?.onUpdate);
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

    const generation = this.connectionGeneration;
    const requested = new Map(this.viewStore);
    try {
      const resp = await this.apiCall<{
        views: Array<{
          view: string;
          params_hash: string;
          mode: "snapshot" | "catch_up";
          version?: number;
          refs?: Ref[];
          tables?: Record<string, Record<string, Record<string, unknown>>>;
          patches?: Array<{
            version: number;
            refs_patch: PatchOp[];
            tables?: Record<string, Record<string, Record<string, unknown>>>;
          }>;
        }>;
      }>("sync", { views });

      if (generation !== this.connectionGeneration) return;
      if (resp.ok && resp.data) {
        for (const result of resp.data.views) {
          const view = this.viewStore.get(result.params_hash);
          if (!view || view !== requested.get(result.params_hash) || view.graphKey !== result.view) continue;
          if (result.mode === "snapshot") {
            if (result.version === undefined || result.version < view.version) continue;
            view.refs = result.refs ?? [];
            view.version = result.version;
            if (result.tables) this.mergeTables(result.tables);
          } else {
            for (const patch of result.patches ?? []) {
              if (patch.version <= view.version) continue;
              if (patch.version !== view.version + 1) throw new Error("Non-contiguous sync history");
              applyRefsPatch(view.refs, patch.refs_patch);
              if (patch.tables) this.mergeTables(patch.tables);
              view.version = patch.version;
            }
          }
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
    if (!snapshot || snapshot.workspaceId !== this.workspaceId) return;

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
      if (!view.params) continue;
      this.viewStore.set(hash, {
        refs: view.refs,
        version: view.version,
        graphKey: view.graphKey,
        params: view.params,
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
        params: structuredClone(view.params),
      };
    }

    return { tables, views, timestamp: Date.now(), workspaceId: this.workspaceId ?? undefined };
  }

  private schedulePersist(): void {
    if (!this.storage) return;
    if (this.persistDebounce) clearTimeout(this.persistDebounce);
    this.persistDebounce = setTimeout(() => {
      this.persist();
    }, 500);
  }

  private viewIdentity(graphKey: string, params: Record<string, unknown>): string {
    return JSON.stringify([graphKey, parameterKey(params)]);
  }

  private createViewHandle(paramsHash: string, onUpdate?: OnUpdateCallback): ViewHandle {
    const view = this.viewStore.get(paramsHash)!;
    const subscribers = this.viewSubscribers.get(paramsHash) ?? new Map<symbol, OnUpdateCallback | undefined>();
    const token = Symbol();
    subscribers.set(token, onUpdate);
    this.viewSubscribers.set(paramsHash, subscribers);
    view.onUpdate = () => { for (const callback of subscribers.values()) callback?.(); };
    const store = this.viewStore;
    return {
      get data() { return store.get(paramsHash)?.refs ?? view.refs; },
      get version() { return store.get(paramsHash)?.version ?? view.version; },
      loading: false,
      destroy: () => {
        if (!subscribers.delete(token)) return;
        if (subscribers.size === 0 && this.viewSubscribers.get(paramsHash) === subscribers) {
          this.viewSubscribers.delete(paramsHash);
          if (!this.pendingViews.get(this.viewIdentity(view.graphKey, view.params))?.size) {
            void this.unsubscribeView(paramsHash);
          }
        }
      },
    };
  }

  private unsubscribeView(paramsHash: string): void {
    const view = this.viewStore.get(paramsHash);
    if (!view) return;

    const identity = this.viewIdentity(view.graphKey, view.params);
    this.viewStore.delete(paramsHash);
    this.physicalViews.delete(identity);

    const unsub = this.unsubscribers.get(paramsHash);
    unsub?.();
    this.unsubscribers.delete(paramsHash);

    const retirement = (async () => {
      if (!this.isOffline) {
        if (isViewTransportClient(this.transport)) {
          this.transport.unsubscribeView(paramsHash);
        } else {
          const response = await this.apiCall("unsubscribe", {
            view: view.graphKey,
            params_hash: paramsHash,
          });
          if (!response.ok) throw new Error(response.error ?? "Unsubscribe failed");
        }
      }
    })();
    this.retiringViews.set(identity, retirement);
    void retirement.then(() => {
      if (this.retiringViews.get(identity) === retirement) this.retiringViews.delete(identity);
    }, () => {
      // Retain uncertainty: a later subscribe must observe this rejection,
      // not race an unconfirmed removal with a duplicate server request.
    });
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

/** Compare JSON wire values independently of object-key insertion order. */
function parameterKey(params: Record<string, unknown>) {
  return JSON.stringify(params, (_key, value) => value && typeof value === "object" && !Array.isArray(value)
    ? Object.fromEntries(Object.entries(value).sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0))
    : value);
}

/** Apply the server's flat add/remove/replace row operations. */
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
    return aIsRemove ? idxB - idxA : idxA - idxB;
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
