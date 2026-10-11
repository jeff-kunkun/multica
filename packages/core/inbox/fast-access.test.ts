import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { fastAccessOptions } from "./fast-access";
const readFastAccess = vi.hoisted(() => vi.fn());
vi.mock("../api", () => ({ api: { readFastAccess } }));
describe("compact activity queries", () => {
  it("binds cache to caller and workspace and sends cursor-only continuation", async () => {
    const options = fastAccessOptions("user", "workspace", false, "2026-10-11T00:00:00Z");
    expect(options.queryKey).toEqual(["fast-access", "user", "workspace", false, "2026-10-11T00:00:00Z"]);
    readFastAccess.mockResolvedValueOnce({ complete: false, nextCursor: "cursor" });
    const client = new QueryClient();
    await client.fetchInfiniteQuery(options);
    expect(readFastAccess).toHaveBeenLastCalledWith({ scope: { mode: "workspace_ids", workspace_ids: ["workspace"] }, since: "2026-10-11T00:00:00Z", include: ["active_runs", "chats"], limit: 100, max_bytes: 50000 });
    client.clear();
    readFastAccess.mockResolvedValueOnce({ complete: true, nextCursor: null });
    await client.fetchInfiniteQuery({ ...options, initialPageParam: "cursor" });
    expect(readFastAccess).toHaveBeenLastCalledWith({ cursor: "cursor", limit: 100, max_bytes: 50000 });
    client.clear();
  });
});
