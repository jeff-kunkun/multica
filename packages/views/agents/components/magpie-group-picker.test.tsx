import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent } from "@multica/core/types";
import type { MagpieRoute } from "@multica/core/runtimes";
import { renderWithI18n } from "../../test/i18n";

const mocks = vi.hoisted(() => ({
  updateAgent: vi.fn(),
  cacheAgentResponse: vi.fn(),
  canEdit: true,
}));

vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => ({}),
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("@multica/core/api", () => ({ api: { updateAgent: mocks.updateAgent } }));
vi.mock("@multica/core/permissions", () => ({
  useAgentPermissions: () => ({ canEdit: { allowed: mocks.canEdit } }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  cacheAgentResponse: mocks.cacheAgentResponse,
}));
vi.mock("sonner", () => ({ toast: { error: vi.fn() } }));

import { MagpieGroupPicker } from "./magpie-group-picker";

const models = [{ id: "a/b", name: "B" }];
const ROUTE: MagpieRoute = {
  group: "office-opencode",
  models,
  groups: [
    { name: "office-opencode", models },
    { name: "office-pojia", models },
    { name: "office-router", models },
  ],
};

function agent(runtime_config: Record<string, unknown>): Agent {
  return { id: "agent-1", custom_args: [], runtime_config } as unknown as Agent;
}

const box = (name: string) => screen.getByRole("checkbox", { name });

beforeEach(() => {
  vi.clearAllMocks();
  mocks.canEdit = true;
  mocks.updateAgent.mockImplementation(async (_id, data) => agent(data.runtime_config));
});

describe("MagpieGroupPicker", () => {
  it("ticks the agent's groups and marks the first as default", () => {
    renderWithI18n(
      <MagpieGroupPicker agent={agent({ magpie_groups: ["office-pojia", "office-opencode"] })} route={ROUTE} />,
    );
    expect(box("office-pojia")).toBeChecked();
    expect(box("office-opencode")).toBeChecked();
    expect(box("office-router")).not.toBeChecked();
    expect(screen.getAllByText("Default")).toHaveLength(1);
    // Only a ticked group that is not the default can become it.
    expect(screen.getAllByRole("button", { name: "Make default" })).toHaveLength(1);
  });

  it("adds a group after the default, keeping other runtime config", async () => {
    const user = userEvent.setup();
    renderWithI18n(
      <MagpieGroupPicker agent={agent({ mode: "x", magpie_groups: ["office-opencode"] })} route={ROUTE} />,
    );
    await user.click(box("office-pojia"));
    await waitFor(() =>
      expect(mocks.updateAgent).toHaveBeenCalledWith("agent-1", {
        runtime_config: { mode: "x", magpie_groups: ["office-opencode", "office-pojia"] },
      }),
    );
    expect(mocks.cacheAgentResponse).toHaveBeenCalled();
  });

  it("makes a group the default and removes the last one", async () => {
    const user = userEvent.setup();
    const { unmount } = renderWithI18n(
      <MagpieGroupPicker agent={agent({ magpie_groups: ["office-opencode", "office-pojia"] })} route={ROUTE} />,
    );
    await user.click(screen.getByRole("button", { name: "Make default" }));
    await waitFor(() =>
      expect(mocks.updateAgent).toHaveBeenCalledWith("agent-1", {
        runtime_config: { magpie_groups: ["office-pojia", "office-opencode"] },
      }),
    );
    unmount();

    renderWithI18n(<MagpieGroupPicker agent={agent({ magpie_groups: ["office-pojia"] })} route={ROUTE} />);
    await user.click(box("office-pojia"));
    // No groups left: the key goes, and the agent runs on its CLI's group.
    await waitFor(() =>
      expect(mocks.updateAgent).toHaveBeenLastCalledWith("agent-1", { runtime_config: {} }),
    );
  });

  it("keeps a listed group the machine no longer has, so it can be removed", () => {
    renderWithI18n(<MagpieGroupPicker agent={agent({ magpie_groups: ["gone"] })} route={ROUTE} />);
    expect(box("gone")).toBeChecked();
  });

  it("is read-only for people who cannot edit the agent", () => {
    mocks.canEdit = false;
    renderWithI18n(<MagpieGroupPicker agent={agent({})} route={ROUTE} />);
    expect(box("office-pojia")).toHaveAttribute("aria-disabled", "true");
  });
});
