import type {
  TransportClient,
  TransportMessage,
  SubscribeResponse,
  MutateResponse,
  ViewTransportClient,
} from "../types";

/** Configuration for the built-in WebSocket transport. */
export interface WSTransportConfig {
  /** WebSocket URL (e.g., "ws://localhost:8080/arcana/ws"). */
  url: string;
  /** Returns an auth token for the WebSocket handshake. */
  auth: () => Promise<string> | string;
  /** Called after a successful reconnect. */
  onReconnect?: () => void;
  /** Base delay for reconnect backoff in ms. Default: 500. */
  reconnectBaseDelay?: number;
  /** Max delay for reconnect backoff in ms. Default: 30000. */
  reconnectMaxDelay?: number;
  /** Timeout for subscribe/unsubscribe requests in ms. Default: 10000. */
  requestTimeout?: number;
}

interface PendingRequest {
  resolve: (data: any) => void;
  reject: (err: Error) => void;
  timer: ReturnType<typeof setTimeout>;
}

/**
 * Built-in WebSocket transport for Arcana.
 * Implements both TransportClient and ViewTransportClient for
 * subscribe/unsubscribe via the WS connection.
 */
export class ArcanaWSTransport implements ViewTransportClient {
  private _ws: WebSocket | null = null;
  private _connected = false;
  private _destroyed = false;
  private _seanceId: string | null = null;
  private _handlers: Map<string, Set<(msg: TransportMessage) => void>> =
    new Map();
  private _pendingRequests: Map<string, PendingRequest> = new Map();
  private _reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private _reconnectDelay: number;
  private _requestSeq = 0;
  private _connectResolve: (() => void) | null = null;
  private _connectReject: ((err: Error) => void) | null = null;
  private _reconnectHandler: (() => void) | null = null;

  private readonly config: Required<
    Pick<WSTransportConfig, "url" | "auth" | "reconnectBaseDelay" | "reconnectMaxDelay" | "requestTimeout">
  > & Pick<WSTransportConfig, "onReconnect">;

  constructor(config: WSTransportConfig) {
    this.config = {
      url: config.url,
      auth: config.auth,
      onReconnect: config.onReconnect,
      reconnectBaseDelay: config.reconnectBaseDelay ?? 500,
      reconnectMaxDelay: config.reconnectMaxDelay ?? 30000,
      requestTimeout: config.requestTimeout ?? 10000,
    };
    this._reconnectDelay = this.config.reconnectBaseDelay;
  }

  get seanceId(): string | null {
    return this._seanceId;
  }

  setReconnectHandler(handler: () => void): void {
    this._reconnectHandler = handler;
  }

  async connect(): Promise<void> {
    if (this._destroyed) throw new Error("Transport destroyed");

    return new Promise<void>((resolve, reject) => {
      this._connectResolve = resolve;
      this._connectReject = reject;
      this.openSocket();
    });
  }

  disconnect(): void {
    this._destroyed = true;
    this.cleanup();
  }

  subscribe(
    channel: string,
    handler: (msg: TransportMessage) => void
  ): () => void {
    let handlers = this._handlers.get(channel);
    if (!handlers) {
      handlers = new Set();
      this._handlers.set(channel, handlers);
    }
    handlers.add(handler);

    return () => {
      handlers!.delete(handler);
      if (handlers!.size === 0) {
        this._handlers.delete(channel);
      }
    };
  }

  async subscribeView(
    view: string,
    params: Record<string, unknown>
  ): Promise<SubscribeResponse> {
    if (!this._connected) throw new Error("Not connected");

    const id = this.nextId();
    this.sendWS({ id, type: "subscribe", view, params });

    const reply = await this.waitForReply(id);

    if (reply.type === "error") {
      throw new Error(reply.message || reply.code || "Subscribe failed");
    }

    return {
      params_hash: reply.params_hash,
      version: reply.version,
      refs: reply.refs ?? [],
      tables: reply.tables ?? {},
      seance_id: reply.seance_id ?? this._seanceId ?? "",
    };
  }

  unsubscribeView(paramsHash: string): void {
    if (!this._connected) return;
    const id = this.nextId();
    this.sendWS({ id, type: "unsubscribe", params_hash: paramsHash });
  }

