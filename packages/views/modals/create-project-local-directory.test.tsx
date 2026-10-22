// @vitest-environment jsdom

import React from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithI18n } from "../test/i18n";

// What the desktop validator reports for the picked folder.
let pickedIsGitRepo: boolean | undefined = true;

const createProjectMock = vi.fn().mockResolvedValue({ id: "p1", slug: "p1" });

// jsdom has no IntersectionObserver; the modal's scroll affordances construct
// one, and the resulting rejection aborts the submit handler mid-flight.
class NoopIntersectionObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() {
    return [];
  }
}
vi.stubGlobal("IntersectionObserver", NoopIntersectionObserver);

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: [] }),
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  queryOptions: (options: unknown) => options,
}));

vi.mock("@multica/core/projects/mutations", () => ({
  useCreateProject: () => ({ mutateAsync: createProjectMock }),
}));

vi.mock("@multica/core/projects", () => ({
  useProjectDraftStore: (selector: (state: unknown) => unknown) =>
    selector({
      draft: {
        title: "Game Client",
        description: "",
        status: "planned",
        priority: "none",
        icon: "📁",
        leadId: null,
        leadType: null,
        startDate: null,
        dueDate: null,
        repos: [],
      },
      setDraft: vi.fn(),
      clearDraft: vi.fn(),
      resetDraft: vi.fn(),
    }),
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("../workflows/workflow-pill", () => ({ WorkflowPill: () => null }));
vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "workspace-1", slug: "ws", repos: [] }),
  useWorkspacePaths: () => ({ projectDetail: (id: string) => `/ws/projects/${id}` }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"], queryFn: vi.fn() }),
  agentListOptions: () => ({ queryKey: ["agents"], queryFn: vi.fn() }),
  workspaceKeys: { list: () => ["workspaces"] },
}));
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: vi.fn() }),
}));
vi.mock("../navigation", () => ({ useNavigation: () => ({ push: vi.fn() }) }));
vi.mock("../editor", () => {
  const ContentEditor = React.forwardRef<HTMLTextAreaElement, { placeholder?: string }>(
    ({ placeholder }, ref) => <textarea ref={ref} placeholder={placeholder} />,
  );
  ContentEditor.displayName = "ContentEditor";
  return {
    ContentEditor,
    TitleEditor: ({
      placeholder,
      onChange,
    }: {
      placeholder?: string;
      onChange?: (value: string) => void;
    }) => <input placeholder={placeholder} onChange={(e) => onChange?.(e.target.value)} />,
  };
});
vi.mock("../issues/components/priority-icon", () => ({ PriorityIcon: () => <span /> }));
vi.mock("../common/actor-avatar", () => ({ ActorAvatar: () => <span /> }));
vi.mock("../projects/components/project-start-date-picker", () => ({
  ProjectStartDatePicker: () => <button type="button">Start date</button>,
}));
vi.mock("../projects/components/project-due-date-picker", () => ({
  ProjectDueDatePicker: () => <button type="button">Due date</button>,
}));

// Desktop-only surface: without these the Local directory tab never renders.
vi.mock("../platform/local-directory", () => ({
  isDesktopShell: () => true,
  pickDirectory: () =>
    Promise.resolve({ ok: true, path: "/Users/dev/work/game-client", basename: "game-client" }),
  validateLocalDirectory: () => Promise.resolve({ ok: true, is_git_repo: pickedIsGitRepo }),
  initLocalGit: async () => ({ ok: false, reason: "unsupported" as const }),
}));
vi.mock("../platform/use-local-daemon-status", () => ({
  useLocalDaemonStatus: () => ({ daemonId: "daemon-1", deviceName: "MacBook", running: true }),
}));

// Render overlays inline so their contents are assertable.
vi.mock("@multica/ui/components/ui/dialog", () => ({
  Dialog: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  DialogContent: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  DialogTitle: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));
vi.mock("@multica/ui/components/ui/popover", () => ({
  Popover: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  PopoverTrigger: ({ render }: { render: React.ReactNode }) => <>{render}</>,
  PopoverContent: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));
vi.mock("@multica/ui/components/ui/dropdown-menu", () => ({
  DropdownMenu: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  DropdownMenuTrigger: ({ render }: { render: React.ReactNode }) => <>{render}</>,
  DropdownMenuContent: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  DropdownMenuItem: ({ children, onClick }: { children: React.ReactNode; onClick?: () => void }) => (
    <button type="button" onClick={onClick}>
      {children}
    </button>
  ),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { CreateProjectModal, buildLocalDirectoryResourceRef } from "./create-project";

async function pickLocalDirectory(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: /Local directory/i }));
  await user.click(screen.getByRole("button", { name: /Choose directory/i }));
  await waitFor(() => expect(screen.getByText("/Users/dev/work/game-client")).toBeInTheDocument());
}

describe("CreateProjectModal — local directory", () => {
  beforeEach(() => {
    createProjectMock.mockClear();
    pickedIsGitRepo = true;
  });

  // Tasks always run directly in the bound folder, so there is no mode to pick.
  it("offers no execution mode once a folder is picked", async () => {
    const user = userEvent.setup();
    renderWithI18n(<CreateProjectModal onClose={vi.fn()} />);

    await pickLocalDirectory(user);

    expect(screen.getByRole("button", { name: /Change directory/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Direct$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("radio")).not.toBeInTheDocument();
  });

  it("offers to create a local Git repository for a plain folder", async () => {
    pickedIsGitRepo = false;
    const user = userEvent.setup();
    renderWithI18n(<CreateProjectModal onClose={vi.fn()} />);

    await pickLocalDirectory(user);

    expect(screen.getByRole("button", { name: /create local git/i })).toBeInTheDocument();
  });

  it("has no description field", () => {
    renderWithI18n(<CreateProjectModal onClose={vi.fn()} />);

    expect(screen.queryByRole("textbox", { name: /description/i })).not.toBeInTheDocument();
    expect(screen.queryByPlaceholderText(/description/i)).not.toBeInTheDocument();
  });
});

describe("buildLocalDirectoryResourceRef", () => {
  it("sends no execution mode", () => {
    expect(
      buildLocalDirectoryResourceRef({
        localPath: "/Users/dev/work/game-client",
        daemonId: "daemon-1",
        label: "game-client",
      }),
    ).toEqual({
      local_path: "/Users/dev/work/game-client",
      daemon_id: "daemon-1",
      label: "game-client",
    });
  });

  it("sends identity fields the machine measured at pick time", () => {
    expect(
      buildLocalDirectoryResourceRef({
        localPath: "/tmp/x",
        daemonId: "d",
        label: null,
        realPath: "/private/tmp/x",
        repoKey: "github.com/acme/x",
        isGitRepo: true,
      }),
    ).toEqual({
      local_path: "/tmp/x",
      daemon_id: "d",
      real_path: "/private/tmp/x",
      repo_key: "github.com/acme/x",
      is_git_repo: true,
    });
  });
});
