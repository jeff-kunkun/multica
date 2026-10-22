import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";

vi.mock("@multica/core/api", () => ({
  api: { getBaseUrl: () => "http://localhost" },
}));
import { renderWithI18n } from "../../test/i18n";
import { AgentListToolbar } from "./agent-list-toolbar";
import type { AgentListRow } from "./agents-page";

const NO_FILTERS = {
  availability: [],
  runtimes: [],
  owners: [],
  models: [],
  access: [],
};

function makeRow(id: string, name: string): AgentListRow {
  return {
    agent: {
      id,
      workspace_id: "ws-1",
      runtime_id: "rt-1",
      name,
      description: "",
      instructions: "",
      avatar_url: null,
      runtime_mode: "local",
      runtime_config: {},
      max_concurrent_tasks: 1,
      owner_id: "user-1",
      archived_at: null,
      custom_args: [],
      visibility: "private",
      permission_mode: "private",
      invocation_targets: [],
      model: "claude",
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
      status: "idle",
      skills: [],
      archived_by: null,
    },
    runtime: null,
    presence: null,
    activity: null,
    runCount: 0,
    lastActiveDays: null,
    owner: null,
    isOwnedByMe: false,
    canManage: false,
  };
}

describe("AgentListToolbar model facet", () => {
  // Regression (DENE-708): the facet label is display text, the facet VALUE is
  // the stored `agent.model`. Decoding the value too would merge two distinct
  // seat strings into one facet and filter on a string no agent has.
  // Canonical decoding matrix: `provider-seat-model.test.ts`.
  it("labels a provider-preset seat decoded but still filters on the raw string", () => {
    const onToggleFilter = vi.fn();
    const row = makeRow("a-1", "Alpha");
    row.agent.model = "deepseek-official/deepseek-v4%2Fflash";

    renderWithI18n(
      <AgentListToolbar
        scope="all"
        onScopeChange={vi.fn()}
        scopeCounts={{ mine: 0, all: 1, archived: 0 }}
        search=""
        onSearchChange={vi.fn()}
        filters={NO_FILTERS}
        onToggleFilter={onToggleFilter}
        onClearFilters={vi.fn()}
        sortField="name"
        sortDirection="asc"
        onSortFieldChange={vi.fn()}
        onSortDirectionChange={vi.fn()}
        hiddenColumns={[]}
        onToggleColumn={vi.fn()}
        allRows={[row]}
        members={[]}
        visibleCount={1}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /Filter/ }));
    fireEvent.click(screen.getByText("Model"));

    const option = screen.getByText("deepseek-official · deepseek-v4/flash");
    expect(option).toBeInTheDocument();

    fireEvent.click(option);
    expect(onToggleFilter).toHaveBeenCalledWith(
      "models",
      "deepseek-official/deepseek-v4%2Fflash",
    );
  });
});
