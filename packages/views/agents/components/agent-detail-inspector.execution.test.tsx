// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, screen } from "@testing-library/react";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const mockInitiateListModels = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({
  api: {
    initiateListModels: (...args: unknown[]) =>
      mockInitiateListModels(...args),
  },
}));

vi.mock("../../common/avatar-upload-control", () => ({
  AvatarUploadControl: () => <div data-testid="avatar-upload" />,
}));

vi.mock("./inspector/runtime-picker", () => ({
  RuntimePicker: () => <div data-testid="runtime-picker" />,
}));

import { AgentDetailInspector } from "./agent-detail-inspector";

const agent = {
  id: "agent-1",
  workspace_id: "workspace-1",
  name: "Lambda",
  description: "Test agent",
  runtime_id: "runtime-1",
  model: "gpt-6-astra",
  thinking_level: "high",
  service_tier: "priority",
} as Agent;

describe("AgentDetailInspector execution settings", () => {
  afterEach(() => {
    cleanup();
  });

  // A member is a CLI on a computer; the model is chosen per run, so the
  // settings page offers no model, thinking or speed control (DENE-2037).
  it("leads with the CLI and offers no model, thinking or speed control", () => {
    renderWithI18n(
      <QueryClientProvider client={new QueryClient()}>
        <AgentDetailInspector
          agent={agent}
          runtimes={[]}
          members={[]}
          currentUserId="user-1"
          canEdit
          onUpdate={vi.fn(async () => {})}
        />
      </QueryClientProvider>,
    );

    expect(screen.getByText("CLI")).toBeInTheDocument();
    expect(screen.getByTestId("runtime-picker")).toBeInTheDocument();
    expect(
      screen.getByText("Picked by magpie; until then the CLI config decides"),
    ).toBeInTheDocument();
    expect(screen.queryByText("gpt-6-astra")).toBeNull();
    expect(screen.queryByText("Thinking")).toBeNull();
    expect(screen.queryByText("Speed")).toBeNull();
    expect(mockInitiateListModels).not.toHaveBeenCalled();
  });
});
