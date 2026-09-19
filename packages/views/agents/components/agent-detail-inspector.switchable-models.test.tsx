import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { AgentDetailInspector } from "./agent-detail-inspector";

// Normalisation, clamping and the blank-row matrix are covered once in
// packages/core/agents/switchable-models.test.ts. This suite owns the wiring:
// what the switch writes, what the editor writes, and the read-only path.

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: () => ({ data: undefined, isSuccess: false }),
}));

vi.mock("../../common/avatar-upload-control", () => ({
  AvatarUploadControl: () => <div data-testid="avatar-upload" />,
}));

vi.mock("./inspector/model-picker", () => ({
  ModelPicker: () => <div data-testid="model-picker" />,
}));

vi.mock("./inspector/runtime-picker", () => ({
  RuntimePicker: () => <div data-testid="runtime-picker" />,
}));

vi.mock("./inspector/thinking-prop-row", () => ({
  ThinkingSettingField: () => <div data-testid="thinking-field" />,
}));

vi.mock("./inspector/service-tier-setting-field", () => ({
  ServiceTierSettingField: () => <div data-testid="service-tier-field" />,
}));

function agentFixture(overrides: Partial<Agent> = {}): Agent {
  return {
    id: "agent-1",
    workspace_id: "workspace-1",
    name: "Lambda",
    description: "Test agent",
    runtime_id: "runtime-1",
    model: "claude-opus-5[1m]",
    max_concurrent_tasks: 1,
    ...overrides,
  } as Agent;
}

function renderInspector(
  agent: Agent,
  onUpdate = vi.fn(async () => {}),
  canEdit = true,
) {
  renderWithI18n(
    <AgentDetailInspector
      agent={agent}
      runtime={null}
      runtimes={[]}
      members={[]}
      currentUserId="user-1"
      canEdit={canEdit}
      onUpdate={onUpdate}
    />,
  );
  return onUpdate;
}

const LINEUP_SWITCH = { name: "Model lineup" };

describe("AgentDetailInspector model lineup", () => {
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  it("reads a single-model agent as off and shows no editor", () => {
    renderInspector(agentFixture({ switchable_models: [] }));

    expect(screen.getByRole("switch", LINEUP_SWITCH)).not.toBeChecked();
    expect(screen.queryByTestId("switchable-model-row-0")).toBeNull();
  });

  it("treats a backend that omits the field as a single model", () => {
    renderInspector(agentFixture());

    expect(screen.getByRole("switch", LINEUP_SWITCH)).not.toBeChecked();
  });

  it("opens the editor seeded with the agent's own model, without writing yet", async () => {
    const user = userEvent.setup();
    const onUpdate = renderInspector(agentFixture({ switchable_models: [] }));

    await user.click(screen.getByRole("switch", LINEUP_SWITCH));

    expect(screen.getByRole("switch", LINEUP_SWITCH)).toBeChecked();
    expect(screen.getByLabelText("Model 1 id")).toHaveValue("claude-opus-5[1m]");
    // Seeding alone is not a change worth persisting — the lineup still
    // resolves to the one model the agent already runs.
    expect(onUpdate).not.toHaveBeenCalled();
  });

  it("clears the lineup to a single model when the switch is turned off", async () => {
    const user = userEvent.setup();
    const onUpdate = renderInspector(
      agentFixture({
        switchable_models: [
          { model: "claude-opus-5[1m]", role: "default", note: "" },
          { model: "claude-opus-5", role: "fallback", note: "" },
        ],
      }),
    );

    expect(screen.getByRole("switch", LINEUP_SWITCH)).toBeChecked();
    await user.click(screen.getByRole("switch", LINEUP_SWITCH));

    expect(screen.getByRole("switch", LINEUP_SWITCH)).not.toBeChecked();
    expect(screen.queryByTestId("switchable-model-row-0")).toBeNull();
    // Written on the click, not on the 650ms text debounce: "turn it off" is
    // the whole point of the switch and must not look like it did nothing.
    expect(onUpdate).toHaveBeenCalledWith("agent-1", {
      switchable_models: [],
    });
  });

  it("persists an edited model id", async () => {
    const user = userEvent.setup();
    const onUpdate = renderInspector(
      agentFixture({
        switchable_models: [
          { model: "claude-opus-5[1m]", role: "default", note: "" },
        ],
      }),
    );

    const field = screen.getByLabelText("Model 1 id");
    await user.clear(field);
    await user.type(field, "claude-sonnet-5");

    await waitFor(
      () =>
        expect(onUpdate).toHaveBeenCalledWith("agent-1", {
          switchable_models: [
            { model: "claude-sonnet-5", role: "default", note: "" },
          ],
        }),
      { timeout: 3000 },
    );
  });

  it("removes a row and keeps the rest", async () => {
    const user = userEvent.setup();
    const onUpdate = renderInspector(
      agentFixture({
        switchable_models: [
          { model: "claude-opus-5[1m]", role: "default", note: "" },
          { model: "claude-opus-5", role: "fallback", note: "" },
        ],
      }),
    );

    await user.click(screen.getByRole("button", { name: "Remove model 2" }));

    await waitFor(
      () =>
        expect(onUpdate).toHaveBeenCalledWith("agent-1", {
          switchable_models: [
            { model: "claude-opus-5[1m]", role: "default", note: "" },
          ],
        }),
      { timeout: 3000 },
    );
  });

  it("keeps the editor open for a freshly added blank row and does not send it", async () => {
    const user = userEvent.setup();
    const onUpdate = renderInspector(
      agentFixture({
        switchable_models: [
          { model: "claude-opus-5[1m]", role: "default", note: "" },
        ],
      }),
    );

    await user.click(screen.getByRole("button", { name: "Add model" }));

    expect(screen.getByTestId("switchable-model-row-1")).toBeInTheDocument();
    expect(screen.getByRole("switch", LINEUP_SWITCH)).toBeChecked();
    expect(
      screen.getByText("Rows without a model id are not saved."),
    ).toBeInTheDocument();
    // The normalised draft still equals what is stored, so nothing is written.
    await new Promise((resolve) => setTimeout(resolve, 900));
    expect(onUpdate).not.toHaveBeenCalled();
  });

  it("disables the switch and every editor control for a viewer who cannot edit", () => {
    renderInspector(
      agentFixture({
        switchable_models: [
          { model: "claude-opus-5[1m]", role: "default", note: "" },
        ],
      }),
      vi.fn(async () => {}),
      false,
    );

    expect(screen.getByRole("switch", LINEUP_SWITCH)).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    expect(screen.getByLabelText("Model 1 id")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Remove model 1" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Add model" })).toBeDisabled();
  });
});
