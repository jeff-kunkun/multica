import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { ApiError } from "@multica/core/api";
import type { ChatMessage, IssueDraftSummary, RuntimeDevice } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";
import { IssueDraftPage } from "./issue-draft-page";

/**
 * What this page must never do is create an issue before the user confirms —
 * that is the entire reason the alignment step exists. The rest of the suite
 * pins the states around that boundary: the four stages, a confirm that is
 * refused, and a draft that survives its own failed confirm.
 */

const mocks = vi.hoisted(() => ({
  drafts: [] as unknown[],
  draftsError: null as unknown,
  messages: [] as unknown[],
  messagesError: null as unknown,
  pendingTaskId: null as string | null,
  runtimes: [] as unknown[],
  listIssueDrafts: vi.fn(),
  listChatMessages: vi.fn(),
  getPendingChatTask: vi.fn(),
  listRuntimes: vi.fn(),
  listMembers: vi.fn(),
  updateIssueDraft: vi.fn(),
  finalizeIssueDraft: vi.fn(),
  abandonIssueDraft: vi.fn(),
  switchIssueDraftRuntime: vi.fn(),
  sendChatMessage: vi.fn(),
  push: vi.fn(),
  replace: vi.fn(),
}));

vi.mock("@multica/core/api", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/api")>("@multica/core/api");
  return {
    ...actual,
    api: {
      listIssueDrafts: mocks.listIssueDrafts,
      listChatMessages: mocks.listChatMessages,
      getPendingChatTask: mocks.getPendingChatTask,
      listRuntimes: mocks.listRuntimes,
      listMembers: mocks.listMembers,
      updateIssueDraft: mocks.updateIssueDraft,
      finalizeIssueDraft: mocks.finalizeIssueDraft,
      abandonIssueDraft: mocks.abandonIssueDraft,
      switchIssueDraftRuntime: mocks.switchIssueDraftRuntime,
      sendChatMessage: mocks.sendChatMessage,
    },
  };
});

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string } }) => unknown) =>
    selector({ user: { id: "user-1" } }),
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    issues: () => "/acme/issues",
    issueDetail: (id: string) => `/acme/issues/${id}`,
    newIssueDraft: (id: string) => `/acme/issues/new/${id}`,
    runtimes: () => "/acme/runtimes",
  }),
}));

vi.mock("../../navigation", () => ({
  useNavigation: () => ({
    push: mocks.push,
    replace: mocks.replace,
    pathname: "/acme/issues/new/sess-1",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path: string) => path,
  }),
  useBackOrReplace: () => (fallback: string) => mocks.replace(fallback),
}));

// The chat surfaces, the pickers and the split layout are not what this suite
// is about; each is replaced by the smallest thing that keeps its contract.
vi.mock("../../chat/components/chat-input", () => ({
  ChatInput: ({
    onSend,
    disabled,
  }: {
    onSend: (content: string, ids: undefined, commit: () => void) => void;
    disabled?: boolean;
  }) => (
    <button
      type="button"
      disabled={disabled}
      onClick={() => onSend("please continue", undefined, () => {})}
    >
      send-turn
    </button>
  ),
}));

vi.mock("../../chat/components/chat-message-list", () => ({
  ChatMessageList: ({ messages }: { messages: { id: string; content: string }[] }) => (
    <div data-testid="transcript">
      {messages.map((message) => (
        <p key={message.id} data-testid="transcript-row">
          {message.content}
        </p>
      ))}
    </div>
  ),
  ChatMessageSkeleton: () => <div data-testid="transcript-loading" />,
}));

vi.mock("@multica/ui/components/ui/resizable", () => ({
  ResizablePanelGroup: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  ResizablePanel: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  ResizableHandle: () => <div />,
}));

vi.mock("../../agents/components/runtime-picker", () => ({
  RuntimePicker: () => <div data-testid="runtime-picker" />,
}));

vi.mock("../components/pickers/status-picker", () => ({
  StatusPicker: () => <div data-testid="status-picker" />,
}));

vi.mock("../components/pickers/priority-picker", () => ({
  PriorityPicker: () => <div data-testid="priority-picker" />,
}));

const TEST_RESOURCES = { en: { common: enCommon, issues: enIssues } };

