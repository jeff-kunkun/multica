// @vitest-environment jsdom
//
// Component suite for the one-click quota recovery (DENE-466). The rules it
// renders — which account is in effect, whether it is exhausted, and where the
// switch goes — have their canonical layer in
// `tabs/agent-accounts-model.test.ts`; this file checks the wiring only: when
// the prompt appears at all, and how many writes one click performs.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent, RuntimeDevice } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enAgents from "../../locales/en/agents.json";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

const getAgentEnv = vi.hoisted(() => vi.fn());
const updateAgentEnv = vi.hoisted(() => vi.fn());
const updateAgent = vi.hoisted(() => vi.fn());
vi.mock("@multica/core/api", () => ({
  api: { getAgentEnv, updateAgentEnv, updateAgent },
}));

vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

import { AgentAccountQuotaSwitch } from "./agent-account-quota-switch";

const RUNTIME_HOME = "/Users/you";
const NOW = 1_800_000_000_000;
const RESET_AT = Math.floor(NOW / 1000) + 3600;

const baseAgent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Agent",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "local",
  runtime_config: {},
  custom_args: [],
  visibility: "workspace",
  permission_mode: "public_to",
  invocation_targets: [{ target_type: "workspace", target_id: null }],
  status: "idle",
  max_concurrent_tasks: 1,
  model: "",
  owner_id: "user-1",
  skills: [],
  created_at: "2026-05-28T00:00:00Z",
  updated_at: "2026-05-28T00:00:00Z",
  archived_at: null,
  archived_by: null,
} as unknown as Agent;

function account(
  cli: string,
  id: string,
  home: string,
  overrides: Record<string, unknown> = {},
) {
  return {
    cli,
    account: id,
    home,
    base_url: "",
    key_ref: "",
    lever: "",
    signed_in: true,
    quota_reset_at: 0,
    ...overrides,
  };
}

const DSH_DEFAULT = account("dsh", "default", `${RUNTIME_HOME}/.dsh`, {
  lever: "env:DSH_HOME",
});
const DSH_ACCOUNT2 = account("dsh", "account2", `${RUNTIME_HOME}/.dsh-account2`, {
  lever: "env:DSH_HOME",
});

function runtimeWith(provider: string, entries: unknown): RuntimeDevice {
  return {
    id: "runtime-1",
    workspace_id: "ws-1",
    daemon_id: "daemon-1",
    name: "Runtime",
    runtime_mode: "local",
    provider,
    launch_header: "",
    status: "online",
    device_info: "",
    metadata: { home_dir: RUNTIME_HOME, agent_accounts: entries },
    owner_id: null,
    visibility: "private",
    last_seen_at: null,
    created_at: "2026-05-28T00:00:00Z",
    updated_at: "2026-05-28T00:00:00Z",
  } as unknown as RuntimeDevice;
}

function renderSwitch(runtime: RuntimeDevice, agent: Agent = baseAgent) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={queryClient}>
        <AgentAccountQuotaSwitch agent={agent} runtime={runtime} nowMs={NOW} />
      </QueryClientProvider>
    </I18nProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  getAgentEnv.mockResolvedValue({
    agent_id: "agent-1",
    custom_env: { DSH_HOME: DSH_DEFAULT.home, UNRELATED: "keep-me" },
  });
  updateAgentEnv.mockImplementation(
    async (_id: string, body: { custom_env: Record<string, string> }) => ({
      agent_id: "agent-1",
      custom_env: body.custom_env,
    }),
  );
});

describe("AgentAccountQuotaSwitch", () => {
  it("stays out of the way while the account in effect has quota", async () => {
    const { container } = renderSwitch(
      runtimeWith("dsh", [DSH_DEFAULT, DSH_ACCOUNT2]),
    );
    await waitFor(() => expect(getAgentEnv).toHaveBeenCalled());
    await waitFor(() => expect(container).toBeEmptyDOMElement());
  });

  it("prompts and switches in one click when the account is exhausted", async () => {
    renderSwitch(
      runtimeWith("dsh", [
        { ...DSH_DEFAULT, quota_reset_at: RESET_AT },
        DSH_ACCOUNT2,
      ]),
    );

    expect(
      await screen.findByRole("button", { name: /switch to DSH · account2/i }),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /switch to DSH · account2/i }));

    await waitFor(() => expect(updateAgentEnv).toHaveBeenCalledTimes(1));
    // Only the target key moves; an unrelated variable the user set survives.
    expect(updateAgentEnv.mock.calls[0]?.[1]).toEqual({
      custom_env: { DSH_HOME: DSH_ACCOUNT2.home, UNRELATED: "keep-me" },
    });
  });

  it("says so rather than offering a button when every sibling is also out", async () => {
    renderSwitch(
      runtimeWith("dsh", [
        { ...DSH_DEFAULT, quota_reset_at: RESET_AT },
        { ...DSH_ACCOUNT2, quota_reset_at: RESET_AT },
      ]),
    );

    expect(
      await screen.findByText(/no other account of this CLI/i),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("never writes from an untrusted list", async () => {
    // A daemon that reported no accounts is the empty state, and the empty
    // state has no account to call exhausted in the first place.
    const { container } = renderSwitch(runtimeWith("dsh", []));
    await waitFor(() => expect(container).toBeEmptyDOMElement());
    expect(updateAgentEnv).not.toHaveBeenCalled();
  });
});
