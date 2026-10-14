// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, screen } from "@testing-library/react";
import type { Agent, AgentRuntime } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

vi.mock("@multica/core/api", () => ({
  api: {},
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
} as Agent;

describe("AgentDetailInspector execution settings", () => {
  afterEach(() => {
    cleanup();
  });

  function renderInspector(metadata: Record<string, unknown>) {
    renderWithI18n(
      <QueryClientProvider client={new QueryClient()}>
        <AgentDetailInspector
          agent={agent}
          runtimes={[{ id: "runtime-1", metadata } as AgentRuntime]}
          members={[]}
          currentUserId="user-1"
          canEdit
          onUpdate={vi.fn(async () => {})}
        />
      </QueryClientProvider>,
    );
  }

  // A member is a CLI on a computer; the model is chosen per run, so the
  // settings page offers no model, thinking or speed control (DENE-2037).
  it("leads with the CLI and offers no model, thinking or speed control", () => {
    renderInspector({});

    expect(screen.getByText("CLI")).toBeInTheDocument();
    expect(screen.getByTestId("runtime-picker")).toBeInTheDocument();
    // No magpie on the runtime's machine: no model row at all.
    expect(screen.queryByText("Model")).toBeNull();
    expect(screen.queryByText("Thinking")).toBeNull();
    expect(screen.queryByText("Speed")).toBeNull();
  });

  it("shows the magpie route the runtime reported, read-only", () => {
    renderInspector({
      magpie: {
        group: "office-claude",
        models: [
          { id: "claude/claude-opus-5-5", name: "Opus 5.5" },
          { id: "claude/claude-sonnet-5-5", name: "Sonnet 5.5" },
        ],
      },
    });

    expect(screen.getByText("Model")).toBeInTheDocument();
    expect(screen.getByText("office-claude")).toBeInTheDocument();
    expect(screen.getByText("Opus 5.5")).toBeInTheDocument();
    expect(screen.getByText("Sonnet 5.5")).toBeInTheDocument();
  });
});
