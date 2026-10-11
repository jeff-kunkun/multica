import { beforeEach, describe, expect, it, vi } from "vitest";
const { refresh, invalidate, setups } = vi.hoisted(() => ({
  refresh: vi.fn(), invalidate: vi.fn(), setups: [] as Array<(ws: MockWS, wsId: string) => (() => void)[]>,
}));
interface MockWS { on: ReturnType<typeof vi.fn>; onReconnect: ReturnType<typeof vi.fn> }
vi.mock("@tanstack/react-query", async (importOriginal) => ({ ...await importOriginal<typeof import("@tanstack/react-query")>(), useQueryClient: () => ({ invalidateQueries: invalidate }) }));
vi.mock("./workspace-link-ws-updaters", () => ({ refreshWorkspaceLinks: refresh }));
vi.mock("@/lib/use-ws-subscriptions", () => ({ useWSSubscriptions: (fn: (ws: MockWS, id: string) => (() => void)[]) => setups.push(fn) }));
vi.mock("@/data/api", () => ({ api: {} }));
import { useWorkspaceLinksRealtime } from "./use-workspace-links-realtime";
describe("mobile link subscriptions", () => {
  beforeEach(() => { refresh.mockClear(); invalidate.mockClear(); setups.length = 0; });
  it("refreshes link permissions and references on both sides and reconnect, ignoring foreign workspaces", () => {
    useWorkspaceLinksRealtime();
    const handlers = new Map<string, (p: unknown) => void>();
    let reconnect: () => void = () => {};
    setups[0]({ on: vi.fn((name, fn) => { handlers.set(name, fn); return () => {}; }), onReconnect: vi.fn((fn) => { reconnect = fn; return () => {}; }) }, "ours");
    handlers.get("workspace_link:changed")?.({ workspace_id: "other" });
    expect(refresh).not.toHaveBeenCalled();
    handlers.get("workspace_link:changed")?.({ workspace_id: "ours", side: "source" });
    handlers.get("workspace_link:changed")?.({ workspace_id: "ours", side: "viewer" });
    handlers.get("inbox:new")?.({ item: { workspace_id: "ours", type: "workspace_link_request" } });
    reconnect();
    expect(refresh).toHaveBeenCalledTimes(4);
    expect(refresh).toHaveBeenLastCalledWith(expect.anything(), "ours");
    handlers.get("chat:session_updated")?.({ chat_session_id: "session" });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["workspace-links", "ours", "chat"] });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["chat", "ours", "sessions"] });
  });
});
