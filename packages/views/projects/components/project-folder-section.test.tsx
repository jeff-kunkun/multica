// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { AgentTask, ProjectResource } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const mockList = vi.hoisted(() => vi.fn());
const mockCreate = vi.hoisted(() => vi.fn());
const mockUpdate = vi.hoisted(() => vi.fn());
const mockDelete = vi.hoisted(() => vi.fn());
const mockSnapshot = vi.hoisted(() => vi.fn());
const mockRuntimes = vi.hoisted(() => vi.fn());
const mockCopy = vi.hoisted(() => vi.fn());
const env = vi.hoisted(() => ({
  desktop: true,
  daemon: { daemonId: "mac" as string | null, deviceName: "Mac", running: true },
  validated: { ok: true, real_path: "/real/new-app", is_git_repo: true } as Record<string, unknown>,
}));

vi.mock("@multica/core/api", () => ({
  api: {
    listProjectResources: mockList,
    createProjectResource: mockCreate,
    updateProjectResource: mockUpdate,
    deleteProjectResource: mockDelete,
    getAgentTaskSnapshot: mockSnapshot,
    listRuntimes: mockRuntimes,
    getIssue: (id: string) => Promise.resolve({ id, identifier: `DENE-${id}`, title: `Issue ${id}` }),
  },
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    issueDetail: (id: string) => `/acme/issues/${id}`,
    chatSession: (id: string) => `/acme/chat/${id}`,
  }),
}));
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: (_type: string, id: string) => `Agent ${id}` }),
}));
vi.mock("../../navigation", () => ({
  AppLink: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a>,
}));
vi.mock("../../platform/local-directory", () => ({
  isDesktopShell: () => env.desktop,
  pickDirectory: () => Promise.resolve({ ok: true, path: "/work/new-app", basename: "new-app" }),
  validateLocalDirectory: () => Promise.resolve(env.validated),
}));
vi.mock("../../platform/use-local-daemon-status", () => ({
  useLocalDaemonStatus: () => env.daemon,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@multica/ui/lib/clipboard", () => ({ copyText: mockCopy }));

import { toast } from "sonner";
import { displayFolderPath, ProjectFolderSection } from "./project-folder-section";

function folder(id: string, ref: Record<string, unknown>, label: string | null = null): ProjectResource {
  return {
    id,
    project_id: "p-1",
    workspace_id: "ws-1",
    resource_type: "local_directory",
    resource_ref: ref,
    label,
    position: 0,
    created_at: "",
    created_by: null,
  } as ProjectResource;
}

function run(id: string, patch: Partial<AgentTask>): AgentTask {
  return {
    id,
    agent_id: `a-${id}`,
    runtime_id: "rt-1",
    issue_id: id,
    status: "running",
    created_at: `2026-10-27T10:00:0${id}Z`,
    ...patch,
  } as AgentTask;
}

function renderSection() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <ProjectFolderSection projectId="p-1" />
    </QueryClientProvider>,
  );
}

describe("ProjectFolderSection", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    env.desktop = true;
    env.daemon = { daemonId: "mac", deviceName: "Mac", running: true };
    env.validated = { ok: true, real_path: "/real/new-app", is_git_repo: true };
    mockList.mockResolvedValue({ resources: [], total: 0 });
    mockSnapshot.mockResolvedValue([]);
    mockRuntimes.mockResolvedValue([{ id: "rt-1", daemon_id: "mac" }]);
    mockCreate.mockImplementation((_p: string, data: { resource_ref: Record<string, unknown> }) =>
      Promise.resolve(folder("new", data.resource_ref)),
    );
    mockUpdate.mockImplementation((_p: string, id: string, data: { resource_ref: Record<string, unknown> }) =>
      Promise.resolve(folder(id, data.resource_ref)),
    );
  });

  it("points at the default directory until a folder is chosen, then binds this machine's pick", async () => {
    const user = userEvent.setup();
    renderSection();

    expect(await screen.findByText(/default working directory/i)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Choose folder/i }));

    await waitFor(() => expect(mockCreate).toHaveBeenCalled());
    expect(mockCreate).toHaveBeenCalledWith("p-1", {
      resource_type: "local_directory",
      resource_ref: {
        local_path: "/work/new-app",
        daemon_id: "mac",
        label: "new-app",
        real_path: "/real/new-app",
        is_git_repo: true,
      },
    });
  });

  it("replaces this machine's folder in place, clearing identity the new folder lacks", async () => {
    env.validated = { ok: true };
    mockList.mockResolvedValue({
      resources: [
        folder("res-1", {
          local_path: "/work/old",
          daemon_id: "mac",
          real_path: "/real/old",
          repo_key: "github.com/acme/old",
          is_git_repo: true,
        }),
      ],
      total: 1,
    });
    const user = userEvent.setup();
    renderSection();

    // An unnamed row is named after the path's last segment, with the path under it.
    expect(await screen.findByText("old")).toBeInTheDocument();
    expect(screen.getByText("/work/old")).toBeInTheDocument();
    expect(screen.getByText("This machine")).toBeInTheDocument();
    expect(screen.getByText("Git")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Choose folder/i })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Change folder/i }));

    await waitFor(() => expect(mockUpdate).toHaveBeenCalled());
    expect(mockUpdate).toHaveBeenCalledWith("p-1", "res-1", {
      label: "new-app",
      resource_ref: {
        local_path: "/work/new-app",
        daemon_id: "mac",
        label: "new-app",
        real_path: null,
        repo_key: null,
        is_git_repo: null,
      },
    });
    expect(mockCreate).not.toHaveBeenCalled();
  });

  it("shows the home folder as ~ but copies the absolute path", async () => {
    mockCopy.mockResolvedValue(true);
    mockList.mockResolvedValue({
      resources: [folder("res-1", { local_path: "/Users/kun/code/app", daemon_id: "mac" }, "app")],
      total: 1,
    });
    const user = userEvent.setup();
    renderSection();

    expect(await screen.findByText("~/code/app")).toBeInTheDocument();
    expect(screen.queryByText(/\/Users\/kun/)).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Copy full path" }));

    expect(mockCopy).toHaveBeenCalledWith("/Users/kun/code/app");
    expect(await screen.findByRole("button", { name: "Copied" })).toBeInTheDocument();
  });

  it("says so when the copy fails", async () => {
    mockCopy.mockResolvedValue(false);
    mockList.mockResolvedValue({
      resources: [folder("res-1", { local_path: "/work/app", daemon_id: "mac" }, "app")],
      total: 1,
    });
    const user = userEvent.setup();
    renderSection();

    await user.click(await screen.findByRole("button", { name: "Copy full path" }));

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("Couldn't copy the path"));
    expect(screen.queryByRole("button", { name: "Copied" })).not.toBeInTheDocument();
  });

  it.each([
    ["/Users/kun", "~"],
    ["/Users/kun/code", "~/code"],
    ["/home/ubuntu/work/office", "~/work/office"],
    ["/root/app", "~/app"],
    ["C:\\Users\\kun\\code", "~\\code"],
    ["/Users/Shared/app", "/Users/Shared/app"],
    ["/Users/kunkun2/app", "~/app"],
    ["/Volumes/Storge/app", "/Volumes/Storge/app"],
    ["/opt/Users/kun/app", "/opt/Users/kun/app"],
  ])("displays %s as %s", (path, shown) => {
    expect(displayFolderPath(path)).toBe(shown);
  });

  it("warns when more than one run is in the folder", async () => {
    mockList.mockResolvedValue({
      resources: [folder("res-1", { local_path: "/work/app", daemon_id: "mac" }, "app")],
      total: 1,
    });
    mockSnapshot.mockResolvedValue([
      run("1", { code_decision: { kind: "local_in_place", resource_id: "res-1", path: "/work/app" } }),
      run("2", { work_dir: "/work/app/" }),
      run("3", { work_dir: "/work/other" }),
    ]);
    renderSection();

    expect(await screen.findByText(/2 runs are working in this folder/i)).toBeInTheDocument();
    expect(screen.getByText("Agent a-1")).toBeInTheDocument();
    expect(screen.getByText("Agent a-2")).toBeInTheDocument();
    expect(screen.queryByText("Agent a-3")).not.toBeInTheDocument();
  });

  it("says nothing when the folder has one run", async () => {
    mockList.mockResolvedValue({
      resources: [folder("res-1", { local_path: "/work/app", daemon_id: "mac" }, "app")],
      total: 1,
    });
    mockSnapshot.mockResolvedValue([run("1", { work_dir: "/work/app" })]);
    renderSection();

    expect(await screen.findByText("app")).toBeInTheDocument();
    await waitFor(() => expect(mockSnapshot).toHaveBeenCalled());
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("sends the web to the desktop app or the CLI", async () => {
    env.desktop = false;
    env.daemon = { daemonId: null, deviceName: null, running: false } as never;
    mockList.mockResolvedValue({
      resources: [folder("res-1", { local_path: "/work/app", daemon_id: "mac" }, "app")],
      total: 1,
    });
    renderSection();

    expect(await screen.findByText("app")).toBeInTheDocument();
    expect(screen.getByText(/multica project resource add/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Choose folder|Change folder/i })).not.toBeInTheDocument();
    expect(screen.queryByText("This machine")).not.toBeInTheDocument();
  });
});
