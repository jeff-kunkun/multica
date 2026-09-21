// @vitest-environment jsdom

// The one-click seat switch on the agents list (DENE-714). What is pinned here
// is the wiring only: which verb the menu item calls, and that the item names
// the state the row is in rather than the state it is not. The server is the
// authoritative gate — that half lives in the Go suites
// (`agent_disabled_test.go`, `agent_disabled_claim_test.go`), and hiding or
// showing this item never decides whether a parked seat takes work.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const disableSpy = vi.hoisted(() => vi.fn(async () => ({})));
const enableSpy = vi.hoisted(() => vi.fn(async () => ({})));
const invalidateSpy = vi.hoisted(() => vi.fn());

vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => ({ invalidateQueries: invalidateSpy }),
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ agentDetail: (id: string) => `/w/agents/${id}` }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  workspaceKeys: { agents: (wsId: string) => ["agents", wsId] },
}));
vi.mock("@multica/core/api", () => ({
  api: {
    disableAgent: disableSpy,
    enableAgent: enableSpy,
    archiveAgent: vi.fn(),
    restoreAgent: vi.fn(),
    cancelAgentTasks: vi.fn(),
  },
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("../../navigation", () => ({
  AppLink: ({ children }: { children?: React.ReactNode }) => <a>{children}</a>,
  useIntentNavigate: () => vi.fn(),
}));

import { AgentRowActions } from "./agent-row-actions";

function makeAgent(overrides: Partial<Agent> = {}): Agent {
  return {
    id: "agent-1",
    workspace_id: "ws-1",
    runtime_id: "rt-1",
    name: "Lambda",
    description: "",
    instructions: "",
    avatar_url: null,
    runtime_mode: "local",
    runtime_config: {},
    custom_args: [],
    visibility: "private",
    permission_mode: "private",
    invocation_targets: [],
    status: "idle",
    max_concurrent_tasks: 1,
    model: "",
    owner_id: "user-1",
    skills: [],
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    archived_at: null,
    archived_by: null,
    ...overrides,
  } as Agent;
}

function openMenu(agent: Agent, canManage = true) {
  renderWithI18n(
    <AgentRowActions
      agent={agent}
      presence={null}
      canManage={canManage}
      duplicateHref="/w/agents/new"
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Row actions" }));
}

describe("agents list seat switch", () => {
  beforeEach(() => {
    disableSpy.mockClear();
    enableSpy.mockClear();
    invalidateSpy.mockClear();
  });

  it("parks a seat from the row menu, without opening the agent", async () => {
    openMenu(makeAgent());
    fireEvent.click(await screen.findByText("Disable"));
    await waitFor(() => expect(disableSpy).toHaveBeenCalledWith("agent-1"));
    expect(enableSpy).not.toHaveBeenCalled();
    // The row has to repaint as disabled straight away, or the switch looks
    // like it did nothing.
    expect(invalidateSpy).toHaveBeenCalled();
  });

  it("offers the way back on a parked seat", async () => {
    openMenu(makeAgent({ disabled_at: "2026-02-03T04:05:06Z" }));
    expect(screen.queryByText("Disable")).toBeNull();
    fireEvent.click(await screen.findByText("Enable"));
    await waitFor(() => expect(enableSpy).toHaveBeenCalledWith("agent-1"));
  });

  // A backend that predates the switch sends no `disabled_at` at all. That has
  // to read as "taking work" — the state those backends are in — not as a
  // parked seat with an Enable item that would 409.
  it("treats a missing disabled_at as taking work", async () => {
    openMenu(makeAgent({ disabled_at: undefined }));
    expect(await screen.findByText("Disable")).toBeTruthy();
  });

  // Archived is a different lifecycle: the seat is already out of every
  // dispatch path and restore is the only move that means anything.
  it("hides the switch on an archived seat", async () => {
    openMenu(makeAgent({ archived_at: "2026-02-03T04:05:06Z" }));
    expect(await screen.findByText("Restore")).toBeTruthy();
    expect(screen.queryByText("Disable")).toBeNull();
    expect(screen.queryByText("Enable")).toBeNull();
  });

  it("hides the switch from someone who cannot manage the seat", async () => {
    openMenu(makeAgent(), false);
    expect(await screen.findByText("Open in new tab")).toBeTruthy();
    expect(screen.queryByText("Disable")).toBeNull();
  });
});
