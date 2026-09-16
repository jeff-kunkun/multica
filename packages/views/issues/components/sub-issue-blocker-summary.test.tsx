/**
 * @vitest-environment jsdom
 *
 * The deep (grandchild) and cross-family (`waiting_on`) root causes go through
 * the component's REAL query chain — `childrenByParentsOptions` per nesting
 * layer, `issueIdentifierOptions` for a foreign ticket number — instead of a
 * hand-built `children` snapshot, so a regression in either expansion path, or
 * in the identifier fallback for a target that no query could resolve, fails
 * here. `@multica/core/issues/queries` is therefore intentionally not mocked.
 */
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ReactElement } from "react";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import type { Issue } from "@multica/core/types";
import { RESOURCES } from "../../test/i18n";

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ issueDetail: (id: string) => `/issues/${id}` }),
}));

// Both query option factories are keyed by the route-derived workspace id.
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));

vi.mock("../../navigation", () => ({
  AppLink: ({ children, ...props }: React.ComponentProps<"a">) => <a {...props}>{children}</a>,
}));

import { SubIssueBlockerBadge, SubIssueBlockerSummary } from "./sub-issue-blocker-summary";

afterEach(cleanup);

function issue(id: string, overrides: Partial<Issue> = {}): Issue {
  return {
    id,
    workspace_id: "ws-1",
    number: 1,
    identifier: id,
    title: id,
    description: null,
    status: "todo",
    priority: "none",
    assignee_type: null,
    assignee_id: null,
    creator_type: "member",
    creator_id: "member-1",
    parent_issue_id: null,
    project_id: null,
    position: 0,
    stage: 1,
    start_date: null,
    due_date: null,
    metadata: {},
    properties: {},
    created_at: "2026-09-15T00:00:00Z",
    updated_at: "2026-09-15T00:00:00Z",
    ...overrides,
  };
}

function renderWithProviders(ui: ReactElement) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <I18nProvider locale="en" resources={RESOURCES}>{ui}</I18nProvider>
    </QueryClientProvider>,
  );
}

/**
 * Drives the real query options through the shared API singleton: children per
 * batch, and identifier point reads. `null` is what `issueIdentifierOptions`
 * caches for a 404, i.e. "no such ticket in this workspace".
 */
function installIssueApi(
  stub: {
    /** Keyed by parent id; the batched endpoint answers many parents at once. */
    childrenByParent?: Record<string, Issue[]>;
    byIdentifier?: Record<string, Issue | null>;
  } = {},
) {
  const childrenByParent = stub.childrenByParent ?? {};
  const byIdentifier = stub.byIdentifier ?? {};
  const listChildrenByParents = vi.fn(async (parentIds: readonly string[]) => ({
    issues: parentIds.flatMap((id) => childrenByParent[id] ?? []),
  }));
  const getIssue = vi.fn(async (identifier: string) => byIdentifier[identifier] ?? null);
  setApiInstance({ listChildrenByParents, getIssue } as unknown as ApiClient);
  return { listChildrenByParents, getIssue };
}

describe("SubIssueBlockerSummary", () => {
  it("shows root cause links and the needs-you count", () => {
    installIssueApi();
    const parent = issue("DENE-1");
    const child = issue("child-2", {
      identifier: "DENE-2",
      title: "Needs a decision",
      parent_issue_id: parent.id,
      metadata: {
        "close.conclusion": "blocked",
        "close.block_kind": "decision",
        "close.block_action": "decide",
      },
    });

    renderWithProviders(<SubIssueBlockerSummary issue={parent} children={[child]} />);

    expect(screen.getByTestId("sub-issue-blocker-summary")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "DENE-2 · Needs a decision" })).toHaveAttribute("href", "/issues/child-2");
    expect(screen.getByText("1 need you")).toBeInTheDocument();
  });

  it("links a grandchild root cause resolved through the per-layer child query", async () => {
    const parent = issue("DENE-1");
    const child = issue("child-2", { identifier: "DENE-2", parent_issue_id: parent.id });
    const grandchild = issue("grandchild-3", {
      identifier: "DENE-3",
      title: "Deep decision",
      parent_issue_id: child.id,
      metadata: { "close.conclusion": "blocked", "close.block_kind": "decision" },
    });
    // The grandchild is deliberately absent from the `children` prop: it can
    // only reach the card through the component's own batched child query.
    const { listChildrenByParents } = installIssueApi({ childrenByParent: { [child.id]: [grandchild] } });

    renderWithProviders(<SubIssueBlockerSummary issue={parent} children={[child]} />);

    await waitFor(() => expect(listChildrenByParents).toHaveBeenCalledWith([child.id]));
    expect(await screen.findByRole("link", { name: "DENE-3 · Deep decision" })).toHaveAttribute("href", "/issues/grandchild-3");
  });

  it("links a cross-family waiting_on root cause resolved by identifier", async () => {
    const parent = issue("DENE-1");
    const child = issue("child-2", {
      identifier: "DENE-2",
      parent_issue_id: parent.id,
      metadata: { "close.waiting_on": "DENE-410" },
    });
    const otherFamily = issue("other-410", { identifier: "DENE-410", title: "Other family root" });
    const { getIssue } = installIssueApi({ byIdentifier: { "DENE-410": otherFamily } });

    renderWithProviders(<SubIssueBlockerSummary issue={parent} children={[child]} />);

    await waitFor(() => expect(getIssue).toHaveBeenCalledWith("DENE-410", expect.anything()));
    expect(await screen.findByRole("link", { name: "DENE-410 · Other family root" })).toHaveAttribute("href", "/issues/other-410");
  });

  it("keeps an unresolvable waiting_on root cause listed and clickable", async () => {
    const parent = issue("DENE-1");
    const child = issue("child-2", {
      identifier: "DENE-2",
      parent_issue_id: parent.id,
      metadata: { "close.waiting_on": "DENE-410" },
    });
    // Identifier lookup answers with nothing (deleted ticket, foreign workspace
    // prefix, or a query that never settled). The row must still list the
    // ticket number and stay one click away from the blocker.
    const { getIssue } = installIssueApi();

    renderWithProviders(<SubIssueBlockerSummary issue={parent} children={[child]} />);

    await waitFor(() => expect(getIssue).toHaveBeenCalledWith("DENE-410", expect.anything()));
    expect(await screen.findByRole("link", { name: "DENE-410" })).toHaveAttribute("href", "/issues/DENE-410");
  });

  it("does not render the summary when there are no blockers", () => {
    installIssueApi();
    renderWithProviders(<SubIssueBlockerSummary issue={issue("DENE-1")} children={[issue("DENE-2")]} />);
    expect(screen.queryByTestId("sub-issue-blocker-summary")).not.toBeInTheDocument();
  });

  it("renders solid and hollow badges for ROOT and PROPAGATED states", () => {
    const { rerender } = renderWithProviders(<SubIssueBlockerBadge state="ROOT" rootCause="DENE-2" />);
    expect(screen.getByTestId("sub-issue-blocker-badge")).toHaveAttribute("data-blocker-state", "ROOT");
    expect(screen.getByText(/Root blocker DENE-2/)).toBeInTheDocument();

    rerender(<SubIssueBlockerBadge state="PROPAGATED" rootCause="DENE-2" />);
    expect(screen.getByTestId("sub-issue-blocker-badge")).toHaveAttribute("data-blocker-state", "PROPAGATED");
    expect(screen.getByText(/Blocked by a sub-issue DENE-2/)).toBeInTheDocument();
  });
});
