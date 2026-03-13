import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, act, waitFor } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import type { ArcanaClient, ViewHandle, ConnectionStatus } from "../client";
import type { Ref } from "../types";
import {
  ArcanaProvider,
  useView,
  useMutation,
  useStatus,
} from "./react";

// --- Mock client factory ---

type StatusCallback = (status: ConnectionStatus) => void;

function createMockClient(overrides: Partial<ArcanaClient> = {}) {
  const statusListeners = new Set<StatusCallback>();
  let currentStatus: ConnectionStatus = "disconnected";

  const client = {
    subscribe: vi.fn(),
    mutate: vi.fn(),
    getRow: vi.fn(),
    get status() {
      return currentStatus;
    },
    onStatusChange(cb: StatusCallback) {
      statusListeners.add(cb);
      return () => statusListeners.delete(cb);
    },
    // Test helper: push a status change
    _setStatus(s: ConnectionStatus) {
      currentStatus = s;
      for (const cb of statusListeners) cb(s);
    },
    ...overrides,
  } as unknown as ArcanaClient & { _setStatus: (s: ConnectionStatus) => void };

  return client;
}

function createWrapper(client: ArcanaClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return createElement(ArcanaProvider, { client, children });
  };
}

// --- Mock ViewHandle ---

function mockViewHandle(data: Ref[] = [], version = 1): ViewHandle {
  return {
    data,
    version,
    loading: false,
    destroy: vi.fn(),
  };
}

// =====================================================================
// useView
// =====================================================================

describe("useView", () => {
  it("starts with loading=true, then populates data from subscribe", async () => {
    const refs: Ref[] = [{ table: "tasks", id: "t1", fields: ["id", "title"] }];
    const handle = mockViewHandle(refs, 1);

    const client = createMockClient({
      subscribe: vi.fn().mockResolvedValue(handle),
    });

    const { result } = renderHook(() => useView("tasks_list"), {
      wrapper: createWrapper(client),
    });

    // Initially loading
    expect(result.current.loading).toBe(true);
    expect(result.current.data).toEqual([]);

    // After subscribe resolves
    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });

    expect(result.current.data).toEqual(refs);
    expect(result.current.version).toBe(1);
    expect(client.subscribe).toHaveBeenCalledWith("tasks_list", {}, expect.any(Object));
  });

  it("re-subscribes when params change, destroying old handle", async () => {
    const handle1 = mockViewHandle(
      [{ table: "t", id: "1", fields: ["id"] }],
      1
    );
    const handle2 = mockViewHandle(
      [{ table: "t", id: "2", fields: ["id"] }],
      2
    );

    const subscribeFn = vi
      .fn()
      .mockResolvedValueOnce(handle1)
      .mockResolvedValueOnce(handle2);

    const client = createMockClient({ subscribe: subscribeFn });

    const { result, rerender } = renderHook(
      ({ params }) => useView("items", params),
      {
        wrapper: createWrapper(client),
        initialProps: { params: { page: 1 } as Record<string, unknown> },
      }
    );

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.data[0].id).toBe("1");

    // Change params
    rerender({ params: { page: 2 } });

    await waitFor(() => {
      expect(result.current.data[0]?.id).toBe("2");
    });

    // Old handle was destroyed
    expect(handle1.destroy).toHaveBeenCalled();
    expect(subscribeFn).toHaveBeenCalledTimes(2);
  });

  it("calls destroy on unmount", async () => {
    const handle = mockViewHandle([], 1);
    const client = createMockClient({
      subscribe: vi.fn().mockResolvedValue(handle),
    });

    const { unmount } = renderHook(() => useView("v"), {
      wrapper: createWrapper(client),
    });

    await waitFor(() => expect(handle.destroy).not.toHaveBeenCalled());
    unmount();
    expect(handle.destroy).toHaveBeenCalled();
  });
});

// =====================================================================
// useMutation
// =====================================================================

describe("useMutation", () => {
  it("sets loading during mutation and returns result", async () => {
    const client = createMockClient({
      mutate: vi.fn().mockResolvedValue({ id: "new-1" }),
    });

    const { result } = renderHook(() => useMutation<{ id: string }>("create_task"), {
      wrapper: createWrapper(client),
    });

    expect(result.current.loading).toBe(false);

    let mutationResult: { id: string } | undefined;
    await act(async () => {
      mutationResult = await result.current.mutate({ title: "Test" });
    });

    expect(mutationResult).toEqual({ id: "new-1" });
    expect(result.current.loading).toBe(false);
    expect(client.mutate).toHaveBeenCalledWith("create_task", { title: "Test" });
  });

  it("resets loading on error", async () => {
    const client = createMockClient({
      mutate: vi.fn().mockRejectedValue(new Error("fail")),
    });

    const { result } = renderHook(() => useMutation("bad_action"), {
      wrapper: createWrapper(client),
    });

    await act(async () => {
      try {
        await result.current.mutate({});
      } catch {
        // expected
      }
    });

    expect(result.current.loading).toBe(false);
  });
});

// =====================================================================
// useStatus
// =====================================================================

describe("useStatus", () => {
  it("reflects initial status and reacts to changes", async () => {
    const client = createMockClient();

    const { result } = renderHook(() => useStatus(), {
      wrapper: createWrapper(client as unknown as ArcanaClient),
    });

    expect(result.current).toBe("disconnected");

    act(() => {
      (client as any)._setStatus("connecting");
    });
    expect(result.current).toBe("connecting");

    act(() => {
      (client as any)._setStatus("connected");
    });
    expect(result.current).toBe("connected");

    act(() => {
      (client as any)._setStatus("disconnected");
    });
    expect(result.current).toBe("disconnected");
  });
});
