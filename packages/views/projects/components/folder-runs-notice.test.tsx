// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { AgentTask } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const mockSnapshot = vi.hoisted(() => vi.fn());
const flag = vi.hoisted(() => ({ on: true }));

vi.mock("@multica/core/api", () => ({
  api: {
    getAgentTaskSnapshot: mockSnapshot,
    listRuntimes: () => Promise.resolve([{ id: "rt-1", daemon_id: "mac" }, { id: "rt-2", daemon_id: "mac" }]),
    getIssue: (id: string) => Promise.resolve({ id, identifier: `DENE-${id}`, title: `Issue ${id}` }),
  },
}));
vi.mock("@multica/core/config", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/config")>()),
  useFeatureEnabled: (key: string) => key === "office_project_folder" && flag.on,
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

import { IssueSharedFolderNotice } from "./folder-runs-notice";

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

function renderNotice(issueId: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <IssueSharedFolderNotice issueId={issueId} />
    </QueryClientProvider>,
  );
}

describe("IssueSharedFolderNotice", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    flag.on = true;
    mockSnapshot.mockResolvedValue([
      run("1", { work_dir: "/Users/someone/work/app" }),
      run("2", { runtime_id: "rt-2", work_dir: "/Users/someone/work/app" }),
      run("3", { runtime_id: "", issue_id: "", chat_session_id: "chat-9", work_dir: "/Users/someone/work/solo" }),
    ]);
  });

  it("names the other runs in this issue's folder, by the folder's name only", async () => {
    renderNotice("1");

    expect(await screen.findByText("Shared folder app")).toBeInTheDocument();
    expect(screen.getByText(/2 runs are working in this folder/i)).toBeInTheDocument();
    expect(screen.getByText("This issue")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("link")).toHaveAttribute("href", "/acme/issues/2"));
    expect(screen.getByText("DENE-2")).toBeInTheDocument();
    expect(screen.queryByText(/\/Users\/someone/)).not.toBeInTheDocument();
  });

  it("is silent when the issue runs alone", async () => {
    renderNotice("9");
    await waitFor(() => expect(mockSnapshot).toHaveBeenCalled());
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("stays out of builds without the flag", () => {
    flag.on = false;
    renderNotice("1");
    expect(mockSnapshot).not.toHaveBeenCalled();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });
});
