import { describe, expect, it } from "vitest";
import { parseFastAccess } from "./fast-access";

const page = { schema_version: "1", mode: "timestamp_snapshot", as_of: "2026-10-11T00:00:00Z", scope_revision: "revision", active_runs: [], changes: [], active_runs_complete: true, complete: true, next_cursor: null };
describe("fast access response", () => {
  it("does not turn malformed or partial envelopes into empty complete results", () => {
    expect(parseFastAccess({})).toBeNull();
    expect(parseFastAccess({ ...page, complete: false })).toBeNull();
    expect(parseFastAccess({ ...page, active_runs_complete: undefined })).toBeNull();
    expect(parseFastAccess({ ...page, active_runs: [{ id: "run" }] })).toBeNull();
  });
  it("accepts the durable mode", () => {
    expect(parseFastAccess({ ...page, mode: "durable_delta" })).not.toBeNull();
  });
  it("retains continuation and maps unread without modifying it", () => {
    const result = parseFastAccess({ ...page, complete: false, next_cursor: "next", changes: [{ id: "chat", workspace_id: "ws", kind: "chat", changed_at: page.as_of, unread_count: 3, content_truncated: true, preview: "你好" }] });
    expect(result?.nextCursor).toBe("next");
    expect(result?.complete).toBe(false);
    expect(result?.changes[0]).toMatchObject({ workspaceId: "ws", unreadCount: 3, contentTruncated: true, preview: "你好" });
  });
});
