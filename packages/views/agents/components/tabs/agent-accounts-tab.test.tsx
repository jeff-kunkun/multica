// @vitest-environment jsdom
//
// Component suite for the agent "accounts" tab (DENE-308).
//
// The rules this surface renders — parsing, grouping, status mapping, the four
// page states and the switch plan — have one canonical test layer, in node:
// `agent-accounts-model.test.ts`. This file deliberately does NOT re-run that
// matrix through a mount. It checks the wiring only: which state renders which
// control, that the drawer entry disappears in the three untrusted states, and
// how many writes "save and switch" performs.

import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent, RuntimeDevice } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import jaAgents from "../../../locales/ja/agents.json";
import koAgents from "../../../locales/ko/agents.json";
import zhAgents from "../../../locales/zh-Hans/agents.json";
import {
  NavigationProvider,
  type NavigationAdapter,
} from "../../../navigation";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/paths", () => ({
  paths: { workspace: () => ({ runtimes: () => "/acme/runtimes" }) },
  useWorkspaceSlug: () => "acme",
}));

const getAgentEnv = vi.hoisted(() => vi.fn());
const updateAgentEnv = vi.hoisted(() => vi.fn());
vi.mock("@multica/core/api", () => ({
  api: { getAgentEnv, updateAgentEnv },
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

vi.mock("@multica/ui/lib/clipboard", () => ({
  copyText: vi.fn().mockResolvedValue(true),
}));

import { toast } from "sonner";
import { AgentAccountsTab } from "./agent-accounts-tab";

const RUNTIME_HOME = "/Users/you";

const baseAgent: Agent = {
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
};

/** One `agent_accounts` entry exactly as DENE-306 reports it. */
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

const AGY_DEFAULT = account("agy", "default", `${RUNTIME_HOME}/.gemini`, {
  lever: "custom_args:--gemini_dir",
});
const AGY_ACCOUNT2 = account("agy", "account2", `${RUNTIME_HOME}/.gemini-account2`, {
  lever: "custom_args:--gemini_dir",
});
const DSH_DEFAULT = account("dsh", "default", `${RUNTIME_HOME}/.dsh`, {
  lever: "env:DSH_HOME",
});
const DSH_ACCOUNT2 = account("dsh", "account2", `${RUNTIME_HOME}/.dsh-account2`, {
  lever: "env:DSH_HOME",
});
const CODEX_DEFAULT = account("codex", "default", `${RUNTIME_HOME}/.codex`, {
  lever: "",
});

function runtimeWith(
  provider: string,
  entries: unknown,
  error?: string,
): RuntimeDevice {
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
    metadata: {
      home_dir: RUNTIME_HOME,
      agent_accounts: entries,
      ...(error === undefined ? {} : { agent_accounts_error: error }),
    },
    owner_id: null,
    visibility: "private",
    last_seen_at: null,
    created_at: "2026-05-28T00:00:00Z",
    updated_at: "2026-05-28T00:00:00Z",
  };
}

function makeNavigation(): NavigationAdapter {
  return {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/agents/agent-1",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path) => path,
  };
}

/**
 * Stands in for the detail page: it applies the update the tab hands to
 * `onSave` to its own agent state, which is what `handleUpdate`'s optimistic
 * cache patch does in production.
 */
function Harness({
  agent,
  device,
  onSave,
}: {
  agent: Agent;
  device?: RuntimeDevice;
  onSave: (updates: Partial<Agent>) => Promise<void>;
}) {
  const [current, setCurrent] = useState(agent);
  return (
    <AgentAccountsTab
      agent={current}
      runtimeDevice={device}
      onSave={async (updates) => {
        await onSave(updates);
        setCurrent((prev) => ({ ...prev, ...updates }));
      }}
    />
  );
}

function renderTab({
  agent = baseAgent,
  device,
  onSave = vi.fn().mockResolvedValue(undefined),
}: {
  agent?: Agent;
  device?: RuntimeDevice;
  onSave?: (updates: Partial<Agent>) => Promise<void>;
} = {}) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const navigation = makeNavigation();
  const result = render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <NavigationProvider value={navigation}>
        <QueryClientProvider client={queryClient}>
          <Harness agent={agent} device={device} onSave={onSave} />
        </QueryClientProvider>
      </NavigationProvider>
    </I18nProvider>,
  );
  return { ...result, onSave, navigation };
}

// A report carrying an env-lever account renders the loading state until the
// env query settles, so the entry point has to be awaited rather than queried
// eagerly.
async function openDrawer() {
  fireEvent.click(
    await screen.findByRole("button", { name: /manage accounts/i }),
  );
}

function selectAccount(name: RegExp) {
  fireEvent.click(screen.getByRole("radio", { name }));
}

