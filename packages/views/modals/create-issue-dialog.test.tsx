import type { ReactNode } from "react";
import { beforeEach, describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

const mockSetLastMode = vi.hoisted(() => vi.fn());
const mockBeginIsolatedDraft = vi.hoisted(() => vi.fn());
const mockEndIsolatedDraft = vi.hoisted(() => vi.fn());
const mockRefetchSourceContext = vi.hoisted(() => vi.fn());
const mockClearDraft = vi.hoisted(() => vi.fn());

// The preview a source-context dialog reads. `null` is the loading/failed
// shape, which is also what a backend that predates the endpoint answers with.
const sourcePreview = vi.hoisted(() => ({ current: null as unknown }));

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({
    data: sourcePreview.current,
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

// A stand-in for the create draft's align slot: the shell writes the comment
// seed into it, and the alignment face reads its body from it.
const draftStore = vi.hoisted(() => ({
  alignRequest: "",
  setAlign: vi.fn(),
}));

vi.mock("@multica/core/issues/stores/draft-store", () => ({
  useIssueDraftStore: Object.assign(
    (selector: (s: { draft: { align: { request: string } } }) => unknown) =>
      selector({ draft: { align: { request: draftStore.alignRequest } } }),
    {
      getState: () => ({
        draft: { align: { request: draftStore.alignRequest } },
        setAlign: draftStore.setAlign,
        beginIsolatedDraft: mockBeginIsolatedDraft,
        endIsolatedDraft: mockEndIsolatedDraft,
        clearDraft: mockClearDraft,
      }),
    },
  ),
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
    onSwitchMode,
    onSwitchToAlign,
  }: {
    data?: Record<string, unknown> | null;
    onSwitchMode?: (carry?: Record<string, unknown> | null) => void;
    onSwitchToAlign?: (carry?: Record<string, unknown> | null) => void;
  }) => (
    <div>
      manual panel · {String(data?.anchor_comment_id ?? "ordinary")} · {data?.source_context_expanded ? "expanded" : "collapsed"} · parent:{String(data?.parent_issue_id ?? "none")}
      <button type="button" onClick={() => onSwitchMode?.({ parent_issue_id: data?.parent_issue_id })}>
        switch agent
      </button>
      <button
        type="button"
        onClick={() =>
          onSwitchToAlign?.({ parent_issue_id: data?.parent_issue_id ?? "carried-parent" })
        }
      >
        switch align
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

// The alignment face is the same shell's third mode; its own suite covers the
// input, so here it only has to prove the switch keeps ONE DialogContent.
vi.mock("./align-create-issue", () => ({
  AlignCreatePanel: ({
    onClose,
    onSwitchMode,
    parentIssueId,
  }: {
    onClose: () => void;
    onSwitchMode?: (carry?: Record<string, unknown> | null) => void;
    parentIssueId?: string;
  }) => (
    <div>
      align panel · parent:{String(parentIssueId ?? "none")}
      <button type="button" onClick={() => onSwitchMode?.(null)}>
        switch manual from align
      </button>
      <button type="button" onClick={() => onClose()}>
        close from align
      </button>
    </div>
  ),
  alignDialogContentClass: () => "align-dialog-class",
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
    sourcePreview.current = null;
    draftStore.alignRequest = "";
    draftStore.setAlign.mockClear();
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

  // Parent context is the one seed that is NOT persisted in the draft store:
  // it rides the carry channel per invocation. The alignment face reads none
  // of it, so the shell has to hold it for the duration of the detour —
  // otherwise "Add sub issue" → align → back silently files a top-level issue.
  it("keeps the carried parent across a manual → align → manual round trip", () => {
    render(
      <CreateIssueDialog
        onClose={vi.fn()}
        initialMode="manual"
        data={{ parent_issue_id: "parent-1" }}
      />,
    );

    expect(screen.getByText(/parent:parent-1/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "switch align" }));
    expect(screen.getByText(/align panel · parent:parent-1/)).toBeInTheDocument();
    expect(mockSetLastMode).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "switch manual from align" }));
    expect(screen.getByText(/parent:parent-1/)).toBeInTheDocument();
  });

  // DENE-452: the parent the shell holds is not only kept for the way back —
  // the alignment face is what writes it into the draft the confirm builds the
  // group from. A shell that held it and never handed it over would file the
  // conversation's issues at the top level anyway.
  it("hands the parent to the alignment face, including from a comment entry", () => {
    render(
      <CreateIssueDialog
        onClose={vi.fn()}
        initialMode="align"
        data={{ anchor_comment_id: "comment-source", parent_issue_id: "parent-source" }}
      />,
    );

    expect(screen.getByText(/align panel · parent:parent-source/)).toBeInTheDocument();
  });

  it("leaves the alignment face without a parent for a standalone alignment", () => {
    render(<CreateIssueDialog onClose={vi.fn()} initialMode="align" data={{}} />);

    expect(screen.getByText(/align panel · parent:none/)).toBeInTheDocument();
  });

  // "Start aligning from this comment" seeds the conversation with the thread
  // (DENE-452). The seed is written into the draft's own align slot — that is
  // where the alignment face reads its body from — and only ONCE, so a user who
  // deletes the quote and writes their own sentence keeps it.
  it("seeds the alignment request from the comment thread, once", () => {
    sourcePreview.current = {
      capture_token: "sha256:preview-token",
      anchor_comment_id: "comment-source",
      source_issue: { id: "i-1", identifier: "MUL-9" },
      comment_thread: [{ id: "comment-source", content: "the toggle flickers" }],
    };
    render(
      <CreateIssueDialog
        onClose={vi.fn()}
        initialMode="align"
        data={{ anchor_comment_id: "comment-source", parent_issue_id: "parent-source" }}
      />,
    );

    expect(draftStore.setAlign).toHaveBeenCalledWith({
      request: "MUL-9\n\n> the toggle flickers",
    });
    expect(draftStore.setAlign).toHaveBeenCalledTimes(1);
  });

  it("does not overwrite a request the user already typed", () => {
    draftStore.alignRequest = "my own sentence";
    sourcePreview.current = {
      capture_token: "sha256:preview-token",
      anchor_comment_id: "comment-source",
      source_issue: { id: "i-1", identifier: "MUL-9" },
      comment_thread: [{ id: "comment-source", content: "the toggle flickers" }],
    };
    render(
      <CreateIssueDialog
        onClose={vi.fn()}
        initialMode="align"
        data={{ anchor_comment_id: "comment-source", parent_issue_id: "parent-source" }}
      />,
    );

    expect(draftStore.setAlign).not.toHaveBeenCalled();
  });
});

/**
 * DENE-370: the alignment face is a MODE of this dialog, not a second one. The
 * shell owning the single DialogContent is the whole point — switching to it
 * must swap only the inner panel, or the open animation replays on every flip.
 */
describe("CreateIssueDialog mode switching", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("swaps only the inner panel when switching to the alignment face", () => {
    render(<CreateIssueDialog onClose={vi.fn()} initialMode="manual" />);
    const content = screen.getByTestId("dialog-content");
    expect(content.className).toContain("manual-dialog-class");

    fireEvent.click(screen.getByRole("button", { name: "switch align" }));

    // Same DOM node: the Portal/Backdrop/Popup stayed mounted, so Base UI
    // never replays the open animation on a mode flip.
    expect(screen.getByTestId("dialog-content")).toBe(content);
    expect(contentClass()).toContain("align-dialog-class");
    expect(screen.getByText(/align panel/)).toBeInTheDocument();
  });

  it("never remembers the alignment face as the filing preference", () => {
    render(<CreateIssueDialog onClose={vi.fn()} initialMode="manual" />);

    fireEvent.click(screen.getByRole("button", { name: "switch align" }));
    expect(mockSetLastMode).not.toHaveBeenCalled();

    // A switch back to a filing face still records the preference.
    fireEvent.click(screen.getByRole("button", { name: "switch manual from align" }));
    expect(screen.getByText(/manual panel · ordinary/)).toBeInTheDocument();
    expect(mockSetLastMode).toHaveBeenCalledWith("manual");
  });

  it("opens directly on the alignment face when the registry asks for it", () => {
    render(<CreateIssueDialog onClose={vi.fn()} initialMode="align" data={null} />);

    expect(screen.getByText(/align panel/)).toBeInTheDocument();
    expect(mockSetLastMode).not.toHaveBeenCalled();
  });
});

/**
 * DENE-421: an unsent create draft does not outlive the dialog. Reopening any
 * of the three faces starts blank — before a submit, and before the alignment
 * face has opened a conversation, what was typed is scratch. The shell owns
 * this rather than each panel: Esc, the backdrop and every panel's own cancel
 * all land on the same `onClose`, and a rule enforced in three places is a rule
 * that holds in two.
 */
describe("CreateIssueDialog draft lifetime", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("clears the draft before handing the close up", () => {
    const onClose = vi.fn();
    render(<CreateIssueDialog onClose={onClose} initialMode="align" data={null} />);

    fireEvent.click(screen.getByRole("button", { name: "close from align" }));

    expect(mockClearDraft).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("keeps the draft across a mode switch within one open", () => {
    // The store still spans a switch: that is what stopped a manual body and an
    // agent prompt from destroying each other (MUL-5181). Only the close clears.
    render(<CreateIssueDialog onClose={vi.fn()} initialMode="manual" />);

    fireEvent.click(screen.getByRole("button", { name: "switch align" }));
    fireEvent.click(screen.getByRole("button", { name: "switch manual from align" }));

    expect(mockClearDraft).not.toHaveBeenCalled();
  });
});
