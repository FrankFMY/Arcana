import { expect, test, vi } from "vitest";
import { ArcanaClient } from "./client.js";
import { MemoryStorageAdapter } from "./storage.js";
import type { SubscribeResponse, ViewTransportClient } from "./types.js";

function fixtureTransport(offline = false): ViewTransportClient {
  return {
    seanceId: "fixture-session",
    connect: async () => { if (offline) throw new Error("offline fixture"); },
    disconnect: vi.fn(),
    subscribe: () => () => {},
    unsubscribeView: vi.fn(),
    subscribeView: async (_view, params) => ({
      params_hash: String(params.scope), version: 1, seance_id: "fixture-session",
      refs: [{ table: "items", id: String(params.scope), fields: ["value"] }],
      tables: { items: { [String(params.scope)]: { value: params.scope } } },
    }),
  };
}

test("offline views retain the actual submitted parameter identity", async () => {
  const storage = new MemoryStorageAdapter();
  const online = new ArcanaClient({ apiUrl: "http://127.0.0.1", transport: fixtureTransport(), storage });
  await online.connect("workspace-a");
  const params = { scope: "a" };
  const pending = online.subscribe("items", params);
  params.scope = "b";
  await pending;
  await online.persist();
  online.disconnect();
  const offline = new ArcanaClient({ apiUrl: "http://127.0.0.1", transport: fixtureTransport(true), storage });
  try {
    await offline.connect("workspace-a");
    expect((await offline.subscribe("items", { scope: "b" })).data).toEqual([]);
    expect((await offline.subscribe("items", { scope: "a" })).data[0]?.id).toBe("a");
  } finally { offline.disconnect(); }
});

test("cached rows are not restored into another workspace", async () => {
  const storage = new MemoryStorageAdapter();
  const online = new ArcanaClient({ apiUrl: "http://127.0.0.1", transport: fixtureTransport(), storage });
  await online.connect("workspace-a");
  await online.subscribe("items", { scope: "a" });
  await online.persist();
  online.disconnect();
  const other = new ArcanaClient({ apiUrl: "http://127.0.0.1", transport: fixtureTransport(true), storage });
  try {
    await other.connect("workspace-b");
    expect(other.getRow("items", "a")).toBeNull();
    expect(other.status).toBe("disconnected");
  } finally { other.disconnect(); }
});

test("an existing client cannot be rebound to another workspace while retaining rows", async () => {
  const client = new ArcanaClient({ apiUrl: "http://127.0.0.1", transport: fixtureTransport() });
  try {
    await client.connect("workspace-a");
    await client.subscribe("items", { scope: "a" });
    await expect(client.connect("workspace-b")).rejects.toThrow(/new.*client/i);
    expect(client.getRow("items", "a")?.value).toBe("a");
    expect(client.status).toBe("connected");
  } finally { client.disconnect(); }
});


test("a late subscribe response cannot repopulate a disconnected client", async () => {
  const transport = fixtureTransport();
  let complete!: (response: SubscribeResponse) => void;
  transport.subscribeView = () => new Promise(resolve => { complete = resolve; });
  const client = new ArcanaClient({ apiUrl: "http://127.0.0.1", transport });
  await client.connect("workspace-a");
  const pending = client.subscribe("items", { scope: "a" });
  client.disconnect();
  complete({ params_hash: "a", version: 1, refs: [], tables: { items: { a: { value: "private-cache" } } }, seance_id: "fixture-session" });
  await expect(pending).rejects.toThrow(/disconnected/);
  expect(client.getRow("items", "a")).toBeNull();
});

