// Wiring, accessibility and the named regressions for the routing section.
// The four-state matrix and every malformed-settings case are the canonical
// business of packages/core/workspace/routing-settings.test.ts and are NOT
// re-run through a DOM mount here.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";

const updateWorkspace = vi.hoisted(() => vi.fn());
const getRoutingHealth = vi.hoisted(() => vi.fn());
const checkRoutingHealth = vi.hoisted(() => vi.fn());
const member = vi.hoisted(() => ({ role: "owner" as "owner" | "admin" | "member" }));
const workspace = vi.hoisted(() => ({
  current: {
    id: "ws-1",
    name: "Acme",
    slug: "acme",
    settings: {} as Record<string, unknown>,
  },
}));

vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/api")>();
  return {
    ...actual,
    api: { updateWorkspace, getRoutingHealth, checkRoutingHealth },
  };
});

vi.mock("@multica/core/paths", async (importOriginal) => ({
  paths: (await importOriginal<typeof import("@multica/core/paths")>()).paths,
  useCurrentWorkspace: () => workspace.current,
}));

vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ role: member.role, isLoading: false }),
}));

import { parseRoutingHealth } from "@multica/core/workspace/routing-health";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { RoutingTab } from "./routing-tab";

function render() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  // The query provider is nested INSIDE the element rather than passed as
  // `wrapper`: renderWithI18n supplies its own wrapper, and a second one here
  // would replace it, leaving every label an empty string.
  const result = renderWithI18n(
    <QueryClientProvider client={qc}>
      <RoutingTab />
    </QueryClientProvider>,
  );
  return { ...result, qc };
}

/**
 * Wait until the health report has actually landed in the cache.
 *
 * Necessary because the pre-fetch render and the "unreadable report" render
 * are deliberately identical — asserting before the query settles would pass
 * on the empty render whatever the server said.
 */
async function healthSettled(qc: QueryClient) {
  await waitFor(() =>
    expect(qc.getQueryData(workspaceKeys.routingHealth("ws-1"))).toBeDefined(),
  );
}

const HEALTHY = {
  state: "enabled" as const,
  usable: true,
  reason: "",
  retry_after_seconds: 0,
  last_success_at: Math.floor(Date.now() / 1000) - 120,
  last_failure_at: 0,
  model: "gpt-5.6-luna",
  threshold: 0.7,
  gateway_host: "api.openai.com",
  gateway_default_model: "gpt-5.6-mini",
  gateway_configured: true,
};

beforeEach(() => {
  updateWorkspace.mockReset();
  getRoutingHealth.mockReset();
  getRoutingHealth.mockResolvedValue(HEALTHY);
  checkRoutingHealth.mockReset();
  checkRoutingHealth.mockResolvedValue(HEALTHY);
  updateWorkspace.mockImplementation(async (_id: string, body: { settings?: unknown }) => ({
    ...workspace.current,
    settings: body.settings,
  }));
  member.role = "owner";
  workspace.current = { id: "ws-1", name: "Acme", slug: "acme", settings: {} };
});

function chip() {
  return document.querySelector("[data-state]") as HTMLElement | null;
}