function saveAndSwitch() {
  fireEvent.click(screen.getByRole("button", { name: /save and switch/i }));
}

beforeEach(() => {
  vi.clearAllMocks();
  getAgentEnv.mockResolvedValue({ agent_id: "agent-1", custom_env: {} });
  // The env endpoint echoes the map it accepted; the tab caches that answer, so
  // echoing keeps the fake faithful to the server.
  updateAgentEnv.mockImplementation(
    async (_id: string, body: { custom_env: Record<string, string> }) => ({
      agent_id: "agent-1",
      custom_env: body.custom_env,
    }),
  );
});

describe("AgentAccountsTab rest state", () => {
  it("shows the account in effect and lists the others as read-only chips", async () => {
    renderTab({
      agent: {
        ...baseAgent,
        custom_args: ["--gemini_dir", AGY_ACCOUNT2.home],
      },
      device: runtimeWith("antigravity", [AGY_DEFAULT, AGY_ACCOUNT2, DSH_DEFAULT]),
    });

    expect(await screen.findByText("Antigravity · account2")).toBeInTheDocument();
    expect(screen.getByText("In use")).toBeInTheDocument();
    expect(
      screen.getByText(`--gemini_dir=${AGY_ACCOUNT2.home}`),
    ).toBeInTheDocument();

    // Non-current accounts are chips, never controls: clicking one must not
    // switch the account.
    const chip = screen.getByText("DSH · default");
    expect(chip.closest("button")).toBeNull();
    expect(screen.getByText("Antigravity · default")).toBeInTheDocument();
  });

  it("reads the env endpoint only when an account is bound through an env key", async () => {
    // That endpoint is audited server-side, so a machine whose accounts all use
    // the custom_args lever must not pay for it.
    renderTab({
      device: runtimeWith("antigravity", [AGY_DEFAULT, AGY_ACCOUNT2]),
    });

    expect(
      await screen.findByText("Antigravity · default"),
    ).toBeInTheDocument();
    expect(getAgentEnv).not.toHaveBeenCalled();
  });

  it("opens the drawer from the one primary entry point", async () => {
    renderTab({
      device: runtimeWith("antigravity", [AGY_DEFAULT, AGY_ACCOUNT2]),
    });

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await openDrawer();

    const drawer = screen.getByRole("dialog", { name: /manage accounts/i });
    expect(drawer).toBeInTheDocument();
    expect(screen.getByText("1 CLIs · 2 accounts")).toBeInTheDocument();
    // Group header carries the binding lever.
    expect(screen.getByText("--gemini_dir")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /save and switch/i }),
    ).toBeInTheDocument();
  });

  it("describes the binding it read from the env endpoint", async () => {
    getAgentEnv.mockResolvedValue({
      agent_id: "agent-1",
      custom_env: { DSH_HOME: DSH_ACCOUNT2.home, KEEP: "untouched" },
    });
    renderTab({
      device: runtimeWith("dsh", [DSH_DEFAULT, DSH_ACCOUNT2]),
    });

    expect(await screen.findByText("DSH · account2")).toBeInTheDocument();
    expect(
      screen.getByText(`DSH_HOME=${DSH_ACCOUNT2.home}`),
    ).toBeInTheDocument();
  });
});

describe("AgentAccountsTab untrusted states keep the drawer shut", () => {
  it("renders the empty state with its three entry points and no drawer entry", () => {
    renderTab({ device: runtimeWith("dsh", []) });

    expect(
      screen.getByText("This agent has no accounts yet"),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /create the first dsh account/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /use an agy account/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /i already have a directory/i }),
    ).toBeInTheDocument();
    expect(screen.getByText(/available lever DSH_HOME/)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /manage accounts/i }),
    ).not.toBeInTheDocument();
  });

  it("renders the loading skeleton until the runtime row arrives", () => {
    renderTab({ device: undefined });

    expect(
      screen.getByText("Reading account directories and sign-in state…"),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /manage accounts/i }),
    ).not.toBeInTheDocument();
  });

  it("renders the error state with the raw reason and no drawer entry", () => {
    renderTab({
      device: runtimeWith(
        "dsh",
        [],
        "dial tcp 127.0.0.1:7433: connect: connection refused",
      ),
    });

    expect(screen.getByText("Can't read local account state")).toBeInTheDocument();
    expect(
      screen.getByText("dial tcp 127.0.0.1:7433: connect: connection refused"),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /^retry$/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /view daemon status/i }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /manage accounts/i }),
    ).not.toBeInTheDocument();
  });
});

