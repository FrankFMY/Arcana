import { expect, test, vi } from "vitest";
import { createApp, defineComponent, h, nextTick, ref } from "vue";
import { ArcanaClient } from "../client.js";
import type { ViewHandle } from "../client.js";
import type { SubscribeResponse, TransportMessage, ViewTransportClient } from "../types.js";
import { provideArcana, useView } from "./vue.js";

test.each([[false, false], [true, false], [true, true]])("late Vue generations preserve current ownership, return=%s old-first=%s", async (returnToFirst, oldFirst) => {
  const pending: { page: number; resolve: (reply: SubscribeResponse) => void }[] = [];
  const handlers = new Set<(message: TransportMessage) => void>();
  const unsubscribeView = vi.fn();
  const transport: ViewTransportClient = {
    seanceId: "fixture-session", connect: async () => {}, disconnect() {},
    subscribe: (_channel, handler) => { handlers.add(handler); return () => { handlers.delete(handler); }; },
    unsubscribeView,
    subscribeView: (_view, params) => new Promise(resolve => pending.push({ page: Number(params.page), resolve })),
  };
  const client = new ArcanaClient({ apiUrl: "http://127.0.0.1", transport });
  await client.connect("fixture-workspace");
  const requests: Promise<ViewHandle>[] = [];
  const realSubscribe = client.subscribe.bind(client);
  vi.spyOn(client, "subscribe").mockImplementation((...args) => {
    const promise = realSubscribe(...args); requests.push(promise); return promise;
  });
  const page = ref(1);
  const Child = defineComponent({ setup() {
    const view = useView("items", () => ({ page: page.value }));
    return () => h("p", `${view.data.value[0]?.id ?? "loading"}:${view.version.value}`);
  } });
  const root = document.createElement("div"); document.body.append(root);
  const app = createApp({ setup() { provideArcana(client); return () => h(Child); } });
  const reply = (id: number, version: number): SubscribeResponse => ({ params_hash: String(id), version, refs: [{ table: "items", id: String(id), fields: [] }], tables: {}, seance_id: "fixture-session" });
  const target = returnToFirst ? 1 : 2;
  const current = returnToFirst ? 2 : 1;
  const targetPhysical = returnToFirst ? 0 : 1;
  const stalePhysical = 1 - targetPhysical;
  try {
    app.mount(root);
    expect(pending.map(p => p.page)).toEqual([1]);
    page.value = 2; await nextTick();
    expect(pending.map(p => p.page)).toEqual([1, 2]);
    if (returnToFirst) { page.value = 1; await nextTick(); expect(pending.map(p => p.page)).toEqual([1, 2]); }
    // Logical owners still have independent generations; the server now sees
    // only the two distinct physical identities it actually permits.
    if (oldFirst) {
      pending[stalePhysical].resolve(reply(pending[stalePhysical].page, stalePhysical + 1));
      await requests[stalePhysical]; await nextTick();
    }
    pending[targetPhysical].resolve(reply(target, targetPhysical + 1)); await requests[current]; await nextTick();
    expect(root.textContent).toBe(`${target}:${targetPhysical + 1}`);
    if (!oldFirst) {
      pending[stalePhysical].resolve(reply(pending[stalePhysical].page, stalePhysical + 1));
      await requests[stalePhysical]; await nextTick();
    }
    await Promise.all(requests); await nextTick();
    expect(root.textContent).toBe(`${target}:${targetPhysical + 1}`);
    if (returnToFirst) expect(unsubscribeView).not.toHaveBeenCalledWith("1");
    else expect(unsubscribeView).toHaveBeenCalledWith("1");
    for (const handler of handlers) handler({ type: "view_diff", data: { params_hash: String(target), version: 4, refs_patch: [{ op: "replace", path: "/0", value: { table: "items", id: "updated", fields: [] } }], tables: {} } });
    await nextTick();
    expect(root.textContent).toBe("updated:4");
  } finally { app.unmount(); root.remove(); client.disconnect(); }
  expect(unsubscribeView).toHaveBeenCalledWith(String(target));
});

test("Vue return to a pending identity shares the server subscription", async () => {
  const active = new Set<number>();
  const pending: { page: number; resolve: (response: Response) => void }[] = [];
  const unsubscribe = vi.fn();
  const transport = { connect: async () => {}, disconnect() {}, subscribe: () => () => {} };
  const fetchFn = vi.fn((_url: string | URL | Request, init?: RequestInit) => {
    const body = JSON.parse(String(init?.body));
    if (String(_url).endsWith("/unsubscribe")) {
      active.delete(Number(body.params_hash)); unsubscribe(body.params_hash);
      return Promise.resolve(new Response(JSON.stringify({ ok: true })));
    }
    const page = Number(body.params.page);
    if (active.has(page)) return Promise.resolve(new Response(JSON.stringify({ ok: false, error: "already subscribed with same params" })));
    active.add(page);
    return new Promise<Response>(resolve => pending.push({ page, resolve }));
  }) as unknown as typeof fetch;
  const client = new ArcanaClient({ apiUrl: "http://127.0.0.1", transport, fetchFn });
  await client.connect("fixture-workspace");
  const page = ref(1);
  const Child = defineComponent({ setup() {
    const view = useView("items", () => ({ page: page.value }));
    return () => h("p", `${view.data.value[0]?.id ?? "empty"}:loading=${view.loading.value}`);
  } });
  const root = document.createElement("div"); document.body.append(root);
  const app = createApp({ setup() { provideArcana(client); return () => h(Child); } });
  try {
    app.mount(root);
    page.value = 2; await nextTick();
    page.value = 1; await nextTick();
    for (const request of pending) request.resolve(new Response(JSON.stringify({ ok: true, data: {
      params_hash: String(request.page), version: 1,
      refs: [{ table: "items", id: String(request.page), fields: [] }], tables: {}, seance_id: "fixture-session",
    } })));
    await vi.waitFor(() => expect(root.textContent).toBe("1:loading=false"));
    expect(pending.map(request => request.page)).toEqual([1, 2]);
    expect(unsubscribe).not.toHaveBeenCalledWith("1");
    expect(active.has(1)).toBe(true);
    const second = await client.subscribe("items", { page: 1 });
    expect(pending).toHaveLength(2);
    second.destroy();
    expect(active.has(1)).toBe(true);
  } finally { app.unmount(); root.remove(); client.disconnect(); }
  expect(unsubscribe).toHaveBeenCalledWith("1");
});
