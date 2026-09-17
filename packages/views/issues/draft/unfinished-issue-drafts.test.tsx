import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiError } from "@multica/core/api";
import { I18nProvider } from "@multica/core/i18n/react";
import type { IssueDraftSummary } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";
import {
  UnfinishedIssueDraftsBanner,
  issueDraftPreview,
  issueDraftTitle,
} from "./unfinished-issue-drafts";

const TEST_RESOURCES = { en: { common: enCommon, issues: enIssues } };

/**
 * The drafts list is the ONLY route back into an alignment conversation — its
 * carrier is a `kind='system'` agent, so it is absent from every chat list. A
 * row whose title or preview renders the carrier's wire format is a row nobody
 * can recognise, which is the same as no list at all.
 */

const mocks = vi.hoisted(() => ({
  abandonIssueDraft: vi.fn(),
}));

vi.mock("@multica/core/api", async () => {
  const actual =
    await vi.importActual<typeof import("@multica/core/api")>("@multica/core/api");
  return {
    ...actual,
    api: { abandonIssueDraft: mocks.abandonIssueDraft },
  };
});

function summary(overrides: Partial<IssueDraftSummary>): IssueDraftSummary {
  return {
    chat_session_id: "sess-1",
    workspace_id: "ws-1",
    status: "draft",
    revision: 1,
    draft: { title: "", description: "", status: "", priority: "" },
    issue_id: null,
    policy: { key: "question", version: "1", guided: true },
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    title: "Align a new issue",
    runtime_id: "rt-1",
    last_message_content: "",
    last_message_role: "",
    last_message_at: "",
    ...overrides,
  };
}

