import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { AgentDetailInspector } from "./agent-detail-inspector";

vi.mock("../../common/avatar-upload-control", () => ({
  AvatarUploadControl: () => <div data-testid="avatar-upload" />,
}));

vi.mock("./inspector/runtime-picker", () => ({
  RuntimePicker: () => <div data-testid="runtime-picker" />,
}));

const agent = {
  id: "agent-1",
  workspace_id: "workspace-1",
  name: "Lambda",
  description: "Test agent",
  runtime_id: "runtime-1",
} as Agent;

describe("AgentDetailInspector labels", () => {
  afterEach(() => {
    cleanup();
  });

  // Agent labels were removed from the product (MUL-5600). Label Settings no
  // longer manages an agent catalog, so an attach-only picker here would be a
  // dead end pointing at a catalog the user cannot populate.
  it("does not offer a label picker", () => {
    renderWithI18n(
      <AgentDetailInspector
        agent={agent}
        runtimes={[]}
        members={[]}
        currentUserId="user-1"
        canEdit
        onUpdate={vi.fn(async () => {})}
      />,
    );

    // Sanity check: the profile card actually rendered.
    expect(screen.getByLabelText("Name")).toBeInTheDocument();

    expect(screen.queryByTestId("resource-label-picker")).toBeNull();
    expect(screen.queryByText("Labels")).toBeNull();
  });
});
