import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type { ProjectMemoryLocation, ProjectMemoryStatus } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enProjects from "../../locales/en/projects.json";

const TEST_RESOURCES = { en: { common: enCommon, projects: enProjects } };

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", () => ({ useWorkspacePaths: () => ({}) }));
vi.mock("../../navigation", () => ({ AppLink: ({ children }: { children: React.ReactNode }) => <a>{children}</a> }));

const mockGetProjectMemory = vi.hoisted(() => vi.fn());
vi.mock("@multica/core/api", () => ({
  api: { getProjectMemory: (...args: unknown[]) => mockGetProjectMemory(...args) },
}));

import { ProjectMemoryCard } from "./project-memory-card";

function location(key: string, path: string, overrides: Partial<ProjectMemoryLocation> = {}): ProjectMemoryLocation {
  return {
    key, path, kind: "file", exists: true, is_directory: false,
    modified_at: null, observed_at: null, error: null, mainline_ref: null, ...overrides,
  };
}

describe("ProjectMemoryCard", () => {
  it("tells a slot the mainline already has apart from one never written", async () => {
    const status: ProjectMemoryStatus = {
      project_id: "p-1", workspace_id: "ws-1", source: "local_directory",
      locations: [
        location("agents", "AGENTS.md"),
        location("docs_index", "docs/README.md", { exists: false, mainline_ref: "origin/dev" }),
        location("evidence_index", "docs/evidence/INDEX.md", { exists: false }),
      ],
      missing: ["docs_index", "evidence_index"], observed_at: null, latest_sediment_at: null,
      sediment_issue: null, sediment_agent_configured: true, sediment_error: null,
    };
    mockGetProjectMemory.mockResolvedValue(status);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <I18nProvider locale="en" resources={TEST_RESOURCES}>
          <ProjectMemoryCard projectId="p-1" />
        </I18nProvider>
      </QueryClientProvider>,
    );
    const behind = await screen.findByText("Behind origin/dev");
    expect(behind.getAttribute("title")).toBe("Present on origin/dev; sync the local directory");
    expect(screen.getByText("Missing")).toBeTruthy();
    expect(screen.getByText("Five memory locations in the project's local directory")).toBeTruthy();
  });

  it("lists the deliveries that wrote the memory, with their files", async () => {
    const status: ProjectMemoryStatus = {
      project_id: "p-1", workspace_id: "ws-1", source: "local_directory",
      locations: [location("agents", "AGENTS.md")],
      missing: [], observed_at: null, latest_sediment_at: "2026-10-01T00:00:00Z",
      sediment_issue: null, sediment_agent_configured: true, sediment_error: null,
      recent_sediments: [
        {
          id: "s-1", source_kind: "issue", issue_id: "i-1", issue_identifier: "DENE-7", chat_session_id: null,
          source_title: "加词条", verified: true, mainline: "kun", commits: [], pr_url: "",
          author_type: "agent", author_id: "a-1", created_at: new Date().toISOString(),
          changes: [{ location: "context", summary: "term", files: ["CONTEXT.md"] }],
        },
        {
          id: "s-2", source_kind: "chat", issue_id: null, issue_identifier: null, chat_session_id: "abcd1234-0000",
          source_title: "Routing talk", verified: false, mainline: "main", commits: [], pr_url: "",
          author_type: "agent", author_id: "a-1", created_at: new Date().toISOString(),
          changes: [{ location: "agents", summary: "rule" }],
        },
      ],
    };
    mockGetProjectMemory.mockResolvedValue(status);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <I18nProvider locale="en" resources={TEST_RESOURCES}>
          <ProjectMemoryCard projectId="p-1" />
        </I18nProvider>
      </QueryClientProvider>,
    );
    expect(await screen.findByText("Recent sediment")).toBeTruthy();
    expect(screen.getByText("DENE-7")).toBeTruthy();
    expect(screen.getByText("CONTEXT.md")).toBeTruthy();
    expect(screen.getByText("Chat Routing talk")).toBeTruthy();
    expect(screen.getByText("Not checked")).toBeTruthy();
    expect(screen.queryByText(/Last sediment/)).toBeNull();
  });
});
