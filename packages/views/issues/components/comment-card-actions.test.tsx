import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, type RenderResult } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ComponentProps, ReactNode, Ref } from "react";
import type { TimelineEntry } from "@multica/core/types";
import { useCommentCollapseStore } from "@multica/core/issues/stores";
import { renderWithI18n } from "../../test/i18n";
import { CommentCard } from "./comment-card";

/**
 * The comment card's ⋯ menu.
 *
 * "Create sub-issue from here" turns the thread into work. It carries the
 * comment id it was opened from, which is what the opener turns into the
 * captured source.
 */

vi.mock("../../navigation", () => ({
  useNavigation: () => ({
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/issues",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (p: string) => `https://app.example${p}`,
  }),
}));

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: () => "Ada" }),
}));

vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: () => null,
}));

vi.mock("../hooks/use-comment-trigger-preview", () => ({
  useCommentTriggerPreview: () => ({ agents: [], blocked: [] }),
}));

// The menu is what this file is about; the editors and attachment surfaces
// nested inside the card have their own suites.
vi.mock("../../editor", async () => ({
  ...(await vi.importActual<typeof import("../../editor/use-upload-gate")>(
    "../../editor/use-upload-gate",
  )),
  ...(await vi.importActual<typeof import("../../editor/use-lazy-editor")>(
    "../../editor/use-lazy-editor",
  )),
  ...(await vi.importActual<typeof import("../../editor/use-composer-submit")>(
    "../../editor/use-composer-submit",
  )),
  useEditorUpload: () => ({ uploadWithToast: vi.fn(), upload: vi.fn(), uploading: false }),
  useFileDropZone: () => ({ isDragOver: false, dropZoneProps: {} }),
  FileDropOverlay: () => null,
  ReadonlyContent: ({ content }: { content: string }) => <div>{content}</div>,
  Attachment: () => null,
  AttachmentDownloadProvider: ({ children }: { children: ReactNode }) => <>{children}</>,
  ContentEditor: ({ placeholder }: { placeholder?: string }, ref: Ref<unknown>) => (
    <textarea data-testid="editor" placeholder={placeholder} ref={ref as never} />
  ),
}));

const entry = {
  id: "comment-1",
  issue_id: "issue-1",
  parent_id: null,
  actor_type: "member",
  actor_id: "user-1",
  content: "the toggle flickers",
  type: "comment",
  // The menu's entries read THIS field, not `type` — the timeline row carries
  // both, and a row whose `comment_type` is absent is not an ordinary comment.
  comment_type: "comment",
  created_at: "2026-07-01T00:00:00Z",
  updated_at: "2026-07-01T00:00:00Z",
  revision: 1,
  attachments: [],
  reactions: [],
} as unknown as TimelineEntry;

function renderCard(
  overrides: Partial<ComponentProps<typeof CommentCard>> = {},
): RenderResult {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <CommentCard
        issueId="issue-1"
        entry={entry}
        replies={[]}
        currentUserId="user-1"
        onReply={vi.fn().mockResolvedValue(true)}
        onEdit={vi.fn().mockResolvedValue(undefined)}
        onDelete={vi.fn()}
        onToggleReaction={vi.fn()}
        {...overrides}
      />
    </QueryClientProvider>,
  );
}

/** The row's ⋯ trigger is icon-only, so it is addressed by its own label. */
function openMenu(): HTMLElement {
  const trigger = screen.getByRole("button", { name: "Comment actions" });
  return trigger;
}

beforeEach(() => {
  vi.clearAllMocks();
  // Collapse state is a module-level store: a row another test collapsed stays
  // collapsed, and this card only renders its ⋯ menu on an expanded thread.
  useCommentCollapseStore.setState({ collapsedByIssue: {} });
});

describe("comment actions menu", () => {
  it("files a sub-issue from the thread, carrying the comment id", async () => {
    const onCreateSubIssue = vi.fn();
    renderCard({ onCreateSubIssue });

    fireEvent.click(openMenu());
    await screen.findByText("Create sub-issue from here");
    fireEvent.click(screen.getByText("Create sub-issue from here"));
    expect(onCreateSubIssue).toHaveBeenCalledWith("comment-1");
  });

  it("offers no sub-issue action where the card was not given one", async () => {
    // A surface that has no issue context to file under must not render an
    // entry that would have nowhere to go.
    renderCard();

    fireEvent.click(openMenu());
    // Something opened — otherwise the assertion below would pass on a menu
    // that never rendered at all.
    await screen.findByText("Copy");
    expect(screen.queryByText("Create sub-issue from here")).toBeNull();
  });
});
