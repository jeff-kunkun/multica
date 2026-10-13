import type { ReactNode } from "react";
import { beforeEach, describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

const mockBeginIsolatedDraft = vi.hoisted(() => vi.fn());
const mockEndIsolatedDraft = vi.hoisted(() => vi.fn());
const mockRefetchSourceContext = vi.hoisted(() => vi.fn());

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({
    data: { capture_token: "sha256:preview-token" },
    isLoading: false,
    isFetching: false,
    isError: false,
    error: null,
    refetch: mockRefetchSourceContext,
  }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-test",
}));

vi.mock("@multica/core/issues/queries", () => ({
  sourceContextPreviewOptions: (wsId: string, anchorCommentId: string) => ({
    queryKey: ["source-context", "preview", wsId, anchorCommentId],
  }),
}));

vi.mock("@multica/core/issues/stores/draft-store", () => ({
  useIssueDraftStore: {
    getState: () => ({
      beginIsolatedDraft: mockBeginIsolatedDraft,
      endIsolatedDraft: mockEndIsolatedDraft,
    }),
  },
}));

vi.mock("@multica/ui/components/ui/dialog", () => ({
  Dialog: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogContent: ({
    className,
    children,
  }: {
    className?: string;
    children: ReactNode;
  }) => (
    <div data-testid="dialog-content" className={className}>
      {children}
    </div>
  ),
}));

vi.mock("./create-issue", () => ({
  ManualCreatePanel: ({
    data,
  }: {
    data?: Record<string, unknown> | null;
  }) => (
    <div>
      manual panel · {String(data?.anchor_comment_id ?? "ordinary")} · {data?.source_context_expanded ? "expanded" : "collapsed"}
      <button
        type="button"
        onClick={() => (data?.source_context_on_expanded_change as ((expanded: boolean) => void) | undefined)?.(!data?.source_context_expanded)}
      >
        toggle source context
      </button>
    </div>
  ),
  manualDialogContentClass: () => "manual-dialog-class",
}));

import { CreateIssueDialog } from "./create-issue-dialog";

function contentClass() {
  return screen.getByTestId("dialog-content").className;
}

describe("CreateIssueDialog sizing", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("leaves ordinary create outside the isolated source-context path", () => {
    render(
      <CreateIssueDialog
        onClose={vi.fn()}
        data={{ parent_issue_id: "ordinary-parent" }}
      />,
    );

    expect(screen.getByText(/manual panel · ordinary/)).toBeInTheDocument();
    expect(mockBeginIsolatedDraft).not.toHaveBeenCalled();
  });

  it("isolates source-context drafts and grows with the expanded context", () => {
    const view = render(
      <CreateIssueDialog
        onClose={vi.fn()}
        data={{ anchor_comment_id: "comment-source", parent_issue_id: "parent-source" }}
      />,
    );

    expect(mockBeginIsolatedDraft).toHaveBeenCalledTimes(1);
    expect(screen.getByText(/manual panel · comment-source · collapsed/)).toBeInTheDocument();
    expect(contentClass()).toContain("manual-dialog-class");
    expect(contentClass()).not.toContain("!h-5/6");
    fireEvent.click(screen.getByRole("button", { name: "toggle source context" }));
    expect(screen.getByText(/manual panel · comment-source · expanded/)).toBeInTheDocument();
    expect(contentClass()).toContain("!h-5/6");

    view.unmount();
    expect(mockEndIsolatedDraft).toHaveBeenCalledTimes(1);
  });
});
