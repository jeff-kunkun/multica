import { forwardRef, useImperativeHandle, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type { IssueDraftPayload, RuntimeDevice } from "@multica/core/types";
import enCommon from "../locales/en/common.json";
import enIssues from "../locales/en/issues.json";
import enModals from "../locales/en/modals.json";
import enEditor from "../locales/en/editor.json";
import enProjects from "../locales/en/projects.json";
import { AlignCreatePanel } from "./align-create-issue";

/**
 * The alignment face's contract is narrow on purpose: open the conversation,
 * hand the request over, leave. It must never create an issue — and it must
 * navigate even when the first turn fails, because a draft the user cannot see
 * is a draft they will create again.
 *
 * DENE-370 moved this face INSIDE the create-issue dialog. What that adds is
 * the shared input: the alignment request lives in the create draft's own
 * `align` slot, and its attachments ride the same pool "New issue" uses.
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
  setAlign: vi.fn(),
  setShared: vi.fn(),
  setActiveMode: vi.fn(),
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

// The upload pool is the SHARED create-dialog pool, so this face owns no
// uploads of its own — it renders what the draft store holds. Mocked in the
// Zustand callable-store shape the repo's tests use, with the writers recorded
// so a spec can prove a body edit lands in the align slot.
const draftStore = {
  draft: {
    shared: {
      projectId: undefined as string | undefined,
      priority: "none" as const,
      dueDate: null as string | null,
      attachments: [] as unknown[],
    },
    manual: { title: "", description: "" },
    agent: { prompt: "" },
    align: { request: "" },
    activeMode: "manual" as string,
  },
  setAlign: mocks.setAlign,
  setShared: mocks.setShared,
  setActiveMode: mocks.setActiveMode,
};

// The real store is a zustand subscription, so a write made through a picker
// re-renders the face that reads it. The mock has to do the same or a spec
// could never observe the picker's own update: it would assert against the
// render that preceded the click.
const draftStoreListeners = new Set<() => void>();
let draftStoreRevision = 0;
function subscribeDraftStore(listener: () => void) {
  draftStoreListeners.add(listener);
  return () => {
    draftStoreListeners.delete(listener);
  };
}
function getDraftStoreRevision() {
  return draftStoreRevision;
}
function writeDraftStore(next: typeof draftStore.draft) {
  draftStore.draft = next;
  draftStoreRevision += 1;
  draftStoreListeners.forEach((listener) => listener());
}

vi.mock("@multica/core/issues/stores", () => ({
  useIssueDraftStore: Object.assign(
    (selector?: (state: typeof draftStore) => unknown) => {
      useSyncExternalStore(subscribeDraftStore, getDraftStoreRevision);
      return selector ? selector(draftStore) : draftStore;
    },
    { getState: () => draftStore },
  ),
}));

vi.mock("../navigation", () => ({
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

// The project picker is a real component with its own suite; this face's
// contract is the WIRING around it — which draft slot the choice lands in, and
// what the first turn carries.
vi.mock("../projects/components/project-picker", () => ({
  ProjectPicker: ({
    projectId,
    onUpdate,
  }: {
    projectId: string | null;
    onUpdate: (updates: { project_id?: string | null }) => void;
  }) => (
    <button
      type="button"
      data-testid="project-picker"
      data-project-id={projectId ?? "none"}
      onClick={() => onUpdate({ project_id: "proj-1" })}
    >
      Choose project
    </button>
  ),
}));

// Pasting happens through the editor's own upload path, which the coordinator
// owns; the spec drives a file in through the footer button instead.
vi.mock("@multica/ui/components/common/file-upload-button", () => ({
  FileUploadButton: ({ onSelect }: { onSelect: (file: File) => void }) => (
    <button type="button" onClick={() => onSelect(new File(["test"], "test.txt"))}>
      Upload file
    </button>
  ),
}));

// The real editor is a Tiptap surface with its own suite; this face's contract
// is the WIRING around it — which draft slot the body lands in, which uploads
// it renders, and what the first turn carries. The mock keeps the real upload
// gate and the real handle shape (getMarkdown / uploadFile / hasActiveUploads)
// so the submit gate under test is production code, not a stub.
let mockUploadIdSeq = 0;

vi.mock("../editor", async () => {
  const uploadGate = await vi.importActual<
    typeof import("../editor/use-upload-gate")
  >("../editor/use-upload-gate");

  const ContentEditor = forwardRef(
    (
      {
        defaultValue,
        onUpdate,
        onSubmit,
        onUploadFile,
        onUploadingChange,
        placeholder,
        attachments,
      }: {
        defaultValue?: string;
        onUpdate?: (md: string) => void;
        onSubmit?: () => void;
        onUploadFile?: (file: File, uploadId: string) => Promise<unknown>;
        onUploadingChange?: (uploading: boolean) => void;
        placeholder?: string;
        attachments?: unknown[];
      },
      ref: React.Ref<unknown>,
    ) => {
      const valueRef = useRef(defaultValue ?? "");
      const [value, setValue] = useState(defaultValue ?? "");
      const inFlightRef = useRef(0);
      useImperativeHandle(ref, () => ({
        getMarkdown: () => valueRef.current,
        clearContent: () => {
          valueRef.current = "";
          setValue("");
        },
        focus: () => {},
        focusAtCoords: () => {},
        focusAtTextAnchor: () => {},
        uploadFile: async (file: File) => {
          inFlightRef.current += 1;
          if (inFlightRef.current === 1) onUploadingChange?.(true);
          try {
            return await onUploadFile?.(file, `mock-upload-${++mockUploadIdSeq}`);
          } finally {
            inFlightRef.current -= 1;
            if (inFlightRef.current === 0) onUploadingChange?.(false);
          }
        },
        hasActiveUploads: () => inFlightRef.current > 0,
        insertUploadPlaceholder: () => true,
        settleUploadPlaceholder: () => false,
      }));
      return (
        <textarea
          value={value}
          placeholder={placeholder}
          data-attachments-count={attachments?.length ?? 0}
          onChange={(e) => {
            valueRef.current = e.target.value;
            setValue(e.target.value);
            onUpdate?.(e.target.value);
          }}
          onKeyDown={(e) => {
            if ((e.metaKey || e.ctrlKey) && e.key === "Enter") onSubmit?.();
          }}
        />
      );
    },
  );
  ContentEditor.displayName = "ContentEditor";

  return {
    ...uploadGate,
    useFileDropZone: () => ({ isDragOver: false, dropZoneProps: {} }),
    FileDropOverlay: () => null,
    ContentEditor,
  };
});

// The face is a PANEL: it only ever renders inside the shell's Dialog root, so
// the sr-only title is stubbed here the way the sibling panel suites do.
vi.mock("@multica/ui/components/ui/dialog", () => ({
  DialogTitle: ({ children, className }: { children: ReactNode; className?: string }) => (
    <div className={className}>{children}</div>
  ),
}));

// The unfinished-drafts entries have their own suite; here the face only has
// to route them.
vi.mock("../issues/draft/unfinished-issue-drafts", () => ({
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

const TEST_RESOURCES = {
  en: {
    common: enCommon,
    issues: enIssues,
    modals: enModals,
    editor: enEditor,
    projects: enProjects,
  },
};

const ONLINE_RUNTIME = {
  id: "rt-1",
  name: "Local",
  status: "online",
  owner_id: "user-1",
  provider: "claude",
} as unknown as RuntimeDevice;

function renderPanel(props: {
  onClose?: () => void;
  onSwitchMode?: (carry?: Record<string, unknown> | null) => void;
} = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <AlignCreatePanel
          onClose={props.onClose ?? mocks.close}
          onSwitchMode={props.onSwitchMode}
        />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

function submitButton() {
  return screen.getByRole("button", { name: "Start aligning" });
}

function editor() {
  return screen.getByPlaceholderText("What do you want done?");
}

async function typeRequest(text: string) {
  await userEvent.type(editor(), text);
  await waitFor(() => expect(submitButton()).toBeEnabled());
}

function uploadedPool(filename = "x.png", url = "https://cdn/x.png") {
  return [
    {
      clientUploadId: "c-1",
      status: "uploaded",
      filename,
      size: 9,
      attachment: {
        id: "att-1",
        workspace_id: "ws-1",
        issue_id: null,
        comment_id: null,
        chat_session_id: null,
        chat_message_id: null,
        uploader_type: "member",
        uploader_id: "user-1",
        filename,
        url,
        download_url: url,
        markdown_url: url,
        content_type: "image/png",
        size_bytes: 9,
        created_at: "2026-01-01T00:00:00Z",
      },
    },
  ];
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
  draftStore.draft.shared.attachments = [];
  draftStore.draft.shared.projectId = undefined;
  draftStore.draft.align.request = "";
  mocks.setAlign.mockImplementation((patch: { request?: string }) => {
    draftStore.draft.align = { ...draftStore.draft.align, ...patch };
  });
  mocks.setShared.mockImplementation((patch: { projectId?: string }) => {
    writeDraftStore({
      ...draftStore.draft,
      shared: { ...draftStore.draft.shared, ...patch },
    });
  });
});

describe("AlignCreatePanel", () => {
  it("opens a conversation for the request and creates no issue", async () => {
    renderPanel();
    await typeRequest("add dark mode");
    await userEvent.click(submitButton());

    await waitFor(() => expect(mocks.createIssueDraftSession).toHaveBeenCalledTimes(1));
    const input = mocks.createIssueDraftSession.mock.calls[0]![0] as CreateSessionInput;
    expect(input.runtime_id).toBe("rt-1");
    // The idea is stored in the draft before anything is sent, so a lost first
    // turn costs the turn and not the request.
    expect(input.draft?.description).toBe("add dark mode");

    await waitFor(() => expect(mocks.sendChatMessage).toHaveBeenCalledTimes(1));
    // No attachments referenced → the third argument stays absent rather than
    // arriving as an empty list.
    expect(mocks.sendChatMessage.mock.calls[0]![2]).toBeUndefined();
    await waitFor(() =>
      expect(mocks.push).toHaveBeenCalledWith("/acme/issues/new/sess-new"),
    );
    expect(mocks.close).toHaveBeenCalled();
  });

  it("still opens the conversation when the first turn could not be sent", async () => {
    mocks.sendChatMessage.mockRejectedValue(new Error("network down"));
    renderPanel();
    await typeRequest("add dark mode");
    await userEvent.click(submitButton());

    // The draft exists and holds the request; staying here would only invite a
    // second, duplicate draft.
    await waitFor(() =>
      expect(mocks.push).toHaveBeenCalledWith("/acme/issues/new/sess-new"),
    );
  });

  it("refuses to start without a request", async () => {
    renderPanel();
    // Typing a request first is what proves the auto-selected runtime is in
    // place; clearing it again leaves the request as the only gate, which is
    // the one this test is about.
    await typeRequest("x");
    await userEvent.clear(editor());
    await waitFor(() => expect(submitButton()).toBeDisabled());
    expect(mocks.createIssueDraftSession).not.toHaveBeenCalled();
  });

  /**
   * DENE-367: the entry face asks for one thing — what to align on. The runtime
   * is chosen for the user, and the picker moves to the alignment page's
   * preview. What must not move is the choice itself.
   */
  it("starts on a request alone, with no runtime picker on the face", async () => {
    renderPanel();
    await typeRequest("add dark mode");

    // Enabled by the request alone: the runtime was seeded without a click.
    expect(submitButton()).toBeEnabled();
    expect(screen.queryByTestId("runtime-picker")).toBeNull();
  });

  it("asks for a runtime only when there is none to run on", async () => {
    mocks.runtimes = [];
    renderPanel();

    expect(
      await screen.findByText(
        "No online runtime is available, so the alignment agent cannot run.",
      ),
    ).toBeTruthy();
    // A link, not a button: it navigates to the machines list.
    expect(screen.getByRole("link", { name: "Connect a runtime" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Start aligning" })).toBeNull();
  });

  /**
   * With no picker on this face, the seed is the only chance to land on a
   * machine that can actually run. The list arrives ordered by creation, so an
   * older offline machine of the user's own sorts ahead of a usable online one
   * — and seeding that would state the alignment cannot start while a machine
   * that could run it sits one entry away, with nothing to click.
   */
  it("seeds an online runtime over an older offline one the user owns", async () => {
    mocks.runtimes = [
      { ...ONLINE_RUNTIME, id: "rt-old", name: "Old laptop", status: "offline" },
      { ...ONLINE_RUNTIME, id: "rt-new", name: "Desk" },
    ];
    renderPanel();
    await typeRequest("add dark mode");
    expect(screen.queryByText("Old laptop is offline, so the alignment cannot start.")).toBeNull();

    await userEvent.click(submitButton());
    await waitFor(() => expect(mocks.createIssueDraftSession).toHaveBeenCalledTimes(1));
    const input = mocks.createIssueDraftSession.mock.calls[0]![0] as CreateSessionInput;
    expect(input.runtime_id).toBe("rt-new");
  });

  it("prefers the user's own machine among the online ones", async () => {
    mocks.runtimes = [
      {
        ...ONLINE_RUNTIME,
        id: "rt-shared",
        name: "Shared",
        owner_id: "user-2",
        visibility: "public",
      } as unknown as RuntimeDevice,
      { ...ONLINE_RUNTIME, id: "rt-mine", name: "Mine" },
    ];
    renderPanel();
    await typeRequest("add dark mode");

    await userEvent.click(submitButton());
    await waitFor(() => expect(mocks.createIssueDraftSession).toHaveBeenCalledTimes(1));
    const input = mocks.createIssueDraftSession.mock.calls[0]![0] as CreateSessionInput;
    expect(input.runtime_id).toBe("rt-mine");
  });

  it("names the offline runtime instead of only disabling submit", async () => {
    mocks.runtimes = [{ ...ONLINE_RUNTIME, status: "offline" }];
    renderPanel();

    expect(
      await screen.findByText("Local is offline, so the alignment cannot start."),
    ).toBeTruthy();
    await userEvent.type(editor(), "add dark mode");
    expect(submitButton()).toBeDisabled();
    expect(mocks.createIssueDraftSession).not.toHaveBeenCalled();
  });

  it("routes an unfinished draft to its conversation", async () => {
    // The row carries a live status: the banner counts only the alignments with
    // a next turn in them (DENE-371), so a fixture without one is filtered out
    // before it ever reaches the banner.
    mocks.drafts = [{ chat_session_id: "sess-old", status: "draft" }];
    renderPanel();
    await userEvent.click(await screen.findByRole("button", { name: "resume-unfinished" }));
    expect(mocks.push).toHaveBeenCalledWith("/acme/issues/new/sess-old");
    expect(mocks.close).toHaveBeenCalled();
  });

  /**
   * DENE-370: the body is the create draft's `align` slot. A request written on
   * this face must land there (and in no other face's slot), and a request that
   * is already there is what the editor opens on — which is how the manual
   * face's assist-init reaches this one.
   */
  it("opens on the request already in the align draft slot", async () => {
    draftStore.draft.align.request = "carried from the manual face";
    renderPanel();

    expect(editor()).toHaveValue("carried from the manual face");
    // A body that is already there is submittable on its own, without a click
    // anywhere else on the face.
    await waitFor(() => expect(submitButton()).toBeEnabled());
  });

  it("writes edits into the align slot so a mode switch cannot lose them", async () => {
    renderPanel();
    await typeRequest("add dark mode");

    await waitFor(() => expect(mocks.setAlign).toHaveBeenCalled());
    expect(mocks.setAlign).toHaveBeenLastCalledWith({ request: "add dark mode" });
    // And the other faces' bodies are untouched.
    expect(draftStore.draft.manual.description).toBe("");
    expect(draftStore.draft.agent.prompt).toBe("");
  });

  it("marks the draft as being edited on the alignment face", () => {
    renderPanel();
    expect(mocks.setActiveMode).toHaveBeenCalledWith("align");
  });

  /**
   * The whole point of folding this face into the create dialog: a file
   * uploaded on the manual face is still in the shared pool here, and the ids
   * whose link the request references ride the first turn (DENE-369's
   * transport) instead of being attached to nothing.
   */
  it("sends the shared pool's referenced attachments with the first turn", async () => {
    draftStore.draft.shared.attachments = uploadedPool();
    draftStore.draft.align.request = "look at ![shot](https://cdn/x.png)";
    renderPanel();

    // The pool the manual face fills is the pool this face renders.
    expect(editor().getAttribute("data-attachments-count")).toBe("1");

    await userEvent.click(submitButton());

    await waitFor(() => expect(mocks.sendChatMessage).toHaveBeenCalledTimes(1));
    expect(mocks.sendChatMessage.mock.calls[0]![2]).toEqual(["att-1"]);
  });

  it("leaves unreferenced pool entries out of the first turn", async () => {
    // A file uploaded on the manual face whose link was removed from the
    // alignment request must not ride along.
    draftStore.draft.shared.attachments = uploadedPool();
    draftStore.draft.align.request = "no files in here";
    renderPanel();

    await userEvent.click(submitButton());

    await waitFor(() => expect(mocks.sendChatMessage).toHaveBeenCalledTimes(1));
    expect(mocks.sendChatMessage.mock.calls[0]![2]).toBeUndefined();
  });

  it("offers a way back to the new-issue face", async () => {
    const onSwitchMode = vi.fn();
    renderPanel({ onSwitchMode });

    await userEvent.click(screen.getByRole("button", { name: /Switch to New issue/i }));
    expect(onSwitchMode).toHaveBeenCalledWith(null);
  });

  /**
   * DENE-423: the project is optional and the face does not ask for it up
   * front — but when one is chosen, the draft is created holding it, so the
   * whole group the conversation settles on is filed under that project rather
   * than under nothing. It rides the SHARED slot, like the attachment pool, so
   * the manual face and this one are choosing the same field.
   */
  it("files the conversation under the project picked on this face", async () => {
    renderPanel();
    await typeRequest("add dark mode");

    await userEvent.click(screen.getByTestId("project-picker"));
    expect(mocks.setShared).toHaveBeenCalledWith({ projectId: "proj-1" });
    expect(screen.getByTestId("project-picker")).toHaveAttribute(
      "data-project-id",
      "proj-1",
    );

    await userEvent.click(submitButton());
    await waitFor(() => expect(mocks.createIssueDraftSession).toHaveBeenCalledTimes(1));
    const input = mocks.createIssueDraftSession.mock.calls[0]![0] as CreateSessionInput;
    expect(input.draft?.project_id).toBe("proj-1");
  });

  /**
   * The other half of the same contract: a project the manual face already
   * committed — including one an opener seeded from a project page — is what
   * this face opens on, with no second click, because both faces read the one
   * shared slot.
   */
  it("opens on the project the shared slot already holds", async () => {
    draftStore.draft.shared.projectId = "proj-9";
    renderPanel();

    expect(screen.getByTestId("project-picker")).toHaveAttribute(
      "data-project-id",
      "proj-9",
    );

    await typeRequest("add dark mode");
    await userEvent.click(submitButton());
    await waitFor(() => expect(mocks.createIssueDraftSession).toHaveBeenCalledTimes(1));
    const input = mocks.createIssueDraftSession.mock.calls[0]![0] as CreateSessionInput;
    expect(input.draft?.project_id).toBe("proj-9");
  });

  it("leaves the project out of the draft when none was chosen", async () => {
    renderPanel();
    await typeRequest("add dark mode");
    await userEvent.click(submitButton());

    await waitFor(() => expect(mocks.createIssueDraftSession).toHaveBeenCalledTimes(1));
    const input = mocks.createIssueDraftSession.mock.calls[0]![0] as CreateSessionInput;
    // Absent, not an empty string: "no project" is the field being missing.
    expect(input.draft).not.toHaveProperty("project_id");
  });

  it("blocks submit while a shared attachment is still uploading", async () => {
    draftStore.draft.shared.attachments = [
      { clientUploadId: "c-flight", status: "uploading", filename: "mid.png", size: 9 },
    ];
    draftStore.draft.align.request = "add dark mode";
    renderPanel();

    // The coordinator-owned placeholder widens the gate, so the button cannot
    // serialize a request whose image has not landed yet.
    await waitFor(() => expect(submitButton()).toBeDisabled());
    fireEvent.click(submitButton());
    expect(mocks.createIssueDraftSession).not.toHaveBeenCalled();
  });
});
