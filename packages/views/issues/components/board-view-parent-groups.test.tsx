/**
 * @vitest-environment jsdom
 *
 * Board parent groups (DENE-2760, `office_parent_groups`): sub-issues of one
 * parent sit together under a colored header in every column, fold per
 * column, and a tab row narrows the board to one parent. Off, the board is
 * unchanged.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { configStore } from "@multica/core/config";
import { OFFICE_PARENT_GROUPS_FLAG } from "@multica/core/feature-flags";
import { ViewStoreProvider } from "@multica/core/issues/stores/view-store-context";
import { getIssueSurfaceViewStore } from "@multica/core/issues/stores/surface-view-store";
import type { Issue, IssueStatus } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { IssueContextMenuProvider } from "../actions/issue-actions-context-menu";
import { ParentIssueLookupProvider } from "../surface/parent-issue-context";
import type {
  IssueStatusPageState,
  IssueStatusPagination,
} from "../surface/use-issue-status-branches";
import { BoardView } from "./board-view";

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));

vi.mock("@multica/core/properties", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/properties")>()),
  propertyListOptions: () => ({
    queryKey: ["properties"],
    queryFn: async () => [],
  }),
  useSetIssueProperty: () => ({ mutate: () => {} }),
  useUnsetIssueProperty: () => ({ mutate: () => {} }),
}));

vi.mock("@multica/core/workspace/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/workspace/hooks")>()),
  useActorName: () => ({ getActorName: () => "Someone" }),
}));

vi.mock("@multica/core/auth", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/auth")>()),
  useAuthStore: (selector: (state: { user: { id: string } }) => unknown) =>
    selector({ user: { id: "viewer-1" } }),
}));

vi.mock("@multica/core/agents", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/agents")>()),
  isAgentRuntimeBound: () => true,
  useAgentPresenceDetail: () => ({ availability: "offline", workload: null }),
}));

vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("@multica/core/paths")>();
  return {
    ...actual,
    useCurrentWorkspace: () => ({ id: "ws-1", slug: "acme" }),
    useWorkspacePaths: () => actual.paths.workspace("acme"),
  };
});

vi.mock("../../navigation", () => ({
  AppLink: ({ children, newTabTitle: _newTabTitle, ...props }: React.ComponentProps<"a"> & { newTabTitle?: string }) => (
    <a {...props}>{children}</a>
  ),
  useNavigation: () => ({
    push: () => {},
    openInNewTab: () => {},
    getShareableUrl: (path: string) => `https://app.example${path}`,
    pathname: "/",
  }),
  resolveClickIntent: () => "push",
  useIntentNavigate: () => () => {},
}));

const P1 = "01a18700-0000-7000-8000-0000000000a1";
const P2 = "01a18700-0000-7000-8000-0000000000a2";
const UNLOADED = "01a18700-0000-7000-8000-0000000000a3";

function makeIssue(
  id: string,
  {
    number = 1,
    title = `Task ${id}`,
    status = "todo",
    parent = null,
  }: { number?: number; title?: string; status?: IssueStatus; parent?: string | null } = {},
): Issue {
  return {
    id,
    workspace_id: "ws-1",
    number,
    identifier: `MUL-${id}`,
    title,
    description: null,
    status,
    priority: "none",
    assignee_type: null,
    assignee_id: null,
    reviewer_type: null,
    reviewer_id: null,
    creator_type: "member",
    creator_id: "member-1",
    parent_issue_id: parent,
    project_id: null,
    position: 1,
    stage: null,
    start_date: null,
    due_date: null,
    labels: [],
    metadata: {},
    properties: {},
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

// Parents get their own prefix, so the chips naming them never read as cards.
const PARENTS = [
  { ...makeIssue(P1, { number: 2734, title: "Multica 办公室: 看板防卡死", status: "in_review" }), identifier: "DENE-2734" },
  { ...makeIssue(P2, { number: 2740, title: "Board colors", status: "in_review" }), identifier: "DENE-2740" },
];

// Todo column, in the surface's order: a loose card sits between P1's two.
const BOARD = [
  makeIssue("c1", { parent: P1 }),
  makeIssue("loose"),
  makeIssue("c2", { parent: P1 }),
  makeIssue("d1", { parent: P2 }),
  makeIssue("c3", { status: "in_progress", parent: P1 }),
];

class ObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() {
    return [];
  }
}

function statusPage(hasMore: boolean, isFetching = false): IssueStatusPageState {
  return {
    total: hasMore ? 40 : 4,
    loaded: 4,
    hasMore,
    isLoading: false,
    isFetching,
    isError: false,
    loadMore: () => {},
    retry: () => {},
  };
}

function column(statusLabel: string) {
  return screen.getByText(statusLabel).closest("div.flex-col") as HTMLElement;
}

function cardOrder(col: HTMLElement) {
  return within(col)
    .queryAllByText(/^MUL-/)
    .map((el) => el.textContent);
}

describe("Board parent groups", () => {
  let queryClient: QueryClient;

  beforeEach(() => {
    vi.stubGlobal("IntersectionObserver", ObserverStub);
    vi.stubGlobal("ResizeObserver", ObserverStub);
    queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: Infinity } },
    });
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    configStore.getState().setLocalFeatureFlags({});
  });

  function render({
    on = true,
    issues = BOARD,
    statusPagination,
  }: { on?: boolean; issues?: Issue[]; statusPagination?: IssueStatusPagination } = {}) {
    configStore.getState().setLocalFeatureFlags({ [OFFICE_PARENT_GROUPS_FLAG]: on });
    const store = getIssueSurfaceViewStore(
      `board-parent-groups-${Math.floor(Math.random() * 1e9)}`,
    );
    const board = (pagination?: IssueStatusPagination) => (
      <QueryClientProvider client={queryClient}>
        <ViewStoreProvider store={store}>
          <IssueContextMenuProvider>
            <ParentIssueLookupProvider issues={[...PARENTS, ...issues]}>
              <BoardView
                issues={issues}
                visibleStatuses={["todo", "in_progress"]}
                onMoveIssue={() => {}}
                statusPagination={pagination}
              />
            </ParentIssueLookupProvider>
          </IssueContextMenuProvider>
        </ViewStoreProvider>
      </QueryClientProvider>
    );
    const { rerender } = renderWithI18n(board(statusPagination));
    return Object.assign(store, {
      repaginate: (pagination: IssueStatusPagination) => rerender(board(pagination)),
    });
  }

  it("leaves the board as it was with the flag off", () => {
    render({ on: false });

    expect(screen.queryByTestId("board-parent-group")).toBeNull();
    expect(screen.queryByRole("group", { name: "Filter by parent issue" })).toBeNull();
    expect(cardOrder(column("Todo"))).toEqual(["MUL-c1", "MUL-loose", "MUL-c2", "MUL-d1"]);
    expect(screen.getAllByTestId("parent-issue-badge").length).toBeGreaterThan(0);
  });

  it("puts one parent's sub-issues together under a header with its number, short title and count", () => {
    render();

    const todo = column("Todo");
    expect(cardOrder(todo)).toEqual(["MUL-c1", "MUL-c2", "MUL-loose", "MUL-d1"]);
    const headers = within(todo).getAllByTestId("board-parent-group");
    expect(headers.map((h) => h.textContent)).toEqual([
      "2734看板防卡死· 2 issues",
      "2740Board colors· 1 issue",
    ]);
    // The header names the parent, so the cards in it drop their own chip.
    expect(screen.queryByTestId("parent-issue-badge")).toBeNull();
  });

  it("colors a parent's cards the same in every column, and leaves loose cards plain", () => {
    render();

    const barOf = (identifier: string) =>
      screen
        .getByText(identifier)
        .closest("[data-board-card]")!
        .querySelector<HTMLElement>("[data-testid='board-card-group-bar']");

    expect(barOf("MUL-loose")).toBeNull();
    const c1 = barOf("MUL-c1")!.style.backgroundColor;
    expect(c1).toContain("light-dark(");
    expect(barOf("MUL-c3")!.style.backgroundColor).toBe(c1);
    expect(barOf("MUL-d1")!.style.backgroundColor).not.toBe(c1);
  });

  it("folds a group in one column only", () => {
    const store = render();

    const todo = column("Todo");
    const toggle = within(todo).getByRole("button", { name: /2734/ });
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    fireEvent.click(toggle);

    expect(cardOrder(column("Todo"))).toEqual(["MUL-loose", "MUL-d1"]);
    expect(within(column("Todo")).getByRole("button", { name: /2734/ }).getAttribute("aria-expanded")).toBe("false");
    expect(cardOrder(column("In Progress"))).toEqual(["MUL-c3"]);
    expect(store.getState().boardCollapsedParentGroups).toHaveLength(1);
  });

  it("narrows every column to one parent from its tab, and 「All」 brings the board back", () => {
    render();

    const tabs = screen.getByRole("group", { name: "Filter by parent issue" });
    const all = within(tabs).getByRole("button", { name: "All" });
    expect(all.getAttribute("aria-pressed")).toBe("true");
    // Newest parent first.
    expect(
      within(tabs)
        .getAllByRole("button")
        .map((b) => b.textContent),
    ).toEqual(["All", "2740Board colors· 1 issue", "2734看板防卡死· 3 issues"]);

    fireEvent.click(within(tabs).getByRole("button", { name: /2734/ }));
    expect(cardOrder(column("Todo"))).toEqual(["MUL-c1", "MUL-c2"]);
    expect(cardOrder(column("In Progress"))).toEqual(["MUL-c3"]);
    // The column counts what it shows.
    expect(column("Todo").textContent).toContain("2");

    fireEvent.click(all);
    expect(cardOrder(column("Todo"))).toEqual(["MUL-c1", "MUL-c2", "MUL-loose", "MUL-d1"]);
  });

  it("keeps the tab row off while a column has more pages, and still groups what is loaded", () => {
    const store = render({
      statusPagination: {
        todo: statusPage(true),
        in_progress: statusPage(false),
      } as IssueStatusPagination,
    });
    act(() => store.getState().setBoardParentFilter(P1));

    // A parent on an unloaded page would be missing and the counts short.
    expect(screen.queryByRole("group", { name: "Filter by parent issue" })).toBeNull();
    // A tab picked earlier doesn't narrow a board it can't see whole.
    expect(cardOrder(column("Todo"))).toEqual(["MUL-c1", "MUL-c2", "MUL-loose", "MUL-d1"]);
    expect(within(column("Todo")).getAllByTestId("board-parent-group")).toHaveLength(2);
  });

  it("keeps the tab row off while a column with no more pages is still fetching", () => {
    const store = render({
      statusPagination: {
        todo: statusPage(false, true),
        in_progress: statusPage(false),
      } as IssueStatusPagination,
    });
    act(() => store.getState().setBoardParentFilter(P1));

    expect(screen.queryByRole("group", { name: "Filter by parent issue" })).toBeNull();
    expect(cardOrder(column("Todo"))).toEqual(["MUL-c1", "MUL-c2", "MUL-loose", "MUL-d1"]);
  });

  it("keeps the tab row and the picked tab through a background refresh", () => {
    const settled = {
      todo: statusPage(false),
      in_progress: statusPage(false),
    } as IssueStatusPagination;
    const store = render({ statusPagination: settled });
    act(() => store.getState().setBoardParentFilter(P1));
    expect(cardOrder(column("Todo"))).toEqual(["MUL-c1", "MUL-c2"]);

    store.repaginate({ ...settled, todo: statusPage(false, true) });

    expect(screen.getByRole("group", { name: "Filter by parent issue" })).toBeTruthy();
    expect(cardOrder(column("Todo"))).toEqual(["MUL-c1", "MUL-c2"]);
  });

  it("still steps aside after settling once if a column gains more pages", () => {
    const settled = {
      todo: statusPage(false),
      in_progress: statusPage(false),
    } as IssueStatusPagination;
    const store = render({ statusPagination: settled });
    act(() => store.getState().setBoardParentFilter(P1));

    store.repaginate({ ...settled, todo: statusPage(true, true) });

    expect(screen.queryByRole("group", { name: "Filter by parent issue" })).toBeNull();
    expect(cardOrder(column("Todo"))).toEqual(["MUL-c1", "MUL-c2", "MUL-loose", "MUL-d1"]);
  });

  it("shows the tab row once every column has its last page", () => {
    render({
      statusPagination: {
        todo: statusPage(false),
        in_progress: statusPage(false),
      } as IssueStatusPagination,
    });

    expect(screen.getByRole("group", { name: "Filter by parent issue" })).toBeTruthy();
  });

  it("shows no tab row on a board without sub-issues", () => {
    render({ issues: [makeIssue("loose"), makeIssue("other")] });

    expect(screen.queryByRole("group", { name: "Filter by parent issue" })).toBeNull();
    expect(screen.queryByTestId("board-parent-group")).toBeNull();
  });

  it("still groups the sub-issues of a parent that is not loaded", () => {
    render({ issues: [makeIssue("o1", { parent: UNLOADED }), makeIssue("o2", { parent: UNLOADED })] });

    const header = within(column("Todo")).getByTestId("board-parent-group");
    expect(header.textContent).toBe("Parent issue· 2 issues");
  });
});