function draftSummary(overrides: Partial<IssueDraftSummary> = {}): IssueDraftSummary {
  return {
    chat_session_id: "sess-1",
    workspace_id: "ws-1",
    status: "draft",
    revision: 3,
    draft: { title: "Dark mode", description: "Add it.", status: "", priority: "" },
    issue_id: null,
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

function chatMessage(overrides: Partial<ChatMessage> = {}): ChatMessage {
  return {
    id: "m1",
    chat_session_id: "sess-1",
    role: "assistant",
    content: "What does dark mode cover?",
    created_at: "2026-01-01T00:00:00Z",
    ...overrides,
  } as ChatMessage;
}

const ONLINE_RUNTIME = {
  id: "rt-1",
  name: "Local",
  status: "online",
  owner_id: "user-1",
  provider: "claude",
} as unknown as RuntimeDevice;

function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <IssueDraftPage draftId="sess-1" />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.drafts = [draftSummary()];
  mocks.draftsError = null;
  mocks.messages = [chatMessage()];
  mocks.messagesError = null;
  mocks.pendingTaskId = null;
  mocks.runtimes = [ONLINE_RUNTIME];
  mocks.listIssueDrafts.mockImplementation(() => {
    if (mocks.draftsError) return Promise.reject(mocks.draftsError);
    return Promise.resolve(mocks.drafts);
  });
  mocks.listChatMessages.mockImplementation(() => {
    if (mocks.messagesError) return Promise.reject(mocks.messagesError);
    return Promise.resolve(mocks.messages);
  });
  mocks.getPendingChatTask.mockImplementation(() =>
    Promise.resolve(mocks.pendingTaskId ? { task_id: mocks.pendingTaskId } : {}),
  );
  mocks.listRuntimes.mockImplementation(() => Promise.resolve(mocks.runtimes));
  mocks.listMembers.mockImplementation(() => Promise.resolve([]));
});

describe("IssueDraftPage stages", () => {
  it("shows 对齐中 while the draft is still being aligned", async () => {
    renderPage();
    expect(await screen.findByText("Aligning")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Confirm and create/ })).toBeDisabled();
  });

  it("offers the confirm only once the server says the draft is ready", async () => {
    mocks.drafts = [draftSummary({ status: "ready" })];
    renderPage();
    expect(await screen.findByText("Ready")).toBeTruthy();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /Confirm and create/ })).toBeEnabled(),
    );
  });

  it("does not offer a create for a ready draft with no title", async () => {
    // The server reads the title straight out of the draft at finalize, so a
    // blank one is a create it would refuse; the button must not promise it.
    mocks.drafts = [
      draftSummary({
        status: "ready",
        draft: { title: "  ", description: "d", status: "", priority: "" },
      }),
    ];
    renderPage();
    await screen.findByText("Ready");
    expect(screen.getByRole("button", { name: /Confirm and create/ })).toBeDisabled();
  });

  it("decodes both directions of the carrier's wire format in the transcript", async () => {
    mocks.messages = [
      chatMessage({
        id: "m1",
        role: "user",
        content:
          'MULTICA_ISSUE_DRAFT_INPUT\n{"user_request":"add dark mode","current_draft":{"title":"","description":"","status":"","priority":""}}',
      }),
      chatMessage({
        id: "m2",
        role: "assistant",
        content: 'Which surfaces?\n<issue_draft>{"title":"Dark mode"}</issue_draft>',
      }),
    ];
    renderPage();
    await waitFor(() => expect(screen.getAllByTestId("transcript-row")).toHaveLength(2));
    const rows = screen.getAllByTestId("transcript-row").map((row) => row.textContent);
    expect(rows).toEqual(["add dark mode", "Which surfaces?"]);
  });
});

