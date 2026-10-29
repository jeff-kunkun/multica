// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { QueryClient, QueryObserver } from "@tanstack/react-query";
import type { StorageAdapter } from "./types/storage";
import {
  clearPersistedQueryCache,
  createPersistedQueryCache,
  QUERY_CACHE_MAX_CHARS,
  QUERY_CACHE_SCHEMA_VERSION,
  QUERY_CACHE_WRITE_INTERVAL_MS,
  queryCacheStoragePrefix,
} from "./query-persistence";

function memoryStorage(): StorageAdapter & { data: Record<string, string> } {
  const data: Record<string, string> = {};
  return {
    data,
    getItem: (key) => data[key] ?? null,
    setItem: (key, value) => { data[key] = value; },
    removeItem: (key) => { delete data[key]; },
    keys: () => Object.keys(data),
  };
}

describe("persisted query cache", () => {
  it("hydrates only the account namespace and writes a schema version", async () => {
    vi.useFakeTimers();
    const storage = memoryStorage();
    const first = new QueryClient();
    const stop = createPersistedQueryCache(first, storage, "user-a");
    first.setQueryData(["projects", "workspace-a"], [{ id: "p1" }]);
    first.setQueryData(["messages", "workspace-a"], [{ id: "secret" }]);
    vi.advanceTimersByTime(QUERY_CACHE_WRITE_INTERVAL_MS);

    const key = `${queryCacheStoragePrefix()}user-a`;
    const envelope = JSON.parse(storage.data[key]!);
    expect(envelope.schemaVersion).toBe(QUERY_CACHE_SCHEMA_VERSION);
    expect(envelope.userId).toBe("user-a");
    expect(envelope.state.queries.map((q: { queryKey: unknown[] }) => q.queryKey)).toEqual([
      ["projects", "workspace-a"],
    ]);

    const second = new QueryClient();
    const invalidate = vi.spyOn(second, "invalidateQueries");
    createPersistedQueryCache(second, storage, "user-a");
    expect(second.getQueryData(["projects", "workspace-a"])).toEqual([{ id: "p1" }]);
    expect(second.getQueryData(["messages", "workspace-a"])).toBeUndefined();
    expect(invalidate).toHaveBeenCalledWith(expect.objectContaining({
      refetchType: "active",
      predicate: expect.any(Function),
    }));
    stop();
    vi.useRealTimers();
  });

  it("never persists attachment bytes and drops them from an older snapshot", async () => {
    vi.useFakeTimers();
    const storage = memoryStorage();
    const writer = new QueryClient();
    const stop = createPersistedQueryCache(writer, storage, "user-a");
    writer.setQueryData(["attachment-inline-blob", "att-1"], new Blob(["x"]));
    writer.setQueryData(["inline-bytes", "att-2"], new Blob(["y"]));
    writer.setQueryData(["projects", "workspace-a"], [{ id: "p1" }]);
    await vi.advanceTimersByTimeAsync(QUERY_CACHE_WRITE_INTERVAL_MS);
    stop();

    const key = `${queryCacheStoragePrefix()}user-a`;
    const envelope = JSON.parse(storage.data[key]!);
    expect(envelope.state.queries.map((q: { queryKey: unknown[] }) => q.queryKey)).toEqual([
      ["projects", "workspace-a"],
    ]);

    // A snapshot written before the fix holds the Blob as `{}`.
    envelope.state.queries.push({
      ...envelope.state.queries[0],
      queryKey: ["attachment-inline-blob", "att-1"],
      queryHash: JSON.stringify(["attachment-inline-blob", "att-1"]),
      state: { ...envelope.state.queries[0].state, data: {} },
    });
    storage.setItem(key, JSON.stringify(envelope));
    const reader = new QueryClient();
    createPersistedQueryCache(reader, storage, "user-a");
    expect(reader.getQueryData(["attachment-inline-blob", "att-1"])).toBeUndefined();
    expect(reader.getQueryData(["projects", "workspace-a"])).toEqual([{ id: "p1" }]);
    vi.useRealTimers();
  });

  it("removes one account or all persisted accounts", () => {
    const storage = memoryStorage();
    storage.setItem(`${queryCacheStoragePrefix()}a`, "a");
    storage.setItem(`${queryCacheStoragePrefix()}b`, "b");
    clearPersistedQueryCache(storage, "a");
    expect(storage.keys?.()).toEqual([`${queryCacheStoragePrefix()}b`]);
    clearPersistedQueryCache(storage);
    expect(storage.keys?.()).toEqual([]);
  });

  it("refetches a restored query when its page mounts", async () => {
    vi.useFakeTimers();
    const storage = memoryStorage();
    const writer = new QueryClient();
    const stopWriter = createPersistedQueryCache(writer, storage, "user-a");
    writer.setQueryData(["projects", "workspace-a"], [{ id: "old" }]);
    await vi.advanceTimersByTimeAsync(QUERY_CACHE_WRITE_INTERVAL_MS);
    stopWriter();

    const reader = new QueryClient();
    const stopReader = createPersistedQueryCache(reader, storage, "user-a");
    const queryFn = vi.fn().mockResolvedValue([{ id: "new" }]);
    const observer = new QueryObserver(reader, {
      queryKey: ["projects", "workspace-a"],
      queryFn,
    });
    const unsubscribe = observer.subscribe(() => undefined);
    await vi.waitFor(() => expect(queryFn).toHaveBeenCalledTimes(1));
    await vi.waitFor(() =>
      expect(reader.getQueryData(["projects", "workspace-a"])).toEqual([{ id: "new" }]),
    );
    unsubscribe();
    stopReader();
    vi.useRealTimers();
  });

  it("skips data JSON cannot round-trip, so a restored Map never comes back as {}", async () => {
    vi.useFakeTimers();
    const storage = memoryStorage();
    const writer = new QueryClient();
    const stop = createPersistedQueryCache(writer, storage, "user-a");
    writer.setQueryData(["issues", "ws", "children-by-parents", ["p1"]], new Map([["p1", [{ id: "c1" }]]]));
    writer.setQueryData(["inbox", "ws", "ids"], new Set(["a"]));
    writer.setQueryData(["issues", "ws", "nested"], { at: new Date(0) });
    writer.setQueryData(["projects", "ws"], [{ id: "p1", meta: { tags: ["x"], none: null } }]);
    await vi.advanceTimersByTimeAsync(QUERY_CACHE_WRITE_INTERVAL_MS);
    stop();

    const envelope = JSON.parse(storage.data[`${queryCacheStoragePrefix()}user-a`]!);
    expect(envelope.state.queries.map((q: { queryKey: unknown[] }) => q.queryKey)).toEqual([["projects", "ws"]]);
    vi.useRealTimers();
  });

  it("drops a snapshot written by an older schema", () => {
    const storage = memoryStorage();
    const key = `${queryCacheStoragePrefix()}user-a`;
    storage.setItem(key, JSON.stringify({
      schemaVersion: QUERY_CACHE_SCHEMA_VERSION - 1,
      userId: "user-a",
      state: {
        mutations: [],
        queries: [{
          queryKey: ["issues", "ws", "children-by-parents", ["p1"]],
          queryHash: JSON.stringify(["issues", "ws", "children-by-parents", ["p1"]]),
          state: { data: {}, status: "success", dataUpdatedAt: 1 },
        }],
      },
    }));
    const reader = new QueryClient();
    createPersistedQueryCache(reader, storage, "user-a");
    expect(reader.getQueryData(["issues", "ws", "children-by-parents", ["p1"]])).toBeUndefined();
    expect(storage.data[key]).toBeUndefined();
  });

  it("keeps the most recently updated queries within the size budget", async () => {
    vi.useFakeTimers();
    const storage = memoryStorage();
    const writer = new QueryClient();
    const stop = createPersistedQueryCache(writer, storage, "user-a");
    const big = "x".repeat(Math.floor(QUERY_CACHE_MAX_CHARS / 2));
    writer.setQueryData(["projects", "old"], big, { updatedAt: 1 });
    writer.setQueryData(["projects", "mid"], big, { updatedAt: 2 });
    writer.setQueryData(["projects", "new"], big, { updatedAt: 3 });
    await vi.advanceTimersByTimeAsync(QUERY_CACHE_WRITE_INTERVAL_MS);
    stop();

    const raw = storage.data[`${queryCacheStoragePrefix()}user-a`]!;
    expect(raw.length).toBeLessThanOrEqual(QUERY_CACHE_MAX_CHARS);
    const reader = new QueryClient();
    createPersistedQueryCache(reader, storage, "user-a");
    expect(reader.getQueryData(["projects", "new"])).toBe(big);
    expect(reader.getQueryData(["projects", "old"])).toBeUndefined();
    vi.useRealTimers();
  });

  it("frees the old snapshot when the write fails", async () => {
    vi.useFakeTimers();
    const storage = memoryStorage();
    const key = `${queryCacheStoragePrefix()}user-a`;
    storage.data[key] = "stale";
    storage.setItem = () => { throw new Error("QuotaExceededError"); };
    const writer = new QueryClient();
    const stop = createPersistedQueryCache(writer, storage, "user-a");
    writer.setQueryData(["projects", "ws"], [{ id: "p1" }]);
    await vi.advanceTimersByTimeAsync(QUERY_CACHE_WRITE_INTERVAL_MS);
    stop();
    expect(storage.data[key]).toBeUndefined();
    vi.useRealTimers();
  });

  it("writes at most once per interval, and not for data that did not change", async () => {
    vi.useFakeTimers();
    const storage = memoryStorage();
    const writes = vi.fn(storage.setItem);
    storage.setItem = writes;
    const writer = new QueryClient();
    const stop = createPersistedQueryCache(writer, storage, "user-a");

    for (let i = 0; i < 20; i++) writer.setQueryData(["projects", "ws"], [{ id: `p${i}` }]);
    await vi.advanceTimersByTimeAsync(QUERY_CACHE_WRITE_INTERVAL_MS - 1);
    expect(writes).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(writes).toHaveBeenCalledTimes(1);

    // A poll returning equal data, and volatile or excluded families, write nothing.
    writer.setQueryData(["projects", "ws"], [{ id: "p19" }]);
    writer.setQueryData(["messages", "ws"], [{ id: "m" }]);
    await writer.invalidateQueries({ queryKey: ["projects", "ws"] });
    await vi.advanceTimersByTimeAsync(QUERY_CACHE_WRITE_INTERVAL_MS * 2);
    expect(writes).toHaveBeenCalledTimes(1);

    writer.setQueryData(["projects", "ws"], [{ id: "p20" }]);
    await vi.advanceTimersByTimeAsync(QUERY_CACHE_WRITE_INTERVAL_MS);
    expect(writes).toHaveBeenCalledTimes(2);
    stop();
    vi.useRealTimers();
  });

  it("drops a restored query from the snapshot once garbage collection removes it", async () => {
    vi.useFakeTimers();
    const storage = memoryStorage();
    const key = `${queryCacheStoragePrefix()}user-a`;
    const writer = new QueryClient();
    const stopWriter = createPersistedQueryCache(writer, storage, "user-a");
    writer.setQueryData(["projects", "ws"], [{ id: "p1" }]);
    await vi.advanceTimersByTimeAsync(QUERY_CACHE_WRITE_INTERVAL_MS);
    stopWriter();

    // Restored before the reader subscribes, and never observed afterwards.
    const reader = new QueryClient({ defaultOptions: { queries: { gcTime: 1_000 } } });
    const stopReader = createPersistedQueryCache(reader, storage, "user-a");
    expect(reader.getQueryData(["projects", "ws"])).toEqual([{ id: "p1" }]);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(reader.getQueryCache().find({ queryKey: ["projects", "ws"] })).toBeUndefined();
    await vi.advanceTimersByTimeAsync(QUERY_CACHE_WRITE_INTERVAL_MS);
    stopReader();

    expect(JSON.parse(storage.data[key]!).state.queries).toEqual([]);
    vi.useRealTimers();
  });

  it("drops a query from the snapshot when its background refetch fails", async () => {
    vi.useFakeTimers();
    const storage = memoryStorage();
    const writer = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const stop = createPersistedQueryCache(writer, storage, "user-a");
    await writer.fetchQuery({ queryKey: ["projects", "ws"], queryFn: () => Promise.resolve([{ id: "p1" }]) });
    await vi.advanceTimersByTimeAsync(QUERY_CACHE_WRITE_INTERVAL_MS);
    const key = `${queryCacheStoragePrefix()}user-a`;
    expect(JSON.parse(storage.data[key]!).state.queries).toHaveLength(1);

    await writer
      .fetchQuery({ queryKey: ["projects", "ws"], queryFn: () => Promise.reject(new Error("offline")) })
      .catch(() => undefined);
    expect(writer.getQueryState(["projects", "ws"])).toMatchObject({ status: "error", data: [{ id: "p1" }] });
    await vi.advanceTimersByTimeAsync(QUERY_CACHE_WRITE_INTERVAL_MS);
    stop();

    // The next start must not bring the old success back.
    const reader = new QueryClient();
    createPersistedQueryCache(reader, storage, "user-a");
    expect(reader.getQueryData(["projects", "ws"])).toBeUndefined();
    vi.useRealTimers();
  });

  it("writes a pending snapshot at once when the page hides", async () => {
    vi.useFakeTimers();
    const storage = memoryStorage();
    const writer = new QueryClient();
    const stop = createPersistedQueryCache(writer, storage, "user-a");
    writer.setQueryData(["projects", "ws"], [{ id: "p1" }]);
    window.dispatchEvent(new Event("pagehide"));
    const envelope = JSON.parse(storage.data[`${queryCacheStoragePrefix()}user-a`]!);
    expect(envelope.state.queries.map((q: { queryKey: unknown[] }) => q.queryKey)).toEqual([["projects", "ws"]]);

    // After stop, page events no longer write.
    stop();
    delete storage.data[`${queryCacheStoragePrefix()}user-a`];
    writer.setQueryData(["projects", "ws"], [{ id: "p2" }]);
    window.dispatchEvent(new Event("pagehide"));
    expect(storage.data[`${queryCacheStoragePrefix()}user-a`]).toBeUndefined();
    vi.useRealTimers();
  });
});
