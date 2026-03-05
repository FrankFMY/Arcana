import type {
  ArcanaConfig,
  ApiResponse,
  SubscribeResponse,
  TableDiff,
  ViewDiff,
  TransportMessage,
  TransportClient,
  Ref,
  PatchOp,
} from "./types";

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

/**
 * ArcanaClient manages subscriptions, applies diffs, and provides
 * reactive access to synchronized data.
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
      onUpdate?: OnUpdateCallback;
    }
  > = new Map();
  private transport: TransportClient;
  private apiUrl: string;
  private fetchFn: typeof fetch;
  private unsubscribers: Map<string, () => void> = new Map();
  private workspaceUnsub: (() => void) | null = null;
  private workspaceId: string | null = null;

  constructor(config: ArcanaConfig) {
    this.apiUrl = config.apiUrl;
    this.transport = config.transport;
    this.fetchFn = config.fetchFn ?? fetch.bind(globalThis);
  }

  /** Connect to the transport and start listening for updates. */
  async connect(workspaceId: string): Promise<void> {
    this.workspaceId = workspaceId;
    await this.transport.connect();

    // Subscribe to workspace channel for table_diff
    this.workspaceUnsub = this.transport.subscribe(
      `workspace:${workspaceId}`,
      (msg) => this.handleMessage(msg)
    );
  }

  /** Disconnect and clean up all subscriptions. */
  disconnect(): void {
    this.workspaceUnsub?.();
    this.workspaceUnsub = null;

    for (const unsub of this.unsubscribers.values()) {
      unsub();
    }
    this.unsubscribers.clear();
    this.viewStore.clear();
    this.tableStore.clear();
    this.transport.disconnect();
  }

  /**
   * Subscribe to a view. Returns a reactive handle.
   */
  async subscribe(
    graphKey: string,
    params: Record<string, unknown>,
    opts?: { onUpdate?: OnUpdateCallback }
  ): Promise<ViewHandle> {
    const resp = await this.apiCall<SubscribeResponse>("subscribe", {
      view: graphKey,
      params,
    });

    if (!resp.ok || !resp.data) {
      throw new Error(resp.error ?? "Subscribe failed");
    }

    const { params_hash, version, refs, tables } = resp.data;

    // Populate table store
    this.mergeTables(tables);

    // Store view state
    this.viewStore.set(params_hash, {
      refs,
      version,
      graphKey,
      onUpdate: opts?.onUpdate,
    });

    // Subscribe to seance channel for view_diff (if not already)
    // The seance channel subscription is typically handled by the transport
    // layer based on the session ID. Here we just track it.

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

  /** Get a row from the normalized table store. */
  getRow(table: string, id: string): Record<string, unknown> | null {
    return this.tableStore.get(table)?.get(id) ?? null;
  }

  /** Get all rows for a table. */
  getTable(table: string): Map<string, Record<string, unknown>> | undefined {
    return this.tableStore.get(table);
  }

  private async unsubscribeView(paramsHash: string): Promise<void> {
    const view = this.viewStore.get(paramsHash);
    if (!view) return;

    this.viewStore.delete(paramsHash);

    const unsub = this.unsubscribers.get(paramsHash);
    unsub?.();
    this.unsubscribers.delete(paramsHash);

    await this.apiCall("unsubscribe", {
      view: view.graphKey,
      params_hash: paramsHash,
    });
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
