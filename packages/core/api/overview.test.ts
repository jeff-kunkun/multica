// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import { parseOverview } from "./overview";

afterEach(() => vi.unstubAllGlobals());

describe("workspace overview boundary", () => {
  it("tolerates omitted sections and nullable fields without losing accessible rows", () => {
    const result = parseOverview({ workspace_id: "w", agents: [{ id: "a", name: "Agent", model: null, runtime_id: null }], omitted_sections: ["projects"] });
    expect(result?.agents[0]?.name).toBe("Agent");
    expect(result?.projects).toEqual([]);
    expect(result?.omittedSections).toEqual(["projects"]);
  });
  it("rejects malformed records instead of exposing unsafe values to rendering", () => {
    expect(parseOverview({ workspace_id: "w", agents: [{ id: "a", model: {} }] })).toBeNull();
  });
  it("uses runtime aliases and workspace names instead of raw identifiers", () => {
    const result = parseOverview({ workspace_id: "w", runtimes: [{ id: "r", name: "raw", custom_name: "Desk", provider: "codex" }], workspace_links: [{ id: "l", source: { name: "Production" }, target: { name: "Team" } }] });
    expect(result?.runtimes[0]?.name).toBe("Desk (Codex)");
    expect(result?.workspaceLinks[0]?.name).toBe("Production → Team");
  });
  it("fetches filtered overview with one request", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ workspace_id: "w" }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetch);
    const api = new ApiClient("https://example.test");
    await api.getOverview({ project: "p one", agent: "a", compact: true });
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch.mock.calls[0]?.[0]).toBe("https://example.test/api/overview?project=p+one&agent=a&compact=true");
  });
});
