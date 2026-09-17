import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type { IssueDraftSummary } from "@multica/core/types";
import {
  NavigationProvider,
  type NavigationAdapter,
} from "../../navigation";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";
import { AlignmentRecords } from "./alignment-records";

/**
 * The chat sidebar's alignment group.
 *
 * What this group must not become is a place where ordinary conversations pick
 * up an alignment label. That is structural: the only rows it can render come
 * from the issue-draft endpoint, so a chat session cannot reach it at all. The
 * cases below pin the other half — that a FINISHED alignment does reach it,
 * because a conversation hidden behind a system carrier is reachable from
 * nowhere else — and that each row opens the alignment it names.
 */

const mocks = vi.hoisted(() => ({
  drafts: [] as unknown[],
  listIssueDrafts: vi.fn(),
}));

vi.mock("@multica/core/api", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/api")>("@multica/core/api");
  return { ...actual, api: { listIssueDrafts: mocks.listIssueDrafts } };
});

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    newIssueDraft: (id: string) => `/acme/issues/new/${id}`,
    issueDetail: (id: string) => `/acme/issues/${id}`,
  }),
}));

const TEST_RESOURCES = { en: { common: enCommon, issues: enIssues } };

function draft(overrides: Partial<IssueDraftSummary> = {}): IssueDraftSummary {
  return {
    chat_session_id: "sess-1",
    workspace_id: "ws-1",
    status: "draft",
    revision: 3,
    draft: { title: "Dark mode", description: "Add it.", status: "", priority: "" },
    issue_id: null,
    policy: { key: "question", version: "1", guided: true },
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    title: "Align a new issue",
    runtime_id: "rt-1",
    last_message_content: "add dark mode",
    last_message_role: "user",
    last_message_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

function renderGroup() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  // The rows are AppLinks, so the group needs the same navigation context the
  // chat sidebar renders them inside.
  const navigation: NavigationAdapter = {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/chat",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path) => path,
  };
  return render(
    <QueryClientProvider client={client}>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <NavigationProvider value={navigation}>
          <AlignmentRecords wsId="ws-1" />
        </NavigationProvider>
      </I18nProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.drafts = [];
  mocks.listIssueDrafts.mockImplementation(() => Promise.resolve(mocks.drafts));
});

describe("AlignmentRecords", () => {
  it("lists a finished alignment and opens the conversation it names", async () => {
    mocks.drafts = [
      draft({
        chat_session_id: "sess-done",
        status: "completed",
        issue_id: "issue-9",
        draft: { title: "Ship dark mode", description: "", status: "", priority: "" },
      }),
    ];
    renderGroup();

    const row = await screen.findByRole("link", { name: /Ship dark mode/ });
    expect(row.getAttribute("href")).toBe("/acme/issues/new/sess-done");
    expect(screen.getByText("Created")).toBeTruthy();
  });

  it("keeps an unfinished alignment distinguishable from a finished one", async () => {
    mocks.drafts = [
      draft({ chat_session_id: "sess-open", status: "ready" }),
      draft({ chat_session_id: "sess-dropped", status: "abandoned" }),
    ];
    renderGroup();

    expect(await screen.findByText("In progress")).toBeTruthy();
    expect(screen.getByText("Abandoned")).toBeTruthy();
    expect(screen.queryByText("Created")).toBeNull();
  });

  it("stays out of the way when this user has never aligned anything", async () => {
    mocks.drafts = [];
    const { container } = renderGroup();

    // Settle the fetch first, so "nothing rendered" is a decision about an
    // empty list rather than about a query still in flight.
    await waitFor(() => expect(mocks.listIssueDrafts).toHaveBeenCalled());
    await waitFor(() => expect(container.querySelector("section")).toBeNull());
    // No rows, no heading furniture either: an empty section would be permanent
    // chrome describing an absence.
    expect(screen.queryByText("Alignments")).toBeNull();
    expect(screen.queryByRole("link")).toBeNull();
  });

  it("decodes the wire format instead of showing the stored envelopes", async () => {
    // Both sides of an alignment are machine-readable: the user's own turn is a
    // JSON envelope carrying the draft, the carrier's ends in an <issue_draft>
    // block. Shown raw this list is a wall of JSON.
    mocks.drafts = [
      draft({
        last_message_role: "user",
        last_message_content:
          'MULTICA_ISSUE_DRAFT_INPUT\n{"user_request":"add dark mode","current_draft":{"title":"Dark mode","description":"Add it.","status":"","priority":""}}',
      }),
    ];
    renderGroup();

    expect(await screen.findByText("add dark mode")).toBeTruthy();
    expect(screen.queryByText(/MULTICA_ISSUE_DRAFT_INPUT/)).toBeNull();
  });
});