test.each([[[], ["a", "b", "c"]], [["z"], ["a", "b", "z"]]])("server ref additions retain target order from %j", async (initial, expected) => {
  const transport = fixtureTransport();
  let deliver!: (message: import("./types.js").TransportMessage) => void;
  transport.subscribe = (_channel, handler) => { deliver = handler; return () => {}; };
  const ref = (id: string) => ({ table: "items", id, fields: [] });
  transport.subscribeView = async () => ({ params_hash: "a", version: 1, refs: initial.map(ref), tables: {}, seance_id: "fixture-session" });
  const client = new ArcanaClient({ apiUrl: "http://127.0.0.1", transport });
  await client.connect("workspace-a");
  try {
    const handle = await client.subscribe("items", { scope: "a" });
    const additions = expected.flatMap((id, index) => initial.includes(id) ? [] : [{ op: "add", path: `/${index}`, value: ref(id) }]);
    deliver({ type: "view_diff", data: { params_hash: "a", version: 2, refs_patch: additions.reverse(), tables: {} } });
    expect(handle.data.map(value => value.id)).toEqual(expected);
    expect(handle.version).toBe(2);
  } finally { client.disconnect(); }
});

test.each(["snapshot", "catch_up"])("native sync %s applies hash-bound refs, tables and versions", async mode => {
  const ref = (id: string) => ({ table: "items", id, fields: ["value"] });
  const transport = { connect: async () => {}, disconnect() {}, subscribe: () => () => {} };
  const fetchFn = vi.fn(async (url: string | URL | Request) => new Response(JSON.stringify({ ok: true, data:
    String(url).endsWith("/sync") ? { views: [{ view: "items", params_hash: "a", mode,
      ...(mode === "snapshot" ? { version: 3, refs: [ref("new")], tables: { items: { new: { value: "new" } } } } : { patches: [
        { version: 2, refs_patch: [{ op: "replace", path: "/0", value: ref("middle") }], tables: { items: { middle: { value: "middle" } } } },
        { version: 3, refs_patch: [{ op: "replace", path: "/0", value: ref("new") }], tables: { items: { new: { value: "new" } } } },
      ] }),
    }] } : { params_hash: "a", version: 1, refs: [ref("old")], tables: { items: { old: { value: "old" } } }, seance_id: "fixture-session" },
  }))) as unknown as typeof fetch;
  const onUpdate = vi.fn();
  const client = new ArcanaClient({ apiUrl: "http://127.0.0.1", transport, fetchFn });
  await client.connect("workspace-a");
  try {
    const handle = await client.subscribe("items", { scope: "a" }, { onUpdate });
    await client.handleReconnect();
    expect(handle.data.map(value => value.id)).toEqual(["new"]);
    expect(handle.version).toBe(3);
    expect(client.getRow("items", "new")).toEqual({ value: "new" });
    expect(onUpdate).toHaveBeenCalled();
  } finally { client.disconnect(); }
});

test("WebSocket reconnect restores physical subscriptions and existing handle callbacks", async () => {
  let seanceId = "fixture-session";
  const transport = { ...fixtureTransport(), get seanceId() { return seanceId; } };
  const subscribe = vi.fn(async () => ({
    params_hash: "a", version: subscribe.mock.calls.length === 1 ? 10 : 1,
    refs: [{ table: "items", id: subscribe.mock.calls.length === 1 ? "old" : "restored", fields: [] }],
    tables: {}, seance_id: transport.seanceId!,
  }));
  transport.subscribeView = subscribe;
  const client = new ArcanaClient({ apiUrl: "http://127.0.0.1", transport, fetchFn: vi.fn(async () => new Response(JSON.stringify({ ok: false, error: "no active subscription" }))) as unknown as typeof fetch });
  const onUpdate = vi.fn();
  await client.connect("workspace-a");
  try {
    const first = await client.subscribe("items", { scope: "a" }, { onUpdate });
    seanceId = "reconnected-session";
    await client.handleReconnect();
    expect(subscribe).toHaveBeenCalledTimes(2);
    expect(first.data[0]?.id).toBe("restored");
    expect(first.version).toBe(1);
    expect(onUpdate).toHaveBeenCalled();
    const second = await client.subscribe("items", { scope: "a" });
    expect(subscribe).toHaveBeenCalledTimes(2);
    second.destroy();
    expect(transport.unsubscribeView).not.toHaveBeenCalled();
    first.destroy();
    expect(transport.unsubscribeView).toHaveBeenCalledTimes(1);
  } finally { client.disconnect(); }
});

