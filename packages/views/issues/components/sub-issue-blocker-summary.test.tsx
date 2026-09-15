/**
 * @vitest-environment jsdom
 */
import { cleanup, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Issue } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ issueDetail: (id: string) => `/issues/${id}` }),
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/issues/queries", () => ({
  childIssuesOptions: (_ws: string, id: string) => ({ queryKey: ["children", id], queryFn: async () => [] }),
  issueIdentifierOptions: (_ws: string, identifier: string) => ({ queryKey: ["identifier", identifier], queryFn: async () => null }),
}));

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

describe("SubIssueBlockerSummary", () => {
  it("shows root cause links and the needs-you count", () => {
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

    renderWithI18n(<SubIssueBlockerSummary issue={parent} children={[child]} />);

    expect(screen.getByTestId("sub-issue-blocker-summary")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "DENE-2 · Needs a decision" })).toHaveAttribute("href", "/issues/child-2");
    expect(screen.getByText("1 need you")).toBeInTheDocument();
  });

  it("renders solid and hollow badges for ROOT and PROPAGATED states", () => {
    const { rerender } = renderWithI18n(<SubIssueBlockerBadge state="ROOT" rootCause="DENE-2" />);
    expect(screen.getByTestId("sub-issue-blocker-badge")).toHaveAttribute("data-blocker-state", "ROOT");
    expect(screen.getByText(/Root blocker DENE-2/)).toBeInTheDocument();

    rerender(<SubIssueBlockerBadge state="PROPAGATED" rootCause="DENE-2" />);
    expect(screen.getByTestId("sub-issue-blocker-badge")).toHaveAttribute("data-blocker-state", "PROPAGATED");
    expect(screen.getByText(/Blocked by a sub-issue DENE-2/)).toBeInTheDocument();
  });

  it("does not render the summary when there are no blockers", () => {
    renderWithI18n(<SubIssueBlockerSummary issue={issue("DENE-1")} children={[issue("DENE-2")]} />);
    expect(screen.queryByTestId("sub-issue-blocker-summary")).not.toBeInTheDocument();
  });

  it("links a deep root cause found in a grandchild", () => {
    const parent = issue("DENE-1");
    const child = issue("child-2", { identifier: "DENE-2", parent_issue_id: parent.id });
    const grandchild = issue("grandchild-3", {
      identifier: "DENE-3", parent_issue_id: child.id,
      metadata: { "close.conclusion": "blocked", "close.block_kind": "decision" },
    });
    // The component's query layer is exercised with the already-fetched
    // hierarchy by passing the grandchild in the child snapshot.
    renderWithI18n(<SubIssueBlockerSummary issue={parent} children={[child, grandchild]} />);
    expect(screen.getByRole("link", { name: "DENE-3 · DENE-3" })).toHaveAttribute("href", "/issues/grandchild-3");
  });

  it("links a cross-family waiting_on root cause", () => {
    const parent = issue("DENE-1");
    const child = issue("child-2", {
      identifier: "DENE-2", parent_issue_id: parent.id,
      metadata: { "close.waiting_on": "DENE-410" },
    });
    const target = issue("other-410", { identifier: "DENE-410", title: "Other root" });
    renderWithI18n(<SubIssueBlockerSummary issue={parent} children={[child, target]} />);
    expect(screen.getByRole("link", { name: "DENE-410 · Other root" })).toHaveAttribute("href", "/issues/other-410");
  });
});