  async mutateView(
    action: string,
    params: Record<string, unknown>
  ): Promise<MutateResponse> {
    if (!this._connected) throw new Error("Not connected");

    const id = this.nextId();
    this.sendWS({ id, type: "mutate", action, params });

    const reply = await this.waitForReply(id);

    if (reply.type === "error") {
      throw new Error(reply.message || reply.code || "Mutation failed");
    }

    return {
      ok: true,
      data: reply.data,
    };
  }

  private openSocket(): void {
    const ws = new WebSocket(this.config.url);
    this._ws = ws;

    ws.onopen = async () => {
      try {
        const token = await this.config.auth();
        const authId = this.nextId();
        this.sendWS({ id: authId, type: "auth", token });

        const reply = await this.waitForReply(authId);

        if (reply.type === "error") {
          throw new Error(reply.message || "Auth failed");
        }

        this._seanceId = reply.seance_id ?? null;
        this._connected = true;
        this._reconnectDelay = this.config.reconnectBaseDelay;
        this._connectResolve?.();
        this._connectResolve = null;
        this._connectReject = null;
      } catch (err) {
        this._connectReject?.(err instanceof Error ? err : new Error(String(err)));
        this._connectResolve = null;
        this._connectReject = null;
        ws.close();
      }
    };

    ws.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data as string);
        this.handleServerMessage(msg);
      } catch {
        // Ignore malformed messages
      }
    };

    ws.onclose = () => {
      const wasConnected = this._connected;
      this._connected = false;
      this._ws = null;

      // Reject all pending requests
      for (const [, pending] of this._pendingRequests) {
        clearTimeout(pending.timer);
        pending.reject(new Error("Connection closed"));
      }
      this._pendingRequests.clear();

      if (!wasConnected && this._connectReject) {
        this._connectReject(new Error("Connection closed before auth"));
        this._connectResolve = null;
        this._connectReject = null;
        return;
      }

      if (!this._destroyed) {
        this.scheduleReconnect();
      }
    };

    ws.onerror = () => {
      // onclose will fire after onerror
    };
  }

  private handleServerMessage(msg: Record<string, any>): void {
    // Reply to a pending request
    if (msg.reply_to) {
      const pending = this._pendingRequests.get(msg.reply_to);
      if (pending) {
        clearTimeout(pending.timer);
        this._pendingRequests.delete(msg.reply_to);
        pending.resolve(msg);
      }
      return;
    }

    // Push message — dispatch to all handlers
    const transportMsg: TransportMessage = {
      type: msg.type,
      data: msg.data ?? msg,
    };

    for (const [, handlers] of this._handlers) {
      for (const handler of handlers) {
        handler(transportMsg);
      }
    }
  }

  private sendWS(msg: Record<string, unknown>): void {
    if (!this._ws || this._ws.readyState !== WebSocket.OPEN) return;
    this._ws.send(JSON.stringify(msg));
  }

  private waitForReply(id: string): Promise<Record<string, any>> {
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this._pendingRequests.delete(id);
        reject(new Error("Request timeout"));
      }, this.config.requestTimeout);

      this._pendingRequests.set(id, { resolve, reject, timer });
    });
  }

  private nextId(): string {
    return String(++this._requestSeq);
  }

  private scheduleReconnect(): void {
    if (this._reconnectTimer) return;

    this._reconnectTimer = setTimeout(() => {
      this._reconnectTimer = null;

      this._connectResolve = () => {
        this.config.onReconnect?.();
        this._reconnectHandler?.();
      };
      this._connectReject = () => {
        this._connectResolve = null;
        this._connectReject = null;
        this._reconnectDelay = Math.min(
          this._reconnectDelay * 2,
          this.config.reconnectMaxDelay
        );
        if (!this._destroyed) {
          this.scheduleReconnect();
        }
      };

      this.openSocket();
    }, this._reconnectDelay);
  }

  private cleanup(): void {
    if (this._reconnectTimer) {
      clearTimeout(this._reconnectTimer);
      this._reconnectTimer = null;
    }

    for (const [, pending] of this._pendingRequests) {
      clearTimeout(pending.timer);
      pending.reject(new Error("Transport destroyed"));
    }
    this._pendingRequests.clear();

    this._ws?.close();
    this._ws = null;
    this._connected = false;
    this._handlers.clear();
  }
}
