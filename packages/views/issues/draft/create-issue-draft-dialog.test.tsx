import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type { IssueDraftPayload, RuntimeDevice } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";
import { CreateIssueDraftDialog } from "./create-issue-draft-dialog";

/**
 * The entry dialog's contract is narrow on purpose: open the conversation, hand
 * the request over, leave. It must never create an issue — and it must navigate
 * even when the first turn fails, because a draft the user cannot see is a
 * draft they will create again.
 */

interface CreateSessionInput {
  runtime_id: string;
  model?: string;
  draft?: Partial<IssueDraftPayload>;
}

const mocks = vi.hoisted(() => ({
  drafts: [] as unknown[],
  runtimes: [] as unknown[],
  createIssueDraftSession: vi.fn(),
  sendChatMessage: vi.fn(),
  listIssueDrafts: vi.fn(),
  listRuntimes: vi.fn(),
  listMembers: vi.fn(),
  push: vi.fn(),
  close: vi.fn(),
}));

vi.mock("@multica/core/api", async () => {
  const actual =
    await vi.importActual<typeof import("@multica/core/api")>("@multica/core/api");
  return {
    ...actual,
    api: {
      createIssueDraftSession: mocks.createIssueDraftSession,
      sendChatMessage: mocks.sendChatMessage,
      listIssueDrafts: mocks.listIssueDrafts,
      listRuntimes: mocks.listRuntimes,
      listMembers: mocks.listMembers,
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
    newIssueDraft: (id: string) => `/acme/issues/new/${id}`,
    runtimes: () => "/acme/runtimes",
  }),
}));

vi.mock("../../navigation", () => ({
  useNavigation: () => ({
    push: mocks.push,
    replace: vi.fn(),
    pathname: "/acme/issues",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path: string) => path,
  }),
  AppLink: ({ href, children }: { href: string; children: React.ReactNode }) => (
    <a href={href}>{children}</a>
  ),
}));

// The real picker seeds an empty selection by calling `onSelect` itself, which
// is the only reason the submit button ever enables without a click. A mock
// that only renders a div would leave the dialog permanently unsubmittable and
// quietly test nothing.
vi.mock("../../agents/components/runtime-picker", async () => {
  const React = await import("react");
  return {
    RuntimePicker: ({
      onSelect,
      selectedRuntimeId,
      disabled,
    }: {
      onSelect: (id: string) => void;
      selectedRuntimeId: string;
      disabled?: boolean;
    }) => {
      React.useEffect(() => {
        if (selectedRuntimeId === "" && !disabled) onSelect("rt-1");
      }, [disabled, onSelect, selectedRuntimeId]);
      return <div data-testid="runtime-picker" data-selected={selectedRuntimeId} />;
    },
  };
});

// The unfinished-drafts entries have their own suite; here the dialog only has
// to route them.
vi.mock("./unfinished-issue-drafts", () => ({
  UnfinishedIssueDraftsBanner: ({
    drafts,
    onResume,
  }: {
    drafts: unknown[];
    onResume: (id: string) => void;
  }) =>
    drafts.length > 0 ? (
      <button type="button" onClick={() => onResume("sess-old")}>
        resume-unfinished
      </button>
    ) : null,
}));

const TEST_RESOURCES = { en: { common: enCommon, issues: enIssues } };

const ONLINE_RUNTIME = {
  id: "rt-1",
  name: "Local",
  status: "online",
  owner_id: "user-1",
  provider: "claude",
} as unknown as RuntimeDevice;

function renderDialog() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <CreateIssueDraftDialog onClose={mocks.close} />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

async function typeRequest(text: string) {
  const box = screen.getByPlaceholderText("What do you want done?");
  await waitFor(() => expect(screen.getByTestId("runtime-picker")).toBeTruthy());
  await userEvent.type(box, text);
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "Start aligning" })).toBeEnabled(),
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.drafts = [];
  mocks.runtimes = [ONLINE_RUNTIME];
  mocks.listIssueDrafts.mockImplementation(() => Promise.resolve(mocks.drafts));
  mocks.listRuntimes.mockImplementation(() => Promise.resolve(mocks.runtimes));
  mocks.listMembers.mockImplementation(() => Promise.resolve([]));
  mocks.createIssueDraftSession.mockResolvedValue({
    session_id: "sess-new",
    agent_id: "agent-1",
    runtime_id: "rt-1",
    draft: {
      chat_session_id: "sess-new",
      workspace_id: "ws-1",
      status: "draft",
      revision: 1,
      draft: { title: "", description: "add dark mode", status: "", priority: "" },
      created_at: "",
      updated_at: "",
    },
  });
  mocks.sendChatMessage.mockResolvedValue({ message_id: "msg-1", task_id: "task-1" });
});

describe("CreateIssueDraftDialog", () => {
  it("opens a conversation for the request and creates no issue", async () => {
    renderDialog();
    await typeRequest("add dark mode");
    await userEvent.click(screen.getByRole("button", { name: "Start aligning" }));

    await waitFor(() => expect(mocks.createIssueDraftSession).toHaveBeenCalledTimes(1));
    const input = mocks.createIssueDraftSession.mock.calls[0]![0] as CreateSessionInput;
    expect(input.runtime_id).toBe("rt-1");
    // The idea is stored in the draft before anything is sent, so a lost first
    // turn costs the turn and not the request.
    expect(input.draft?.description).toBe("add dark mode");

    await waitFor(() => expect(mocks.sendChatMessage).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(mocks.push).toHaveBeenCalledWith("/acme/issues/new/sess-new"),
    );
    expect(mocks.close).toHaveBeenCalled();
  });

  it("still opens the conversation when the first turn could not be sent", async () => {
    mocks.sendChatMessage.mockRejectedValue(new Error("network down"));
    renderDialog();
    await typeRequest("add dark mode");
    await userEvent.click(screen.getByRole("button", { name: "Start aligning" }));

    // The draft exists and holds the request; staying here would only invite a
    // second, duplicate draft.
    await waitFor(() =>
      expect(mocks.push).toHaveBeenCalledWith("/acme/issues/new/sess-new"),
    );
  });

  it("refuses to start without a request", async () => {
    renderDialog();
    await waitFor(() => expect(screen.getByTestId("runtime-picker")).toBeTruthy());
    await waitFor(() =>
      expect(screen.getByTestId("runtime-picker").dataset.selected).toBe("rt-1"),
    );
    expect(screen.getByRole("button", { name: "Start aligning" })).toBeDisabled();
    expect(mocks.createIssueDraftSession).not.toHaveBeenCalled();
  });

  it("routes an unfinished draft to its conversation", async () => {
    mocks.drafts = [{ chat_session_id: "sess-old" }];
    renderDialog();
    await userEvent.click(await screen.findByRole("button", { name: "resume-unfinished" }));
    expect(mocks.push).toHaveBeenCalledWith("/acme/issues/new/sess-old");
    expect(mocks.close).toHaveBeenCalled();
  });
});
