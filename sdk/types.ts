/** JSON Patch operation (RFC 6902). */
export interface PatchOp {
  op: "add" | "remove" | "replace";
  path: string;
  value?: unknown;
}

/** A reference to a normalized row, defining view structure. */
export interface Ref {
  table: string;
  id: string;
  fields: string[];
  nested?: Record<string, Ref>;
}

/** Message received over the transport layer. */
export interface TransportMessage {
  type: "table_diff" | "view_diff" | "view_snapshot";
  data: unknown;
}

/** Table diff payload — a single row changed. */
export interface TableDiff {
  table: string;
  id: string;
  ver: number;
  patch: PatchOp[];
}

/** View diff payload — refs structure changed. */
export interface ViewDiff {
  view: string;
  params_hash: string;
  version: number;
  refs_patch: PatchOp[];
  tables: Record<string, Record<string, Record<string, unknown>>>;
}

/** Subscribe response from the server. */
export interface SubscribeResponse {
  params_hash: string;
  version: number;
  refs: Ref[];
  tables: Record<string, Record<string, Record<string, unknown>>>;
}

/** API response envelope. */
export interface ApiResponse<T = unknown> {
  ok: boolean;
  data?: T;
  error?: string;
}

/** Configuration for ArcanaClient. */
export interface ArcanaConfig {
  /** Base URL for the Arcana HTTP endpoints (e.g., "/arcana"). */
  apiUrl: string;

  /** Transport client for receiving real-time updates. */
  transport: TransportClient;

  /** Optional fetch function override (for SSR or custom auth). */
  fetchFn?: typeof fetch;
}

/** Abstraction over the real-time transport (Centrifugo, WS, SSE). */
export interface TransportClient {
  /** Subscribe to a channel. Returns unsubscribe function. */
  subscribe(
    channel: string,
    handler: (msg: TransportMessage) => void
  ): () => void;

  /** Connect to the transport. */
  connect(): Promise<void>;

  /** Disconnect from the transport. */
  disconnect(): void;
}

/** Generic view map for type-safe subscriptions. */
export type ViewMap = Record<
  string,
  { params: Record<string, unknown>; refs: Ref[] }
>;

/** Generic table map for type-safe row access. */
export type TableMap = Record<string, Record<string, unknown>>;
