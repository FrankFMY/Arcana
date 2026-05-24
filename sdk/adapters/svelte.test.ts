import { describe, expect, it, vi } from "vitest";
import type { ArcanaClient, ViewHandle } from "../client";
import type { Ref } from "../types";
import { createSubscription, type SubscriptionSnapshot } from "./svelte";

function flushPromises(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

function mockViewHandle(data: Ref[] = [], version = 1): ViewHandle {
  return {
    data,
    version,
    loading: false,
    destroy: vi.fn(),
  };
}

function mockClient(overrides: Partial<ArcanaClient> = {}): ArcanaClient {
  return {
    get isOffline() {
      return false;
    },
    subscribe: vi.fn(),
    ...overrides,
  } as unknown as ArcanaClient;
}

describe("createSubscription", () => {
  it("creates a Svelte store-compatible subscription without runtime runes", async () => {
    const refs: Ref[] = [{ table: "users", id: "u1", fields: ["name"] }];
    const handle = mockViewHandle(refs, 3);
    const client = mockClient({
      subscribe: vi.fn().mockResolvedValue(handle),
    });
    const snapshots: SubscriptionSnapshot[] = [];

    const subscription = createSubscription(client, "users_list", { limit: 20 });
    const unsubscribe = subscription.subscribe((snapshot) => {
      snapshots.push({ ...snapshot, data: [...snapshot.data] });
    });

    expect(subscription.loading).toBe(true);
    expect(snapshots[0]).toMatchObject({ data: [], version: 0, loading: true });

    await flushPromises();

    expect(client.subscribe).toHaveBeenCalledWith("users_list", { limit: 20 }, expect.any(Object));
    expect(subscription.data).toEqual(refs);
    expect(subscription.version).toBe(3);
    expect(subscription.loading).toBe(false);
    expect(subscription.error).toBeNull();
    expect(snapshots.at(-1)).toMatchObject({ data: refs, version: 3, loading: false });

    unsubscribe();
    subscription.destroy();
    expect(handle.destroy).toHaveBeenCalled();
  });

  it("publishes updates through the store subscription", async () => {
    let onUpdate: (() => void) | undefined;
    const handle = mockViewHandle([{ table: "tasks", id: "t1", fields: ["title"] }], 1);
    const client = mockClient({
      subscribe: vi.fn((_graphKey, _params, opts) => {
        onUpdate = opts?.onUpdate;
        return Promise.resolve(handle);
      }),
    });
    const snapshots: SubscriptionSnapshot[] = [];

    const subscription = createSubscription(client, "tasks_list", {});
    subscription.subscribe((snapshot) => {
      snapshots.push({ ...snapshot, data: [...snapshot.data] });
    });
    await flushPromises();

    (handle as { data: Ref[]; version: number }).data = [
      { table: "tasks", id: "t1", fields: ["title"] },
      { table: "tasks", id: "t2", fields: ["title"] },
    ];
    (handle as { data: Ref[]; version: number }).version = 2;
    onUpdate?.();

    expect(subscription.data).toHaveLength(2);
    expect(subscription.version).toBe(2);
    expect(snapshots.at(-1)?.data).toHaveLength(2);
  });

  it("exposes subscribe errors and clears loading", async () => {
    const error = new Error("subscribe failed");
    const client = mockClient({
      subscribe: vi.fn().mockRejectedValue(error),
    });

    const subscription = createSubscription(client, "bad_view", {});
    await flushPromises();

    expect(subscription.loading).toBe(false);
    expect(subscription.error).toBe(error);
  });
});
