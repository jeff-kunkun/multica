// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent, AgentRuntime } from "@multica/core/types";
import { agentListOptions } from "@multica/core/workspace/queries";
import { runtimeListOptions } from "@multica/core/runtimes";

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "ws-1", slug: "acme" }),
}));

import { AgentCliAvatar } from "./agent-cli-avatar";

function renderAvatar(runtimes: Partial<AgentRuntime>[]) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { staleTime: Infinity } },
  });
  queryClient.setQueryData(agentListOptions("ws-1").queryKey, [
    { id: "agent-1", runtime_id: "rt-1" } as Agent,
  ]);
  queryClient.setQueryData(
    runtimeListOptions("ws-1").queryKey,
    runtimes as AgentRuntime[],
  );
  return render(
    <QueryClientProvider client={queryClient}>
      <AgentCliAvatar
        agentId="agent-1"
        name="Lambda"
        initials="L"
        avatarUrl="https://example.com/frog.png"
        isAgent
      />
    </QueryClientProvider>,
  );
}

describe("AgentCliAvatar", () => {
  it("wears the CLI's logo instead of the uploaded picture", () => {
    const { container } = renderAvatar([{ id: "rt-1", provider: "claude" }]);
    expect(container.querySelector("img")).toBeNull();
    expect(
      container.querySelector('svg[fill="#D97757"]'),
    ).toBeInTheDocument();
  });

  it("falls back to the bot, never the uploaded picture, without a runtime", () => {
    const { container } = renderAvatar([]);
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("svg.lucide-bot")).toBeInTheDocument();
  });
});
