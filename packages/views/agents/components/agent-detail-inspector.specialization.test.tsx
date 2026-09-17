// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { WorkspaceSlugProvider } from "@multica/core/paths";
import type { Agent, AgentRuntime } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import { AgentDetailInspector } from "./agent-detail-inspector";

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: () => ({ data: undefined, isSuccess: false }),
}));

vi.mock("../../common/avatar-upload-control", () => ({
  AvatarUploadControl: () => <div data-testid="avatar-upload" />,
}));

// The pickers own their own read-only rendering. What this suite pins is the
// wiring that makes them read-only and feeds them the base role's values, so
// each stub reports exactly what it was handed.
const controlProps = vi.hoisted(() => ({
  runtime: null as { canEdit?: boolean; value?: string } | null,
  model: null as { canEdit?: boolean; value?: string } | null,
  thinking: null as { canEdit?: boolean; value?: string } | null,
  serviceTier: null as { canEdit?: boolean; value?: string } | null,
}));

vi.mock("./inspector/runtime-picker", () => ({
  RuntimePicker: (props: { canEdit?: boolean; value?: string }) => {
    controlProps.runtime = props;
    return <div data-testid="runtime-picker" />;
  },
}));

vi.mock("./inspector/model-picker", () => ({
  ModelPicker: (props: { canEdit?: boolean; value?: string }) => {
    controlProps.model = props;
    return <div data-testid="model-picker" />;
  },
}));

vi.mock("./inspector/thinking-prop-row", () => ({
  ThinkingSettingField: (props: { canEdit?: boolean; value?: string }) => {
    controlProps.thinking = props;
    return <div data-testid="thinking-field" />;
  },
}));

vi.mock("./inspector/service-tier-setting-field", () => ({
  ServiceTierSettingField: (props: { canEdit?: boolean; value?: string }) => {
    controlProps.serviceTier = props;
    return <div data-testid="service-tier-field" />;
  },
}));

const baseRoleRuntime = {
  id: "runtime-base",
  workspace_id: "workspace-1",
  daemon_id: "daemon-1",
  name: "Base runtime",
  runtime_mode: "local",
  provider: "claude",
  launch_header: "",
  status: "online",
  device_info: "Mac",
  metadata: {},
  owner_id: "user-1",
  visibility: "private",
  last_seen_at: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
} satisfies AgentRuntime;

const baseRole = {
  id: "base-1",
  workspace_id: "workspace-1",
  name: "Base Role",
  description: "",
  runtime_id: baseRoleRuntime.id,
  model: "base-model",
  thinking_level: "high",
  service_tier: "fast",
  max_concurrent_tasks: 7,
  auto_retry_enabled: false,
} as Agent;

// Deliberately different from the base role's, so a control that leaked the
// child's own values fails the assertions below.
const specialization = {
  id: "child-1",
  workspace_id: "workspace-1",
  name: "Lambda Variant",
  description: "",
  runtime_id: "runtime-child",
  model: "child-model",
  thinking_level: "low",
  service_tier: "standard",
  max_concurrent_tasks: 2,
  auto_retry_enabled: true,
  parent_agent_id: baseRole.id,
  parent_agent_name: baseRole.name,
} as Agent;

const navigationAdapter: NavigationAdapter = {
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/acme/agents/child-1",
  searchParams: new URLSearchParams(),
  hash: "",
  getShareableUrl: (path: string) => `https://app.test${path}`,
};

function renderInspector({
  agent,
  parentAgent = null,
  onUpdate = vi.fn(async () => {}),
}: {
  agent: Agent;
  parentAgent?: Agent | null;
  onUpdate?: (id: string, data: Record<string, unknown>) => Promise<void>;
}) {
  renderWithI18n(
    <WorkspaceSlugProvider slug="acme">
      <NavigationProvider value={navigationAdapter}>
        <AgentDetailInspector
          agent={agent}
          runtime={null}
          runtimes={[baseRoleRuntime]}
          members={[]}
          currentUserId="user-1"
          canEdit
          onUpdate={onUpdate}
          parentAgent={parentAgent}
        />
      </NavigationProvider>
    </WorkspaceSlugProvider>,
  );
  return { onUpdate };
}

describe("AgentDetailInspector specialization execution settings", () => {
  afterEach(() => {
    cleanup();
    controlProps.runtime = null;
    controlProps.model = null;
    controlProps.thinking = null;
    controlProps.serviceTier = null;
  });

  // DENE-470: the whole execution section is inherited, so every control is
  // read-only and the values on screen are the base role's.
  it("renders the base role's values read-only and links to the base role", async () => {
    const user = userEvent.setup();
    const { onUpdate } = renderInspector({
      agent: specialization,
      parentAgent: baseRole,
    });

    expect(screen.getByText(/inherited from/i)).toHaveTextContent("Base Role");
    expect(screen.getByRole("link", { name: "Open base role" })).toHaveAttribute(
      "href",
      "/acme/agents/base-1",
    );

    expect(controlProps.runtime).toMatchObject({
      value: "runtime-base",
      canEdit: false,
    });
    expect(controlProps.model).toMatchObject({
      value: "base-model",
      canEdit: false,
    });
    expect(controlProps.thinking).toMatchObject({
      value: "high",
      canEdit: false,
    });
    expect(controlProps.serviceTier).toMatchObject({
      value: "fast",
      canEdit: false,
    });

    // Real controls, so "cannot edit" is asserted against the DOM rather than
    // against a prop: the concurrency field is the base role's value and both
    // it and the auto-retry switch refuse the interaction.
    expect(screen.getByLabelText("Concurrency")).toHaveValue(7);
    expect(screen.getByLabelText("Concurrency")).toBeDisabled();
    expect(screen.getByRole("switch", { name: "Auto retry" })).not.toBeChecked();
    expect(screen.getByRole("switch", { name: "Auto retry" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );

    await user.click(screen.getByRole("switch", { name: "Auto retry" }));
    expect(onUpdate).not.toHaveBeenCalled();
  });

  it("stays read-only when the base role is not in the loaded list", () => {
    renderInspector({ agent: specialization, parentAgent: null });

    // The row is private to another member, so the served name is all there
    // is — the notice must still name it and point at its detail page.
    expect(screen.getByText(/inherited from/i)).toHaveTextContent("Base Role");
    expect(screen.getByRole("link", { name: "Open base role" })).toHaveAttribute(
      "href",
      "/acme/agents/base-1",
    );

    expect(controlProps.runtime?.canEdit).toBe(false);
    expect(controlProps.model?.canEdit).toBe(false);
    expect(screen.getByLabelText("Concurrency")).toBeDisabled();
    expect(screen.getByRole("switch", { name: "Auto retry" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
  });

  it("keeps a base role's execution settings editable", () => {
    renderInspector({ agent: baseRole });

    expect(screen.queryByText(/inherited from/i)).toBeNull();
    expect(controlProps.runtime?.canEdit).toBe(true);
    expect(controlProps.model?.canEdit).toBe(true);
    expect(screen.getByLabelText("Concurrency")).not.toBeDisabled();
    expect(screen.getByRole("switch", { name: "Auto retry" })).not.toHaveAttribute(
      "aria-disabled",
      "true",
    );
  });
});