describe("IssueDraftPage confirming", () => {
  it("creates nothing until confirm, then navigates to the issue it made", async () => {
    mocks.drafts = [draftSummary({ status: "ready" })];
    mocks.finalizeIssueDraft.mockResolvedValue({
      draft: draftSummary({ status: "completed", issue_id: "issue-9" }),
      issue_id: "issue-9",
    });
    renderPage();
    const confirm = await screen.findByRole("button", { name: /Confirm and create/ });
    await waitFor(() => expect(confirm).toBeEnabled());
    await userEvent.click(confirm);

    await waitFor(() => expect(mocks.finalizeIssueDraft).toHaveBeenCalledTimes(1));
    // The revision the user was looking at travels with the confirm: that is
    // what makes a confirm from a superseded view refuse instead of overwrite.
    expect(mocks.finalizeIssueDraft).toHaveBeenCalledWith("sess-1", {
      expected_revision: 3,
    });
    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith("/acme/issues/issue-9"));
  });

  it("sends one confirm for a double click, not two creates", async () => {
    mocks.drafts = [draftSummary({ status: "ready" })];
    let release: ((value: unknown) => void) | undefined;
    mocks.finalizeIssueDraft.mockImplementation(
      () => new Promise((resolve) => { release = resolve; }),
    );
    renderPage();
    const confirm = await screen.findByRole("button", { name: /Confirm and create/ });
    await waitFor(() => expect(confirm).toBeEnabled());

    await userEvent.click(confirm);
    // The second press lands while the first is still in flight. `isPending` is
    // what has to stop it — a local flag can drift from the request it names.
    await userEvent.click(confirm);
    expect(mocks.finalizeIssueDraft).toHaveBeenCalledTimes(1);

    release?.({
      draft: draftSummary({ status: "completed", issue_id: "issue-9" }),
      issue_id: "issue-9",
    });
    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith("/acme/issues/issue-9"));
  });

  it("keeps the draft on screen when finalize is refused", async () => {
    mocks.drafts = [draftSummary({ status: "ready" })];
    mocks.finalizeIssueDraft.mockRejectedValue(
      new ApiError(
        "this draft changed since you loaded it; reload and try again",
        409,
        "Conflict",
      ),
    );
    renderPage();
    const confirm = await screen.findByRole("button", { name: /Confirm and create/ });
    await waitFor(() => expect(confirm).toBeEnabled());
    await userEvent.click(confirm);

    expect(
      await screen.findByText("this draft changed since you loaded it; reload and try again"),
    ).toBeTruthy();
    expect(mocks.replace).not.toHaveBeenCalledWith(expect.stringContaining("/acme/issues/issue"));
  });

  it("keeps the draft when the issue was created but the body is unreadable", async () => {
    // No zod fallback on finalize: an unparseable 2xx must throw rather than
    // hand the router an empty id, and the retry is safe because the protocol
    // returns the same issue for every repeat.
    mocks.drafts = [draftSummary({ status: "ready" })];
    mocks.finalizeIssueDraft.mockRejectedValue(new Error("invalid finalize response"));
    renderPage();
    const confirm = await screen.findByRole("button", { name: /Confirm and create/ });
    await waitFor(() => expect(confirm).toBeEnabled());
    await userEvent.click(confirm);
    expect(await screen.findByText("invalid finalize response")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Confirm and create/ })).toBeTruthy();
  });
});

describe("IssueDraftPage loading", () => {
  it("shows a transcript skeleton while the conversation loads", async () => {
    mocks.messages = [];
    mocks.listChatMessages.mockImplementation(() => new Promise(() => {}));
    renderPage();
    expect(await screen.findByTestId("transcript-loading")).toBeTruthy();
  });
});

describe("IssueDraftPage recovery", () => {
  it("reads the draft back after a refresh instead of starting over", async () => {
    renderPage();
    // No local state seeds this: the title on screen came from the list fetch.
    expect(await screen.findByRole("heading", { name: "Dark mode" })).toBeTruthy();
  });

  it("says the alignment is finished when the draft is no longer unfinished", async () => {
    mocks.drafts = [];
    renderPage();
    expect(await screen.findByText("This alignment has finished")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Confirm and create/ })).toBeNull();
  });

  it("leaves when the conversation itself is gone", async () => {
    mocks.messagesError = new ApiError("API error: 404 Not Found", 404, "Not Found");
    renderPage();
    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith("/acme/issues"));
  });

  it("surfaces a failed list fetch with a retry instead of an empty page", async () => {
    mocks.draftsError = new ApiError("API error: 500 Internal Server Error", 500, "Internal Server Error");
    renderPage();
    expect(await screen.findByText("Could not load this alignment.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  });
});
