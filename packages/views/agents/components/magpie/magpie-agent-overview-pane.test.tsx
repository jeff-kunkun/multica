// @vitest-environment jsdom

import { describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent, AgentRuntime } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import {
  NavigationProvider,
  type NavigationAdapter,
} from "../../../navigation";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

const RAIL_SENTINEL = "rail-sentinel";
const GUTTER_SENTINEL = "gutter-sentinel";
vi.mock("../../../layout/page-header", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../layout/page-header")>()),
  PAGE_RAIL: "rail-sentinel",
  PAGE_GUTTER: "gutter-sentinel",
}));

// MagpieAgentOverviewPane pulls in ActorIssuesPanel which in turn touches the api
// layer. The test only cares about which top-of-pane tab buttons render,
// not what each tab does, so we stub the heavy children.
vi.mock("../tabs/activity-tab", () => ({
  ActivityTab: () => <div>activity-tab</div>,
  AgentPerformanceSummary: () => <div>performance-summary</div>,
}));
vi.mock("./magpie-agent-overview-summary", () => ({
  MagpieAgentOverviewSummary: () => <div>agent-overview-summary</div>,
}));
vi.mock("../../../common/actor-issues-panel", () => ({
  ActorIssuesPanel: () => <div>actor-issues-panel</div>,
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));
import { MagpieAgentOverviewPane } from "./magpie-agent-overview-pane";

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
  model: "",
  skills: [],
  visibility: "workspace",
  permission_mode: "public_to",
  invocation_targets: [{ target_type: "workspace", target_id: null }],
  status: "idle",
  max_concurrent_tasks: 1,
  owner_id: "user-1",
  created_at: "2026-05-28T00:00:00Z",
  updated_at: "2026-05-28T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

function makeRuntime(provider: string): AgentRuntime {
  return {
    id: "runtime-1",
    workspace_id: "ws-1",
    daemon_id: null,
    name: "Runtime",
    runtime_mode: "local",
    provider,
    launch_header: "",
    status: "online",
    device_info: "",
    metadata: {},
    owner_id: null,
    visibility: "private",
    last_seen_at: null,
    created_at: "2026-05-28T00:00:00Z",
    updated_at: "2026-05-28T00:00:00Z",
  };
}

function renderPane(runtimes: AgentRuntime[]) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const navigation: NavigationAdapter = {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/agents/agent-1",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path) => path,
  };
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <NavigationProvider value={navigation}>
        <QueryClientProvider client={queryClient}>
          <MagpieAgentOverviewPane
            agent={baseAgent}
            runtime={runtimes[0] ?? null}
            owner={null}
          />
        </QueryClientProvider>
      </NavigationProvider>
    </I18nProvider>,
  );
}

// The office mirrors magpie: no capability tab, and Settings is one read-only
// card with no sub-navigation or editors.
describe("MagpieAgentOverviewPane tabs", () => {
  it("offers Overview and Work only", () => {
    renderPane([makeRuntime("claude")]);
    expect(
      screen.getAllByRole("tab").map((tab) => tab.textContent),
    ).toEqual(["Overview", "Work"]);
  });
});

// MUL-7107: the header, the tab bar and every panel share one leading edge.
// The regression these guard against is a centred width cap: `mx-auto` plus a
// `max-w-*` moves an element's edge as the viewport grows, so chrome on a
// centred rail and a panel on the page gutter agreed at 1440px and drifted
// hundreds of pixels apart above it. A cap must be anchored, never centred.
describe("MagpieAgentOverviewPane horizontal alignment", () => {
  // Every band on the page has to read the SAME rail: chrome on the rail with
  // panels off it is the original bug, and panels on it with chrome off it is
  // the mirror image. Both were shipped once (MUL-7107).
  //
  // The constants are overridden with sentinels rather than compared against
  // their real values, which are ordinary Tailwind classes a hand-written
  // element could match by accident. Only an element that reads the constant
  // picks a sentinel up.
  const panelFor = (container: HTMLElement) =>
    container.querySelector('[role="tablist"]')?.nextElementSibling
      ?.firstElementChild;

  it("puts the tab bar row on the rail", () => {
    const { container } = renderPane([makeRuntime("claude")]);
    const row = container.querySelector('[role="tablist"] > div');

    expect(row).toHaveClass(RAIL_SENTINEL);
    expect(row).toHaveClass(GUTTER_SENTINEL);
  });

  it("puts the Overview panel on the same rail", () => {
    const { container } = renderPane([makeRuntime("claude")]);

    expect(panelFor(container as HTMLElement)).toHaveClass(RAIL_SENTINEL);
    expect(panelFor(container as HTMLElement)).toHaveClass(GUTTER_SENTINEL);
  });

  it("puts the Work panel on the same rail", () => {
    const { container } = renderPane([makeRuntime("claude")]);
    fireEvent.click(screen.getByRole("tab", { name: /^Work$/i }));

    // Work takes a bare rail: the issues toolbar inside it carries the gutter
    // already, so adding one here would inset it past the tabs.
    expect(panelFor(container as HTMLElement)).toHaveClass(RAIL_SENTINEL);
  });
});
