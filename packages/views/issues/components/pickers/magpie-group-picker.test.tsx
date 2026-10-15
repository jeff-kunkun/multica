import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Issue } from "@multica/core/types";
import { renderWithI18n } from "../../../test/i18n";

const mocks = vi.hoisted(() => ({
  agents: [] as unknown[],
  runtimes: [] as unknown[],
  setKey: vi.fn(),
  deleteKey: vi.fn(),
  invalidate: vi.fn(),
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { queryKey: readonly unknown[] }) => ({
    data: options.queryKey[0] === "agents" ? mocks.agents : mocks.runtimes,
  }),
  useQueryClient: () => ({ invalidateQueries: mocks.invalidate }),
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("@multica/core/api", () => ({
  api: { setIssueMetadataKey: mocks.setKey, deleteIssueMetadataKey: mocks.deleteKey },
}));
vi.mock("@multica/core/workspace/queries", () => ({
  agentListOptions: () => ({ queryKey: ["agents"] }),
}));
vi.mock("@multica/core/runtimes", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/runtimes")>()),
  runtimeListOptions: () => ({ queryKey: ["runtimes"] }),
}));
vi.mock("sonner", () => ({ toast: { error: vi.fn() } }));

import { MagpieGroupPicker, useIssueMagpieGroups } from "./magpie-group-picker";

const GROUPS = ["office-opencode", "office-pojia"];

function issue(metadata: Record<string, unknown>): Issue {
  return { id: "issue-1", assignee_type: "agent", assignee_id: "agent-1", metadata } as unknown as Issue;
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.setKey.mockResolvedValue(undefined);
  mocks.deleteKey.mockResolvedValue(undefined);
  mocks.runtimes = [{ id: "rt-1", provider: "opencode" }];
  mocks.agents = [{ id: "agent-1", runtime_id: "rt-1", runtime_config: { magpie_groups: GROUPS } }];
});

describe("useIssueMagpieGroups", () => {
  it("offers a choice only for an agent with more than one group", () => {
    const groups = (i: Pick<Issue, "assignee_type" | "assignee_id">) =>
      renderHook(() => useIssueMagpieGroups(i)).result.current;
    expect(groups({ assignee_type: "agent", assignee_id: "agent-1" })).toEqual(GROUPS);
    expect(groups({ assignee_type: "member", assignee_id: "agent-1" })).toBeNull();
    expect(groups({ assignee_type: null, assignee_id: null })).toBeNull();

    mocks.agents = [{ id: "agent-1", runtime_id: "rt-1", runtime_config: { magpie_groups: ["office-pojia"] } }];
    expect(groups({ assignee_type: "agent", assignee_id: "agent-1" })).toBeNull();

    mocks.agents = [{ id: "agent-1", runtime_id: "rt-1", runtime_config: { magpie_groups: GROUPS } }];
    mocks.runtimes = [{ id: "rt-1", provider: "claude" }];
    expect(groups({ assignee_type: "agent", assignee_id: "agent-1" })).toBeNull();
  });
});

describe("MagpieGroupPicker", () => {
  it("shows the issue's choice, or the default", () => {
    const { unmount } = renderWithI18n(<MagpieGroupPicker issue={issue({})} groups={GROUPS} />);
    expect(screen.getByText("office-opencode (default)")).toBeInTheDocument();
    unmount();
    renderWithI18n(<MagpieGroupPicker issue={issue({ magpie_group: "office-pojia" })} groups={GROUPS} />);
    expect(screen.getByText("office-pojia")).toBeInTheDocument();
  });

  it("stores a choice on the issue, and the default by clearing it", async () => {
    const user = userEvent.setup();
    const { unmount } = renderWithI18n(<MagpieGroupPicker issue={issue({})} groups={GROUPS} />);
    await user.click(screen.getByText("office-opencode (default)"));
    await user.click(await screen.findByText("office-pojia"));
    await waitFor(() => expect(mocks.setKey).toHaveBeenCalledWith("issue-1", "magpie_group", "office-pojia"));
    expect(mocks.invalidate).toHaveBeenCalled();
    unmount();

    renderWithI18n(<MagpieGroupPicker issue={issue({ magpie_group: "office-pojia" })} groups={GROUPS} />);
    await user.click(screen.getByText("office-pojia"));
    await user.click(await screen.findByText("office-opencode (default)"));
    await waitFor(() => expect(mocks.deleteKey).toHaveBeenCalledWith("issue-1", "magpie_group"));
  });
});
