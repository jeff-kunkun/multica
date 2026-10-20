import type { ReactNode } from "react";
import { beforeEach, describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

const mockSetLastMode = vi.hoisted(() => vi.fn());
const mockBeginIsolatedDraft = vi.hoisted(() => vi.fn());
const mockEndIsolatedDraft = vi.hoisted(() => vi.fn());
const mockRefetchSourceContext = vi.hoisted(() => vi.fn());
const mockClearDraft = vi.hoisted(() => vi.fn());

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
      clearDraft: mockClearDraft,
    }),
  },
}));

const mockCreateModeStore = {
  lastMode: "agent" as "agent" | "manual",
  setLastMode: mockSetLastMode,
};

vi.mock("@multica/core/issues/stores/create-mode-store", () => ({
  useCreateModeStore: Object.assign(
    (selector: (s: typeof mockCreateModeStore) => unknown) =>
      selector(mockCreateModeStore),
    { getState: () => mockCreateModeStore },
  ),
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

vi.mock("./quick-create-issue", () => ({
  AgentCreatePanel: ({
    data,
    onSwitchMode,
  }: {
    data?: Record<string, unknown> | null;
    onSwitchMode?: (carry?: Record<string, unknown> | null) => void;
  }) => (
    <div>
      agent panel · {String(data?.anchor_comment_id ?? "ordinary")} · {data?.source_context_expanded ? "expanded" : "collapsed"}
      <button type="button" onClick={() => onSwitchMode?.({ parent_issue_id: data?.parent_issue_id })}>
        switch manual
      </button>
      <button
        type="button"
        onClick={() => (data?.source_context_on_expanded_change as ((expanded: boolean) => void) | undefined)?.(!data?.source_context_expanded)}
      >
        toggle source context
      </button>
    </div>
  ),
}));

vi.mock("./create-issue", () => ({
  ManualCreatePanel: ({
    data,
    onClose,
    onSwitchMode,
  }: {
    data?: Record<string, unknown> | null;
    onClose?: () => void;
    onSwitchMode?: (carry?: Record<string, unknown> | null) => void;
  }) => (
    <div>
      manual panel · {String(data?.anchor_comment_id ?? "ordinary")} · {data?.source_context_expanded ? "expanded" : "collapsed"}
      <button type="button" onClick={() => onSwitchMode?.({ parent_issue_id: data?.parent_issue_id })}>
        switch agent
      </button>
      <button type="button" onClick={() => onClose?.()}>
        close from manual
      </button>
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

// `cn` is deliberately NOT mocked here: the whole point of these assertions is
// that tailwind-merge keeps the phone cap and the `sm:` width as two separate
// groups instead of collapsing them into one max-width.
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
        initialMode="agent"
        data={{ parent_issue_id: "ordinary-parent" }}
      />,
    );

    expect(screen.getByText(/agent panel · ordinary/)).toBeInTheDocument();
    expect(mockBeginIsolatedDraft).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "switch manual" }));
    expect(screen.getByText(/manual panel · ordinary/)).toBeInTheDocument();
  });

  it("isolates source-context drafts and preserves source identity across mode switches", () => {
    const view = render(
      <CreateIssueDialog
        onClose={vi.fn()}
        initialMode="agent"
        data={{ anchor_comment_id: "comment-source", parent_issue_id: "parent-source" }}
      />,
    );

    expect(mockBeginIsolatedDraft).toHaveBeenCalledTimes(1);
    expect(screen.getByText(/agent panel · comment-source/)).toBeInTheDocument();
    expect(contentClass()).toContain("!h-96");
    expect(contentClass()).toContain("sm:!max-w-xl");
    fireEvent.click(screen.getByRole("button", { name: "toggle source context" }));
    expect(screen.getByText(/agent panel · comment-source · expanded/)).toBeInTheDocument();
    expect(contentClass()).toContain("!h-5/6");
    expect(contentClass()).toContain("sm:!max-w-2xl");
    expect(contentClass()).not.toContain("!h-96");
    fireEvent.click(screen.getByRole("button", { name: "switch manual" }));
    expect(screen.getByText(/manual panel · comment-source · expanded/)).toBeInTheDocument();
    expect(contentClass()).toContain("manual-dialog-class");
    expect(contentClass()).toContain("!h-5/6");
    fireEvent.click(screen.getByRole("button", { name: "switch agent" }));
    expect(screen.getByText(/agent panel · comment-source/)).toBeInTheDocument();

    view.unmount();
    expect(mockEndIsolatedDraft).toHaveBeenCalledTimes(1);
  });
});

/**
 * DENE-421: an unsent create draft does not outlive the dialog. Reopening a
 * face starts blank. The shell owns this rather than each panel: Esc, the
 * backdrop and every panel's own cancel all land on the same `onClose`.
 */
describe("CreateIssueDialog draft lifetime", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("clears the draft before handing the close up", () => {
    const onClose = vi.fn();
    render(<CreateIssueDialog onClose={onClose} initialMode="manual" />);

    fireEvent.click(screen.getByRole("button", { name: "close from manual" }));

    expect(mockClearDraft).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("keeps the draft across a mode switch within one open", () => {
    // The store still spans a switch: that is what stopped a manual body and an
    // agent prompt from destroying each other (MUL-5181). Only the close clears.
    render(<CreateIssueDialog onClose={vi.fn()} initialMode="manual" />);

    fireEvent.click(screen.getByRole("button", { name: "switch agent" }));
    fireEvent.click(screen.getByRole("button", { name: "switch manual" }));

    expect(mockClearDraft).not.toHaveBeenCalled();
  });
});
