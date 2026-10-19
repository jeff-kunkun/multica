import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

const mocks = vi.hoisted(() => ({ magpie: true }));

vi.mock("../../../workspace/use-magpie-mode", () => ({ useWorkspaceMagpieEnabled: () => mocks.magpie }));
vi.mock("./magpie-agents-page", () => ({ MagpieAgentsPage: () => <div>magpie agents mirror</div> }));
vi.mock("./magpie-agent-detail-page", () => ({
  MagpieAgentDetailPage: ({ agentId }: { agentId: string }) => <div>magpie detail {agentId}</div>,
}));

import { AgentsPage } from "../agents-page";
import { AgentDetailPage } from "../agent-detail-page";

// The legacy pages keep their own suites (agents-page.test.tsx and
// agent-detail-page.test.tsx), which run with the switch off.
describe("magpie mode switch on the agents pages", () => {
  it("shows the magpie mirror when the workspace is in magpie mode", () => {
    mocks.magpie = true;
    render(<AgentsPage />);
    expect(screen.getByText("magpie agents mirror")).toBeInTheDocument();
  });

  it("shows the magpie detail when the workspace is in magpie mode", () => {
    mocks.magpie = true;
    render(<AgentDetailPage agentId="agent-1" />);
    expect(screen.getByText("magpie detail agent-1")).toBeInTheDocument();
  });
});
