import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useChatProjectFollow, useCurrentRouteProjectId } from "./use-chat-project-follow";

// Which set the rule resolves to is covered once in
// packages/core/chat/project-follow.test.ts. This suite owns the wiring: what
// the hooks read from the route and what they write back to the chat store.

vi.mock("@multica/core/api", () => ({
  api: {
    getIssue: vi.fn(),
    getProject: vi.fn(),
  },
}));

const navigation = { pathname: "/acme/inbox", searchParams: new URLSearchParams() };
vi.mock("../../navigation", () => ({
  useNavigation: () => navigation,
}));

const storeState = {
  selectedProjectIds: [] as string[],
  projectContextLocked: false,
  setSelectedProjectIds: vi.fn(),
};

vi.mock("@multica/core/chat", () => ({
  useChatStore: Object.assign(
    (selector: (s: typeof storeState) => unknown) => selector(storeState),
    { getState: () => storeState },
  ),
  // Pulled in through parseCurrentContextRoute's module; the recent-context
  // list plays no part in the follow rule.
  useRecentContextStore: () => [],
  selectRecentContexts: () => () => [],
}));

const WS_ID = "ws-1";

function wrapper() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  }
  return Wrapper;
}

function renderFollow(
  params: Partial<Parameters<typeof useChatProjectFollow>[0]> = {},
) {
  return renderHook(
    () =>
      useChatProjectFollow({
        routeProjectId: "project-alpha",
        isOpen: true,
        hasSession: false,
        projectsLoaded: true,
        ...params,
      }),
    { wrapper: wrapper() },
  );
}

describe("useCurrentRouteProjectId", () => {
  beforeEach(() => {
    vi.mocked(api.getIssue).mockReset();
    vi.mocked(api.getProject).mockReset();
    navigation.pathname = "/acme/inbox";
    navigation.searchParams = new URLSearchParams();
  });

  it("reads the project page's own project", async () => {
    navigation.pathname = "/acme/projects/project-alpha";
    vi.mocked(api.getProject).mockResolvedValue({ id: "project-alpha" } as never);

    const { result } = renderHook(() => useCurrentRouteProjectId(WS_ID), {
      wrapper: wrapper(),
    });

    await waitFor(() => expect(result.current).toBe("project-alpha"));
  });

  // The URL carries an identifier, so the project can only come from the issue.
  it("reads the open issue's project", async () => {
    navigation.pathname = "/acme/issues/MUL-12";
    vi.mocked(api.getIssue).mockResolvedValue({
      id: "issue-1",
      project_id: "project-beta",
    } as never);

    const { result } = renderHook(() => useCurrentRouteProjectId(WS_ID), {
      wrapper: wrapper(),
    });

    await waitFor(() => expect(result.current).toBe("project-beta"));
  });

  it("names no project on a route that has none", () => {
    const { result } = renderHook(() => useCurrentRouteProjectId(WS_ID), {
      wrapper: wrapper(),
    });

    expect(result.current).toBeNull();
    expect(api.getIssue).not.toHaveBeenCalled();
    expect(api.getProject).not.toHaveBeenCalled();
  });
});

describe("useChatProjectFollow", () => {
  beforeEach(() => {
    storeState.selectedProjectIds = [];
    storeState.projectContextLocked = false;
    storeState.setSelectedProjectIds = vi.fn();
  });

  it("binds the draft to the project the main UI is on", () => {
    renderFollow();

    expect(storeState.setSelectedProjectIds).toHaveBeenCalledWith(["project-alpha"]);
  });

  it("leaves a manually picked project alone", () => {
    storeState.projectContextLocked = true;
    storeState.selectedProjectIds = ["project-beta"];

    renderFollow();

    expect(storeState.setSelectedProjectIds).not.toHaveBeenCalled();
  });

  it("never rewrites an open session's set", () => {
    renderFollow({ hasSession: true });

    expect(storeState.setSelectedProjectIds).not.toHaveBeenCalled();
  });

  it("stays out of a closed window's persisted draft", () => {
    renderFollow({ isOpen: false });

    expect(storeState.setSelectedProjectIds).not.toHaveBeenCalled();
  });
});
