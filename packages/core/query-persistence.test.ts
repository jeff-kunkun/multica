import { describe, expect, it, vi } from "vitest";
import { dehydrate, QueryClient, QueryObserver } from "@tanstack/react-query";
import type { StorageAdapter } from "./types/storage";
import {
  clearPersistedQueryCache,
  createPersistedQueryCache,
  QUERY_CACHE_SCHEMA_VERSION,
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
    vi.advanceTimersByTime(60);

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
    await vi.advanceTimersByTimeAsync(60);
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

  it("never persists attachment queries or binary data", () => {
    vi.useFakeTimers();
    const storage = memoryStorage();
    const client = new QueryClient();
    const stop = createPersistedQueryCache(client, storage, "user-a");
    client.setQueryData(["projects", "workspace-a"], [{ id: "p1" }]);
    client.setQueryData(["attachment-inline-blob", "att-1"], new Blob(["x"]));
    client.setQueryData(["attachment-inline-resign", "att-1"], { download_url: "https://s/x" });
    client.setQueryData(["files", "att-2"], { bytes: new Uint8Array([1]) });
    vi.advanceTimersByTime(60);

    const envelope = JSON.parse(storage.data[`${queryCacheStoragePrefix()}user-a`]!);
    expect(envelope.state.queries.map((q: { queryKey: unknown[] }) => q.queryKey)).toEqual([
      ["projects", "workspace-a"],
    ]);
    stop();
    vi.useRealTimers();
  });

  it("drops entries an older build should not have persisted", () => {
    const storage = memoryStorage();
    const writer = new QueryClient();
    writer.setQueryData(["projects", "workspace-a"], [{ id: "p1" }]);
    writer.setQueryData(["attachment-pdf-blob", "att-1"], {});
    const state = dehydrate(writer, { shouldDehydrateQuery: () => true });
    storage.setItem(
      `${queryCacheStoragePrefix()}user-a`,
      JSON.stringify({ schemaVersion: QUERY_CACHE_SCHEMA_VERSION, userId: "user-a", state }),
    );

    const reader = new QueryClient();
    const stop = createPersistedQueryCache(reader, storage, "user-a");
    expect(reader.getQueryData(["projects", "workspace-a"])).toEqual([{ id: "p1" }]);
    expect(reader.getQueryData(["attachment-pdf-blob", "att-1"])).toBeUndefined();
    stop();
  });
});
