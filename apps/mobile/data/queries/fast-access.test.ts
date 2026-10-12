import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { fastAccessOptions } from "./fast-access";
const readFastAccess = vi.hoisted(() => vi.fn());
vi.mock("@/data/api", () => ({ api: { readFastAccess } }));
describe("mobile durable activity", () => {
  it("rebuilds without since, forwards cancellation, clears old data on failure", async () => {
    const client = new QueryClient();
    const options = fastAccessOptions("user", "workspace", false, "refresh");
    readFastAccess.mockResolvedValueOnce({ complete: true, nextCursor: null, changes: [{ id: "private" }] });
    await client.fetchInfiniteQuery(options);
    expect(readFastAccess).toHaveBeenLastCalledWith({ scope: { mode: "workspace_ids", workspace_ids: ["workspace"] }, include: ["active_runs", "chats"], limit: 100, max_bytes: 50000 }, { signal: expect.any(AbortSignal) });
    readFastAccess.mockRejectedValueOnce(new Error("scope_reset"));
    await expect(client.fetchInfiniteQuery(options)).rejects.toThrow("scope_reset");
    expect(client.getQueryData(options.queryKey)).toEqual({ pages: [], pageParams: [] });
    expect(options.refetchInterval).toBe(30_000);
    client.clear();
  });
});