describe("AgentAccountsTab drawer", () => {
  it("keeps a CLI with no lever read-only instead of offering a dead switch", async () => {
    renderTab({
      device: runtimeWith("dsh", [DSH_DEFAULT, DSH_ACCOUNT2, CODEX_DEFAULT]),
    });
    await openDrawer();

    expect(
      screen.getByText("This CLI cannot be switched from here yet."),
    ).toBeInTheDocument();
    // Only the two dsh rows are selectable; the codex row is static.
    expect(screen.getAllByRole("radio")).toHaveLength(2);
    expect(
      screen.getByText(`${RUNTIME_HOME}/.codex`).closest("button"),
    ).toBeNull();
  });

  it("writes only the target env key on save and switch", async () => {
    getAgentEnv.mockResolvedValue({
      agent_id: "agent-1",
      custom_env: { KEEP: "untouched" },
    });
    const { onSave } = renderTab({
      device: runtimeWith("dsh", [DSH_DEFAULT, DSH_ACCOUNT2]),
    });

    await screen.findByText("DSH · default");
    await openDrawer();
    selectAccount(/account2/);
    saveAndSwitch();

    // The summary bar reads the new binding; "DSH · account2" alone would also
    // match the read-only chip, so assert on the lever line the bar owns.
    expect(
      await screen.findByText(`DSH_HOME=${DSH_ACCOUNT2.home}`),
    ).toBeInTheDocument();
    expect(updateAgentEnv).toHaveBeenCalledTimes(1);
    // Every other variable is written back untouched; only DSH_HOME moves.
    expect(updateAgentEnv).toHaveBeenCalledWith("agent-1", {
      custom_env: { KEEP: "untouched", DSH_HOME: DSH_ACCOUNT2.home },
    });
    // An env-lever switch never travels through PUT /api/agents/{id}.
    expect(onSave).not.toHaveBeenCalled();
    expect(toast.success).toHaveBeenCalled();
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("writes the agy binding through the agent and updates the summary", async () => {
    const { onSave } = renderTab({
      device: runtimeWith("antigravity", [AGY_DEFAULT, AGY_ACCOUNT2]),
    });

    await openDrawer();
    selectAccount(/account2/);
    saveAndSwitch();

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave).toHaveBeenCalledWith({
      custom_args: ["--gemini_dir", AGY_ACCOUNT2.home],
    });
    expect(updateAgentEnv).not.toHaveBeenCalled();
    // The bar owns this line, so it proves the summary — not a chip — moved.
    expect(
      await screen.findByText(`--gemini_dir=${AGY_ACCOUNT2.home}`),
    ).toBeInTheDocument();
  });

  it("sends nothing when the target is already the account in effect", async () => {
    renderTab({
      device: runtimeWith("antigravity", [AGY_DEFAULT, AGY_ACCOUNT2]),
      agent: {
        ...baseAgent,
        custom_args: ["--gemini_dir", AGY_DEFAULT.home],
      },
    });

    await openDrawer();
    // Opening pre-selects the account in effect, so there is nothing to save.
    expect(
      screen.getByRole("button", { name: /save and switch/i }),
    ).toBeDisabled();
    saveAndSwitch();

    expect(getAgentEnv).not.toHaveBeenCalled();
    expect(updateAgentEnv).not.toHaveBeenCalled();
  });

  it("forgets a selection that was never saved", async () => {
    renderTab({
      device: runtimeWith("antigravity", [AGY_DEFAULT, AGY_ACCOUNT2]),
    });

    await openDrawer();
    selectAccount(/account2/);
    expect(
      screen.getByRole("button", { name: /save and switch/i }),
    ).toBeEnabled();

    // Closing the drawer drops the pending selection; nothing was written.
    fireEvent.click(screen.getByRole("button", { name: /close/i }));
    await openDrawer();
    expect(
      screen.getByRole("button", { name: /save and switch/i }),
    ).toBeDisabled();
    expect(
      screen.getByRole("radio", { name: /default/ }),
    ).toHaveAttribute("aria-checked", "true");
  });
});

// The global sweep for every namespace and locale lives in
// `packages/views/locales/parity.test.ts`. This is the narrow guard for the
// keys THIS surface added: all four bundles must carry the same account keys,
// or one locale silently falls back to English.
describe("agents locale bundles", () => {
  it("ship the same accounts keys in all four locales", () => {
    const bundles = {
      en: enAgents,
      "zh-Hans": zhAgents,
      ja: jaAgents,
      ko: koAgents,
    };
    const keySet = (bundle: typeof enAgents) =>
      Object.keys(
        (bundle as { tab_body: { accounts: Record<string, unknown> } }).tab_body
          .accounts,
      ).sort();

    const expected = keySet(enAgents);
    expect(expected.length).toBeGreaterThan(0);
    for (const [locale, bundle] of Object.entries(bundles)) {
      expect(keySet(bundle as typeof enAgents), locale).toEqual(expected);
      expect(
        (bundle as { tabs: Record<string, unknown> }).tabs.accounts,
        locale,
      ).toBeTruthy();
    }
  });
});