test("disconnect during automatic restoration stays disconnected and releases ownership", async () => {
  const transport = fixtureTransport();
  let reconnect!: () => void;
  let finish!: (reply: SubscribeResponse) => void;
  transport.setReconnectHandler = handler => { reconnect = handler; };
  const client = new ArcanaClient({ apiUrl: "http://127.0.0.1", transport });
  await client.connect("workspace-a");
  await client.subscribe("items", { scope: "a" });
  transport.subscribeView = () => new Promise(resolve => { finish = resolve; });
  const restore = client.handleReconnect.bind(client);
  let pending!: Promise<void>;
  vi.spyOn(client, "handleReconnect").mockImplementation(() => { pending = restore(); return pending; });
  reconnect();
  client.disconnect();
  finish({ params_hash: "a", version: 1, refs: [], tables: { items: { a: { value: "late" } } }, seance_id: "fixture-session" });
  await pending.catch(() => {});
  await Promise.resolve();
  expect(client.status).toBe("disconnected");
  expect(client.getRow("items", "a")).toBeNull();
});

test("failed physical subscriptions fan out and permit a fresh retry", async () => {
  const transport = fixtureTransport();
  let reject!: (error: Error) => void;
  const subscribe = vi.fn(transport.subscribeView);
  subscribe.mockImplementationOnce(() => new Promise((_resolve, fail) => { reject = fail; }));
  transport.subscribeView = subscribe;
  const client = new ArcanaClient({ apiUrl: "http://127.0.0.1", transport });
  await client.connect("workspace-a");
  try {
    const first = client.subscribe("items", { scope: "a" });
    const second = client.subscribe("items", { scope: "a" });
    const failures = Promise.allSettled([first, second]);
    expect(subscribe).toHaveBeenCalledTimes(1);
    reject(new Error("owned transport failure"));
    expect((await failures).map(result => result.status)).toEqual(["rejected", "rejected"]);
    const third = await client.subscribe("items", { scope: "a" });
    const fourth = await client.subscribe("items", { scope: "a" });
    expect(subscribe).toHaveBeenCalledTimes(2);
    third.destroy();
    expect(transport.unsubscribeView).not.toHaveBeenCalled();
    expect(fourth.data[0]?.id).toBe("a");
    fourth.destroy();
    expect(transport.unsubscribeView).toHaveBeenCalledTimes(1);
    third.destroy();
    expect(transport.unsubscribeView).toHaveBeenCalledTimes(1);
  } finally { client.disconnect(); }
});

test("HTTP resubscribe waits for the previous physical unsubscribe", async () => {
  let active = false;
  let release!: () => void;
  const transport = { connect: async () => {}, disconnect() {}, subscribe: () => () => {} };
  const fetchFn = vi.fn(async (url: string | URL | Request) => {
    if (String(url).endsWith("/unsubscribe")) {
      await new Promise<void>(resolve => { release = resolve; });
      active = false;
      return new Response(JSON.stringify({ ok: true }));
    }
    if (active) return new Response(JSON.stringify({ ok: false, error: "already subscribed with same params" }));
    active = true;
    return new Response(JSON.stringify({ ok: true, data: {
      params_hash: "a", version: 1, refs: [], tables: {}, seance_id: "fixture-session",
    } }));
  }) as unknown as typeof fetch;
  const client = new ArcanaClient({ apiUrl: "http://127.0.0.1", transport, fetchFn });
  await client.connect("workspace-a");
  try {
    const first = await client.subscribe("items", { scope: "a" });
    first.destroy();
    const second = client.subscribe("items", { scope: "a" });
    const settled = second.then(handle => ({ handle }), error => ({ error }));
    expect(fetchFn).toHaveBeenCalledTimes(2);
    release();
    const result = await settled;
    expect(result).not.toHaveProperty("error");
    expect(active).toBe(true);
    expect(fetchFn).toHaveBeenCalledTimes(3);
  } finally { client.disconnect(); }
});
