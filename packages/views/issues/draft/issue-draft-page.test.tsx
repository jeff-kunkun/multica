import { describe, expect, it, vi, beforeEach } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
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
  switchIssueDraftPolicy: vi.fn(),
  sendChatMessage: vi.fn(),
  push: vi.fn(),
  replace: vi.fn(),
  transcriptProps: {} as { transformContent?: (content: string) => string },
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
      switchIssueDraftPolicy: mocks.switchIssueDraftPolicy,
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
    uploadEnabled,
  }: {
    onSend: (
      content: string,
      ids: string[] | undefined,
      commit: () => void,
    ) => void;
    disabled?: boolean;
    uploadEnabled?: boolean;
  }) => (
    <div data-upload-enabled={uploadEnabled === true ? "yes" : "no"}>
      <button
        type="button"
        disabled={disabled}
        onClick={() => onSend("please continue", undefined, () => {})}
      >
        send-turn
      </button>
      <button
        type="button"
        disabled={disabled}
        onClick={() =>
          onSend("look at this", ["att-1", "att-2"], () => {})
        }
      >
        send-turn-with-files
      </button>
    </div>
  ),
}));

vi.mock("../../chat/components/chat-message-list", () => ({
  ChatMessageList: ({
    messages,
    transformContent,
  }: {
    messages: { id: string; content: string }[];
    transformContent?: (content: string) => string;
  }) => {
    mocks.transcriptProps.transformContent = transformContent;
    return (
      <div data-testid="transcript">
        {messages.map((message) => (
          <p key={message.id} data-testid="transcript-row">
            {transformContent ? transformContent(message.content) : message.content}
          </p>
        ))}
      </div>
    );
  },
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
  mocks.transcriptProps = {};
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

  it("strips the machine blocks on the way to the transcript, question block included", async () => {
    // A settled reply is drawn from the carrier's task transcript, not from the
    // message body the page already stripped, so the strip has to travel down
    // to the list as well — otherwise the raw block comes back in the bubble
    // (DENE-317). The shapes that strip has to survive are canonical in
    // packages/core/issue-drafts/protocol.test.ts; this is the wiring.
    renderPage();
    await waitFor(() => expect(mocks.transcriptProps.transformContent).toBeTypeOf("function"));
    const transform = mocks.transcriptProps.transformContent!;
    expect(transform('两处已落进草稿。\n\n<issue_draft>{"title":"T"}</issue_draft>')).toBe(
      "两处已落进草稿。",
    );
    expect(
      transform('Who runs it?\n<issue_draft_question>{"question":"Who runs it?"}</issue_draft_question>'),
    ).toBe("Who runs it?");
  });

  it("sends every follow-up turn as an envelope carrying the current draft", async () => {
    // The carrier is told to "preserve good existing draft fields supplied in
    // the user's message". A turn sent as bare text supplies none, so the next
    // reply rebuilds the draft from that one message and drops what was already
    // agreed — including anything the user edited by hand in the preview.
    mocks.sendChatMessage.mockResolvedValue({ message_id: "m9", task_id: "t9" });
    renderPage();
    const send = await screen.findByRole("button", { name: "send-turn" });
    await userEvent.click(send);
    await waitFor(() => expect(mocks.sendChatMessage).toHaveBeenCalledTimes(1));
    expect(mocks.sendChatMessage).toHaveBeenCalledWith(
      "sess-1",
      'MULTICA_ISSUE_DRAFT_INPUT\n{"user_request":"please continue","current_draft":{"title":"Dark mode","description":"Add it.","status":"","priority":""}}',
      // A text-only turn still carries no attachment list, so the envelope the
      // carrier receives is byte-for-byte what it was before uploads existed.
      undefined,
    );
  });

  it("passes the composer's attachment ids through to the chat transport", async () => {
    // The composer uploads to the workspace and hands back ids; dropping them
    // is what made the alignment unable to receive a single screenshot
    // (DENE-369).
    mocks.sendChatMessage.mockResolvedValue({
      message_id: "m9",
      task_id: "t9",
      attachment_ids: ["att-1", "att-2"],
    });
    renderPage();
    const send = await screen.findByRole("button", { name: "send-turn-with-files" });
    await userEvent.click(send);
    await waitFor(() => expect(mocks.sendChatMessage).toHaveBeenCalledTimes(1));
    expect(mocks.sendChatMessage.mock.calls[0]?.[2]).toEqual(["att-1", "att-2"]);
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("reports the turn when the server binds fewer attachments than were sent", async () => {
    // The message landed, so this is not a send failure — but the carrier is
    // about to answer without the file, and saying nothing would make that look
    // like the model ignoring it.
    mocks.sendChatMessage.mockResolvedValue({
      message_id: "m9",
      task_id: "t9",
      attachment_ids: ["att-1"],
    });
    renderPage();
    await userEvent.click(
      await screen.findByRole("button", { name: "send-turn-with-files" }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Message sent, but the files were not attached.",
    );
  });

  it("does not false-alarm on a server that predates attachment_ids", async () => {
    mocks.sendChatMessage.mockResolvedValue({ message_id: "m9", task_id: "t9" });
    renderPage();
    await userEvent.click(
      await screen.findByRole("button", { name: "send-turn-with-files" }),
    );
    await waitFor(() => expect(mocks.sendChatMessage).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("offers uploads while the runtime is online", async () => {
    renderPage();
    await waitFor(() =>
      expect(
        document.querySelector('[data-upload-enabled="yes"]'),
      ).toBeTruthy(),
    );
  });

  it("withdraws the upload affordance while the runtime is offline", async () => {
    mocks.runtimes = [{ ...ONLINE_RUNTIME, status: "offline" } as RuntimeDevice];
    renderPage();
    await waitFor(() =>
      expect(document.querySelector('[data-upload-enabled="no"]')).toBeTruthy(),
    );
  });

  it("still offers the structured preview once the draft is ready", async () => {
    // A converged draft the user keeps refining produces new carrier blocks;
    // with this disabled, retyping them by hand is the only way to apply them.
    mocks.drafts = [draftSummary({ status: "ready" })];
    renderPage();
    await screen.findByText("Ready");
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: /Generate preview/i }),
      ).toBeEnabled(),
    );
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

  it("lands on the created issue when one tick sends the confirm twice", async () => {
    // Two presses dispatched in a single task — a scripted double click, which
    // is how the acceptance run recorded two finalize POSTs in the same
    // millisecond. Both answer with the same issue, and the completed draft
    // then leaves the unfinished list, so the page has to navigate once and
    // stay landed: a page that does not is left on the alignment URL showing
    // "this alignment has finished" (DENE-317).
    mocks.drafts = [draftSummary({ status: "ready" })];
    mocks.finalizeIssueDraft.mockResolvedValue({
      draft: draftSummary({ status: "completed", issue_id: "issue-9" }),
      issue_id: "issue-9",
    });
    // The confirm retires the draft, so the follow-up refetch re-renders the
    // page with no row while the navigation is still in flight. It lands a beat
    // later on purpose: the render that still has the row is what the first
    // replace comes from, and the one without it is the render that used to
    // re-issue that replace.
    mocks.listIssueDrafts.mockImplementation(() =>
      mocks.finalizeIssueDraft.mock.calls.length > 0
        ? new Promise((resolve) => setTimeout(() => resolve([]), 20))
        : Promise.resolve(mocks.drafts),
    );
    renderPage();
    const confirm = await screen.findByRole("button", { name: /Confirm and create/ });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Dark mode" })).toBeTruthy());
    await waitFor(() => expect(confirm).toBeEnabled());

    await act(async () => {
      // Raw dispatches: RTL's fireEvent wraps each one in `act`, which flushes
      // the disabled button between them and hides the very path under test.
      confirm.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
      confirm.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });
    expect(mocks.finalizeIssueDraft).toHaveBeenCalledTimes(2);

    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith("/acme/issues/issue-9"));
    // The row is gone from the list: this is the re-render that used to
    // re-issue the replace, because the effect's `paths` dependency is rebuilt
    // on every render and a router asked to replace the same URL forever never
    // commits. The header falls back to the unnamed-draft placeholder.
    await waitFor(() =>
      expect(screen.getByRole("heading", { name: "Align a new issue" })).toBeTruthy(),
    );
    await act(async () => {});
    expect(
      mocks.replace.mock.calls.filter(([path]) => path === "/acme/issues/issue-9"),
    ).toHaveLength(1);
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

/**
 * The alignment policy is the carrier's prompt, made switchable and auditable.
 * What these pin: the control reflects what the server says is running (not what
 * was clicked), the recorded prompt version is on screen, and a carrier question
 * is answerable in one click — with the composed answer going out through the
 * same envelope every other turn uses.
 */
describe("IssueDraftPage policy", () => {
  it("shows which policy is running, with the prompt version it recorded", async () => {
    renderPage();
    expect(await screen.findByRole("button", { name: "Guided questions" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Guided questions" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.getByText("Prompt question@1")).toBeTruthy();
  });

  it("switches to plain dialogue through the server, not in local state", async () => {
    mocks.switchIssueDraftPolicy.mockResolvedValue({
      ...draftSummary({ policy: { key: "conversation", version: "1", guided: false } }),
    });
    renderPage();
    const plain = await screen.findByRole("button", { name: "Plain conversation" });
    await userEvent.click(plain);
    await waitFor(() =>
      expect(mocks.switchIssueDraftPolicy).toHaveBeenCalledWith("sess-1", {
        policy: "conversation",
      }),
    );
  });

  it("surfaces a refused switch and leaves the running policy on screen", async () => {
    mocks.switchIssueDraftPolicy.mockRejectedValue(new Error("stop the current reply first"));
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "Plain conversation" }));
    expect(await screen.findByText("stop the current reply first")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Guided questions" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  it("offers no control at all when the backend reports no policy", async () => {
    // An installed desktop client can talk to a backend that predates policies;
    // a switch that cannot land is worse than no switch.
    mocks.drafts = [draftSummary({ policy: { key: "", version: "", guided: false } })];
    renderPage();
    await screen.findByText("Aligning");
    expect(screen.queryByRole("button", { name: "Plain conversation" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Guided questions" })).toBeNull();
  });
});

describe("IssueDraftPage questions", () => {
  const questionReply =
    'Who should run it?\n<issue_draft_question>{"question":"Who should run it?","options":[{"label":"A bot","value":"Assign a bot","recommended":true},{"label":"Nobody yet","value":"Leave it unassigned"}]}</issue_draft_question>\n<issue_draft>{"title":"Dark mode"}</issue_draft>';

  it("renders the open question with its recommended answer marked", async () => {
    mocks.messages = [chatMessage({ id: "m2", content: questionReply })];
    renderPage();
    // Twice on purpose: the carrier's own prose and the answer card, which is
    // what makes the question answerable even when the prose omits it.
    await waitFor(() =>
      expect(screen.getAllByText("Who should run it?").length).toBeGreaterThan(0),
    );
    expect(screen.getByRole("button", { name: /A bot/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /A bot/ })).toBeTruthy();
    expect(screen.getByText("Recommended")).toBeTruthy();
    // The block itself must never reach the transcript.
    expect(screen.queryByText(/issue_draft_question/)).toBeNull();
  });

  it("sends the clicked option as the user's own answer", async () => {
    mocks.messages = [chatMessage({ id: "m2", content: questionReply })];
    mocks.sendChatMessage.mockResolvedValue({ message_id: "m9", task_id: "t9" });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Nobody yet/ }));
    await waitFor(() => expect(mocks.sendChatMessage).toHaveBeenCalledTimes(1));
    const wire = mocks.sendChatMessage.mock.calls[0]?.[1];
    expect(wire).toContain('"user_request":"Leave it unassigned"');
  });

  it("keeps the chips out of a plain-dialogue conversation", async () => {
    // The unguided policy does not interview, so a stray block must not turn
    // the page back into a questionnaire.
    mocks.drafts = [
      draftSummary({ policy: { key: "conversation", version: "1", guided: false } }),
    ];
    mocks.messages = [chatMessage({ id: "m2", content: questionReply })];
    renderPage();
    await screen.findByText("Aligning");
    expect(screen.queryByText("Who should run it?")).toBeNull();
  });
});

describe("IssueDraftPage draft persistence", () => {
  it("writes the carrier's proposal to the server draft as the conversation goes", async () => {
    // The draft is what finalize reads. A proposal that only exists in the
    // browser until someone presses a button is one refresh away from gone.
    mocks.messages = [
      chatMessage({
        id: "m2",
        content: 'Sure.\n<issue_draft>{"title":"Dark mode","priority":"high"}</issue_draft>',
      }),
    ];
    mocks.updateIssueDraft.mockImplementation(
      (_draftId: string, data: { draft: IssueDraftSummary["draft"] }) =>
        Promise.resolve({
          ...draftSummary({ revision: 4 }),
          draft: data.draft,
        }),
    );
    renderPage();
    await waitFor(() => expect(mocks.updateIssueDraft).toHaveBeenCalledTimes(1));
    expect(mocks.updateIssueDraft.mock.calls[0]?.[1]).toMatchObject({
      draft: { title: "Dark mode", description: "Add it.", priority: "high" },
      status: "draft",
      expected_revision: 3,
    });
  });
});
