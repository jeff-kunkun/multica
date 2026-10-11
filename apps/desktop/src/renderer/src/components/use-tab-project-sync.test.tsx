import { describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { projectListOptions } from "@multica/core/projects/queries";
import { useTabStore } from "@/stores/tab-store";
import { stripProjectPrefix, useTabProjectSync } from "./use-tab-project-sync";

vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  useCurrentWorkspace: () => ({ id: "ws1", slug: "acme" }),
}));

function Harness() {
  const slug = useTabStore((s) => s.activeWorkspaceSlug);
  const tabs = useTabStore((s) => (slug ? s.byWorkspace[slug]?.tabs : undefined));
  useTabProjectSync(slug, tabs ?? []);
  return null;
}

describe("useTabProjectSync", () => {
  // The sync rewrites tab objects; if the hook re-ran on that it would feed
  // itself. A long project title (over the stored cap) used to make every
  // pass look like a change.
  it("settles instead of re-syncing its own writes", () => {
    const store = useTabStore.getState();
    store.reset();
    useTabStore.setState({ tabGroupMode: "project" });
    store.switchWorkspace("acme");
    store.addTab("/acme/projects/p1", "Project");

    const client = new QueryClient({
      defaultOptions: { queries: { staleTime: Infinity, retry: false } },
    });
    client.setQueryData(projectListOptions("ws1").queryKey, {
      projects: [{ id: "p1", title: "a".repeat(120), icon: null }],
    } as never);

    let writes = 0;
    const unsubscribe = useTabStore.subscribe(() => {
      writes += 1;
    });
    const { rerender } = render(
      <QueryClientProvider client={client}>
        <Harness />
      </QueryClientProvider>,
    );
    rerender(
      <QueryClientProvider client={client}>
        <Harness />
      </QueryClientProvider>,
    );
    unsubscribe();

    const group = useTabStore.getState().byWorkspace.acme;
    expect(group.groups).toHaveLength(1);
    expect(group.groups[0].name).toBe("a".repeat(80));
    // One write files the tab; nothing after that.
    expect(writes).toBe(1);
  });
});

describe("stripProjectPrefix", () => {
  it("drops a leading project name and separator", () => {
    expect(stripProjectPrefix("Multica 魔改 · 额度栏", "Multica 魔改")).toBe("额度栏");
    expect(stripProjectPrefix("Multica 魔改", "Multica 魔改")).toBe("Multica 魔改");
    expect(stripProjectPrefix("Other · x", "Multica 魔改")).toBe("Other · x");
  });
});