describe("RoutingTab", () => {
  it("shows the off state for a workspace that has never configured routing", () => {
    render();
    expect(chip()?.getAttribute("data-state")).toBe("off");
  });

  // The regression this state exists for: somebody flips the switch, walks
  // away, and believes routing is working while the product is unchanged.
  it("shows incomplete — not enabled — when the switch is on with no model", () => {
    workspace.current.settings = { routing: { enabled: true, model: "" } };
    render();
    expect(chip()?.getAttribute("data-state")).toBe("incomplete");
  });

  it("shows enabled once a model is chosen", () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna", confidence_threshold: 0.8 },
    };
    render();
    expect(chip()?.getAttribute("data-state")).toBe("enabled");
  });

  it("saves the three fields under the routing key and leaves the rest of settings alone", async () => {
    workspace.current.settings = { theme: "dark" };
    render();

    const model = screen.getByLabelText(/routing model|路由模型/i);
    await userEvent.type(model, "gpt-5.6-luna");

    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const [, body] = updateWorkspace.mock.calls.at(-1) as [
      string,
      { settings: Record<string, unknown> },
    ];
    expect(body.settings.theme).toBe("dark");
    expect(body.settings.routing).toEqual({
      enabled: false,
      model: "gpt-5.6-luna",
      confidence_threshold: 0.7,
    });
  });

  it("switching routing on writes enabled without inventing a model", async () => {
    render();
    await userEvent.click(screen.getByRole("switch"));

    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const [, body] = updateWorkspace.mock.calls.at(-1) as [
      string,
      { settings: { routing: { enabled: boolean; model: string } } },
    ];
    expect(body.settings.routing.enabled).toBe(true);
    expect(body.settings.routing.model).toBe("");
  });

  it("stores no credential field of any kind", () => {
    render();
    // The section must never grow a key/token input: credentials belong to the
    // deployment's model configuration.
    expect(document.querySelector('input[type="password"]')).toBeNull();
    expect(screen.queryByLabelText(/api key|secret|token/i)).toBeNull();
  });

  it("is read-only for a plain member", async () => {
    member.role = "member";
    render();
    // Base UI's switch marks the disabled state with data-disabled rather
    // than the native attribute, so assert the behaviour too: a member who
    // clicks it must not produce a write.
    expect(screen.getByRole("switch")).toHaveAttribute("data-disabled");
    expect(screen.getByLabelText(/routing model|路由模型/i)).toBeDisabled();

    await userEvent.click(screen.getByRole("switch"));
    await new Promise((resolve) => setTimeout(resolve, 800));
    expect(updateWorkspace).not.toHaveBeenCalled();
  });

  it("greys out the threshold while routing is off so it cannot look active", () => {
    render();
    expect(screen.getByLabelText(/confidence threshold|置信度阈值/i)).toBeDisabled();
  });

  // The regression the fourth state exists for: a configured workspace whose
  // model is rejected or cooling down. Routing keeps that off every ticket by
  // design, so a green chip here would leave the failure visible nowhere at
  // all outside the server log (DENE-633 review, F2).
  it("shows ineffective, with the reason, when the server reports the model unusable", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna", confidence_threshold: 0.7 },
    };
    getRoutingHealth.mockResolvedValue({
      ...HEALTHY,
      state: "ineffective",
      usable: false,
      reason: "the routing model rejected our credentials (401)",
      retry_after_seconds: 240,
      last_failure_at: Math.floor(Date.now() / 1000),
    });
    render();

    await waitFor(() => expect(chip()?.getAttribute("data-state")).toBe("ineffective"));
    expect(screen.getByText(/rejected our credentials \(401\)/)).toBeTruthy();
  });

  it("dates the connection while healthy", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna", confidence_threshold: 0.7 },
    };
    render();
    expect(await screen.findByText(/self-checked|自检/)).toBeTruthy();
    expect(getRoutingHealth).toHaveBeenCalledWith("ws-1");
  });

  // The box holds a bare model id, so "which model is this, on whose
  // endpoint?" has to be answerable from the section itself. The endpoint is
  // deployment config and cannot be edited here — which is why naming it is
  // the whole point (canonical parsing matrix:
  // packages/core/workspace/routing-health.test.ts).
  it("names the endpoint the model id is sent to", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna", confidence_threshold: 0.7 },
    };
    const { qc } = render();
    await healthSettled(qc);
    expect(await screen.findByText(/api\.openai\.com/)).toBeTruthy();
    expect(screen.getByText(/gpt-5\.6-mini/)).toBeTruthy();
  });

  // The single most common reason routing silently does nothing, and the one
  // a workspace admin cannot fix from this screen.
  it("warns when the deployment has no internal LLM at all", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna", confidence_threshold: 0.7 },
    };
    getRoutingHealth.mockResolvedValue({
      ...HEALTHY,
      gateway_host: "",
      gateway_default_model: "",
      gateway_configured: false,
    });
    const { qc } = render();
    await healthSettled(qc);
    expect(await screen.findByText(/MULTICA_LLM_BASE_URL/)).toBeTruthy();
  });

  it("re-checks on demand and adopts the fresh report", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna", confidence_threshold: 0.7 },
    };
    getRoutingHealth.mockResolvedValue({
      ...HEALTHY,
      state: "ineffective",
      usable: false,
      reason: "the routing model is rate limiting us (429)",
      retry_after_seconds: 120,
    });
    render();
    await waitFor(() => expect(chip()?.getAttribute("data-state")).toBe("ineffective"));

    await userEvent.click(screen.getByRole("button", { name: /re-check|重新自检/i }));
    await waitFor(() => expect(chip()?.getAttribute("data-state")).toBe("enabled"));
  });

  it("offers no re-check button to a plain member", async () => {
    member.role = "member";
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna", confidence_threshold: 0.7 },
    };
    render();
    await waitFor(() => expect(getRoutingHealth).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: /re-check|重新自检/i })).toBeNull();
  });

  // The regression that the first real acceptance run would have hit: the
  // health report cached a moment ago still describes the switched-off
  // workspace, and "not usable" there means "routing is off", not "the model
  // is broken" (DENE-633 review, N1).
  it("does not report a fault while health still describes the settings being replaced", async () => {
    getRoutingHealth.mockResolvedValue({
      ...HEALTHY,
      state: "off",
      usable: false,
      last_success_at: 0,
    });
    const { qc } = render();
    await healthSettled(qc);

    await userEvent.click(screen.getByRole("switch"));
    await userEvent.type(
      screen.getByLabelText(/routing model|路由模型/i),
      "gpt-5.6-luna",
    );

    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    expect(chip()?.getAttribute("data-state")).toBe("enabled");
  });

  it("re-reads health after a save so the chip follows what was just written", async () => {
    render();
    await waitFor(() => expect(getRoutingHealth).toHaveBeenCalledTimes(1));

    await userEvent.click(screen.getByRole("switch"));

    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    // Without the invalidation the settings the server derives health from
    // have changed but the cached report has not, and the chip describes the
    // previous configuration until the next poll.
    await waitFor(() => expect(getRoutingHealth).toHaveBeenCalledTimes(2));
  });

  // A response this client cannot read means it does not know — which is
  // neither a fault to report nor a connection to claim. Fed through the real
  // fallback so the test moves if that fallback does.
  it("neither reports a fault nor claims a connection for an unreadable report", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna", confidence_threshold: 0.7 },
    };
    getRoutingHealth.mockResolvedValue(parseRoutingHealth({ garbage: true }));
    const { qc } = render();

    await healthSettled(qc);
    expect(chip()?.getAttribute("data-state")).toBe("enabled");
    expect(screen.getByText(/No call made since|还没有发起过调用/)).toBeTruthy();
    expect(screen.queryByText(/Connected ·|已连通 ·/)).toBeNull();
  });

  // Switching off must not keep a red chip about a model the workspace is no
  // longer using: the draft decides whether routing is on at all, and health
  // only splits enabled from ineffective.
  it("returns to off immediately when the switch is turned off", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna", confidence_threshold: 0.7 },
    };
    getRoutingHealth.mockResolvedValue({
      ...HEALTHY,
      state: "ineffective",
      usable: false,
      reason: "unreachable",
    });
    render();
    await waitFor(() => expect(chip()?.getAttribute("data-state")).toBe("ineffective"));

    await userEvent.click(screen.getByRole("switch"));
    expect(chip()?.getAttribute("data-state")).toBe("off");
  });
});