/** What the abandon endpoint answers with: the same row, now terminal. */
function abandoned(id: string): unknown {
  return {
    chat_session_id: id,
    workspace_id: "ws-1",
    status: "abandoned",
    revision: 2,
    draft: { title: "Dark mode", description: "", status: "", priority: "" },
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

function renderBanner(props: {
  drafts: IssueDraftSummary[];
  onResume?: (id: string) => void;
}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const onResume = props.onResume ?? (() => {});
  const tree = (drafts: IssueDraftSummary[]) => (
    <QueryClientProvider client={client}>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <UnfinishedIssueDraftsBanner
          wsId="ws-1"
          drafts={drafts}
          onResume={onResume}
        />
      </I18nProvider>
    </QueryClientProvider>
  );
  const view = render(tree(props.drafts));
  // The list is React Query data in production, so a successful abandon
  // reaches the banner as the parent re-rendering with one row fewer — never
  // as a local removal here.
  return { ...view, showDrafts: (drafts: IssueDraftSummary[]) => view.rerender(tree(drafts)) };
}

/** The list is behind the banner; every row-level spec starts by opening it. */
async function openList() {
  await userEvent.click(screen.getByRole("button", { name: /unfinished alignment/ }));
}

function discardButton(title: string) {
  return screen.getByRole("button", { name: `Discard alignment: ${title}` });
}

function confirmDialog() {
  return screen.getByRole("alertdialog");
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.abandonIssueDraft.mockImplementation((id: string) =>
    Promise.resolve(abandoned(id)),
  );
});

describe("issueDraftTitle", () => {
  it("uses the draft's own title, not the constant chat session title", () => {
    // Every carrier session is titled "Align a new issue"; a list of those is
    // unreadable, so the structured title is the only candidate worth showing.
    expect(
      issueDraftTitle(summary({ draft: { title: "Dark mode", description: "", status: "", priority: "" } })),
    ).toBe("Dark mode");
    expect(issueDraftTitle(summary({}))).toBe("");
  });
});

describe("issueDraftPreview", () => {
  it("decodes the user's own turn out of the wire envelope", () => {
    const content =
      'MULTICA_ISSUE_DRAFT_INPUT\n{"user_request":"add dark mode","current_draft":{"title":"","description":"","status":"","priority":""}}';
    expect(
      issueDraftPreview(summary({ last_message_role: "user", last_message_content: content })),
    ).toBe("add dark mode");
  });

  it("strips the carrier's draft block from an assistant turn", () => {
    expect(
      issueDraftPreview(
        summary({
          last_message_role: "assistant",
          last_message_content:
            'Which surfaces?\n<issue_draft>{"title":"Dark mode"}</issue_draft>',
        }),
      ),
    ).toBe("Which surfaces?");
  });
});

describe("UnfinishedIssueDraftsBanner", () => {
  const one = summary({ chat_session_id: "sess-1", draft: { title: "Dark mode", description: "", status: "", priority: "" } });

  it("renders nothing when there is no unfinished draft", () => {
    renderBanner({ drafts: [] });
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("names the draft by its structured title", () => {
    renderBanner({ drafts: [one] });
    expect(screen.getByRole("button", { name: /1 unfinished alignment/ })).toBeTruthy();
  });

  /**
   * The one-row shortcut ("a chooser listing one item is a question with one
   * answer") is gone: the row is now also where an unwanted alignment is
   * discarded, so jumping straight in would hide that action in exactly the
   * case that has no other way to reach it.
   */
  it("opens the list for a single unfinished alignment instead of resuming it", async () => {
    const onResume = vi.fn();
    renderBanner({ drafts: [one], onResume });

    await openList();

    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(screen.getByText("Unfinished alignments")).toBeTruthy();
    expect(onResume).not.toHaveBeenCalled();
  });

  it("still resumes the alignment when its row is clicked", async () => {
    const onResume = vi.fn();
    renderBanner({ drafts: [one], onResume });

    await openList();
    await userEvent.click(screen.getByText("Dark mode"));

    expect(onResume).toHaveBeenCalledWith("sess-1");
  });

  it("discards an alignment only after the confirmation", async () => {
    renderBanner({ drafts: [one] });

    await openList();
    await userEvent.click(discardButton("Dark mode"));

    // The confirmation is where the semantics are stated: abandon, not a
    // physical delete, and what the alignment already created survives.
    const confirm = confirmDialog();
    expect(within(confirm).getByText("Discard this alignment?")).toBeTruthy();
    expect(
      within(confirm).getByText(/archived as abandoned and stays in the alignment records/),
    ).toBeTruthy();
    expect(mocks.abandonIssueDraft).not.toHaveBeenCalled();

    await userEvent.click(within(confirm).getByRole("button", { name: "Discard" }));

    await waitFor(() =>
      expect(mocks.abandonIssueDraft).toHaveBeenCalledWith("sess-1"),
    );
  });

  it("abandons nothing when the confirmation is cancelled", async () => {
    const onResume = vi.fn();
    renderBanner({ drafts: [one], onResume });

    await openList();
    await userEvent.click(discardButton("Dark mode"));
    // Guarded against the bug this test exists for: the discard trigger sits
    // inside the row, whose own click resumes.
    expect(onResume).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: "Keep aligning" }));

    expect(mocks.abandonIssueDraft).not.toHaveBeenCalled();
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  /**
   * DENE-422: a refused write says why, in the server's own words. A 4xx
   * message is written for the reader; collapsing every refusal into one
   * sentence is what made the earlier failure unreportable from a screenshot.
   */
  it("shows the server's reason when discarding is refused", async () => {
    mocks.abandonIssueDraft.mockRejectedValue(
      new ApiError("draft is already abandoned", 409, "Conflict"),
    );
    renderBanner({ drafts: [one] });

    await openList();
    await userEvent.click(discardButton("Dark mode"));
    await userEvent.click(
      within(confirmDialog()).getByRole("button", { name: "Discard" }),
    );

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("draft is already abandoned");
    expect(alert).not.toHaveTextContent("Could not discard the draft.");
    // Still up: the reason belongs to the write the user just asked for.
    expect(screen.getByRole("alertdialog")).toBeTruthy();
  });

  it("falls back to the localized sentence when the failure says nothing usable", async () => {
    mocks.abandonIssueDraft.mockRejectedValue(
      new ApiError('pq: relation "issue_draft_sessions" does not exist', 500, "Internal Server Error"),
    );
    renderBanner({ drafts: [one] });

    await openList();
    await userEvent.click(discardButton("Dark mode"));
    await userEvent.click(
      within(confirmDialog()).getByRole("button", { name: "Discard" }),
    );

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Could not discard the draft.");
    expect(alert).not.toHaveTextContent("issue_draft_sessions");
  });

  it("names the row each discard trigger belongs to", async () => {
    const second = summary({
      chat_session_id: "sess-2",
      draft: { title: "Mailbox", description: "", status: "", priority: "" },
    });
    renderBanner({ drafts: [one, second] });

    await openList();
    await userEvent.click(discardButton("Mailbox"));
    await userEvent.click(
      within(confirmDialog()).getByRole("button", { name: "Discard" }),
    );

    await waitFor(() =>
      expect(mocks.abandonIssueDraft).toHaveBeenCalledWith("sess-2"),
    );
  });

  /**
   * The last one takes the whole entry point with it: the list closes and the
   * banner stops rendering, because there is nothing left to offer. Both fall
   * out of the emptied list rather than out of a local removal — a row dropped
   * client-side would hide a server that refused.
   */
  it("closes the list and disappears when the last alignment is discarded", async () => {
    const view = renderBanner({ drafts: [one] });

    await openList();
    await userEvent.click(discardButton("Dark mode"));
    await userEvent.click(
      within(confirmDialog()).getByRole("button", { name: "Discard" }),
    );
    await waitFor(() =>
      expect(mocks.abandonIssueDraft).toHaveBeenCalledWith("sess-1"),
    );

    view.showDrafts([]);

    expect(screen.queryByRole("button", { name: /unfinished alignment/ })).toBeNull();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });
});
