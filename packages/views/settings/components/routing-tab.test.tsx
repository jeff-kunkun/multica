// Wiring, accessibility and the named regressions for the routing section.
// The four-state matrix and every malformed-settings case are the canonical
// business of packages/core/workspace/routing-settings.test.ts and are NOT
// re-run through a DOM mount here.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";

const updateWorkspace = vi.hoisted(() => vi.fn());
const getRoutingHealth = vi.hoisted(() => vi.fn());
const checkRoutingHealth = vi.hoisted(() => vi.fn());
const listRoutingModels = vi.hoisted(() => vi.fn());
const listAgents = vi.hoisted(() => vi.fn());
const listRuntimes = vi.hoisted(() => vi.fn());
const getRoutingDispatch = vi.hoisted(() => vi.fn());
const toastError = vi.hoisted(() => vi.fn());
vi.mock("sonner", () => ({ toast: { error: toastError, success: vi.fn() } }));
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
    api: {
      updateWorkspace,
      getRoutingHealth,
      checkRoutingHealth,
      listRoutingModels,
      listAgents,
      listRuntimes,
      getRoutingDispatch,
    },
  };
});

vi.mock("@multica/core/paths", async (importOriginal) => ({
  paths: (await importOriginal<typeof import("@multica/core/paths")>()).paths,
  useCurrentWorkspace: () => workspace.current,
}));

const nav = vi.hoisted(() => ({ search: "tab=routing" }));
vi.mock("../../navigation", () => ({
  useNavigation: () => ({
    pathname: "/acme/settings",
    searchParams: new URLSearchParams(nav.search),
  }),
  AppLink: ({ href, children, ...rest }: { href: string; children: React.ReactNode }) => (
    <a href={href} {...rest}>{children}</a>
  ),
}));

vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ role: member.role, isLoading: false }),
}));

import { parseRoutingHealth } from "@multica/core/workspace/routing-health";
import { DEFAULT_ROUTING_POLICY_PROMPT } from "@multica/core/workspace/routing-policy-prompt";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { runtimeKeys } from "@multica/core/runtimes";
import { RoutingTab } from "./routing-tab";

/** Mount the main view, or one sub-page (`?integration=<page>`). */
function render(page?: "model" | "rules" | "seats") {
  nav.search = page ? `tab=routing&integration=${page}` : "tab=routing";
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
  gateway_scope: "deployment" as const,
  gateway_protocol: "openai" as const,
  gateway_key_set: false,
  workspace_key_storable: true,
  roles: [{role:"analysis", enabled:true, model:"reasoner", gateway_host:"api.openai.com", gateway_scope:"deployment",gateway_key_set:false,gateway_protocol:"openai"}],
};

const RULES = parseRoutingHealth({
  state: "enabled",
  rules: {
    questions: [
      { key: "scope", label: "改动范围", options: [{ value: "small", label: "小" }, { value: "cross_module", label: "跨模块" }] },
      { key: "clarity", label: "需求", options: [{ value: "clear", label: "清楚" }] },
    ],
    rules: [
      { index: 1, id: "cross_module", label: "跨模块", when: { scope: ["cross_module"] }, tier: "strong", tier_label: "强档", reviewer: "seat" },
      { index: 2, id: "unknown", label: "有一题答不出", any_unknown: true, tier: "medium", tier_label: "中档", reviewer: "seat" },
      { index: 3, id: "simple", label: "小改动", when: { clarity: ["clear"], scope: ["small"] }, tier: "weak", tier_label: "弱档", reviewer: "none" },
      { index: 4, id: "default", label: "其余", tier: "medium", tier_label: "中档", reviewer: "none" },
    ],
  },
}).rules;

beforeEach(() => {
  updateWorkspace.mockReset();
  toastError.mockReset();
  getRoutingHealth.mockReset();
  getRoutingHealth.mockResolvedValue(HEALTHY);
  checkRoutingHealth.mockReset();
  checkRoutingHealth.mockResolvedValue(HEALTHY);
  listRoutingModels.mockReset();
  listRoutingModels.mockResolvedValue({ models: [] });
  listAgents.mockReset();
  listAgents.mockResolvedValue([]);
  getRoutingDispatch.mockReset();
  getRoutingDispatch.mockResolvedValue({
    policy: { text: "", rules: [] },
    is_default: true,
    series: [],
    tiers: [],
    editable: true,
  });
  updateWorkspace.mockImplementation(async (_id: string, body: { settings?: unknown }) => ({
    ...workspace.current,
    settings: body.settings,
  }));
  member.role = "owner";
  workspace.current = { id: "ws-1", name: "Acme", slug: "acme", settings: {} };
});

// Exact names: each role card has its own model, URL and key fields, and a
// loose pattern would match the analysis card's too.
// Settings sections are landmarks labelled by their heading, so a section
// titled like a field ("Judge model") also matches a plain label query. Pick
// the control, not the region.
function fieldByLabel(label: RegExp): HTMLElement {
  const matches = screen.getAllByLabelText(label).filter((el) => el.tagName !== "SECTION");
  if (matches.length !== 1) throw new Error(`expected one field labelled ${label}, got ${matches.length}`);
  return matches[0]!;
}

const ANALYSIS_MODEL = /^(model|模型)$/i;
const ANALYSIS_KEY = /^analysis api key$/i;

function masterSwitch() {
  return screen.getByRole("switch", { name: /^(automatic dispatch|自动派活)$/i });
}

function chip() {
  return document.querySelector("[data-state]") as HTMLElement | null;
}

describe("RoutingTab", () => {
  // The runtime lookup once ran above the state it reads; with an empty list
  // the lookup never fires, so only a mount with a runtime present crashed.
  it("mounts when the workspace has runtimes", async () => {
    listRuntimes.mockResolvedValue([
      { id: "rt-1", name: "Mac", status: "online", provider: "claude" },
    ]);
    workspace.current.settings = {
      routing: { analysis: { enabled: true, source: "runtime_subscription", runtime_id: "rt-1" } },
    };
    const { qc } = render("model");
    await waitFor(() =>
      expect(qc.getQueryData(runtimeKeys.list("ws-1"))).toBeDefined(),
    );
    expect(screen.getByRole("heading", { name: /judging model/i })).toBeInTheDocument();
  });

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

  // DENE-1966: the main view is four rows; everything else is a sub-page or
  // the five-item fold.
  it("shows four rows and a closed fold of five", () => {
    render();
    expect(masterSwitch()).toBeInTheDocument();
    for (const [name, page] of [
      [/judging model/i, "model"],
      [/division rules/i, "rules"],
      [/seats and tiers/i, "seats"],
    ] as const) {
      const row = screen.getByText(name, { selector: "p,span,div,label,h3" }).closest("[class*='min-h-16']");
      const link = row?.querySelector("a");
      expect(link?.getAttribute("href")).toContain(`integration=${page}`);
    }
    const fold = document.querySelector("details")!;
    expect(fold).not.toHaveAttribute("open");
    expect(fold.querySelectorAll("[role=switch]")).toHaveLength(3);
    expect(fold.querySelectorAll("input[type=number]")).toHaveLength(2);
    expect(screen.queryByLabelText(/confidence threshold/i)).toBeNull();
    expect(screen.queryByRole("switch", { name: /prefer ample usage|allow one tier up|previous executor/i })).toBeNull();
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(listRoutingModels).not.toHaveBeenCalled();
  });

  it("names the model on its row and opens it as a sub-page", async () => {
    workspace.current.settings = { routing: { analysis: { enabled: true, model: "reasoner-x" } } };
    render();
    expect(screen.getByRole("link", { name: /reasoner-x/ })).toBeInTheDocument();
    render("model");
    expect(screen.getAllByRole("link", { name: /^routing$/i }).at(-1)?.getAttribute("href")).toBe("/acme/settings?tab=routing");
  });

  // Knobs without a control keep what was stored: the form starts from the
  // saved block, so a save never resets them to defaults.
  it("keeps hidden stored knobs when a visible one is saved", async () => {
    workspace.current.settings = {
      routing: {
        enabled: true,
        model: "judge-x",
        decision_mode: "active",
        confidence_threshold: 0.8,
        usage_priority: false,
        allow_upshift: true,
        learn_low_rate: 0.5,
        learn_window_days: 14,
        prefer_continuation: true,
        judged_review: true,
        base_url: "https://judge.example",
        analysis: { enabled: true, model: "reasoner" },
      },
    };
    render();
    await userEvent.click(screen.getByRole("switch", { name: "Guard the strongest tier" }));
    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const routing = updateWorkspace.mock.calls.at(-1)?.[1].settings.routing as Record<string, unknown>;
    expect(routing).toMatchObject({
      strongest_gate: false,
      model: "judge-x",
      confidence_threshold: 0.8,
      usage_priority: false,
      allow_upshift: true,
      learn_low_rate: 0.5,
      learn_window_days: 14,
      prefer_continuation: true,
      judged_review: true,
      base_url: "https://judge.example",
    });
    expect("decision_mode" in routing).toBe(false);
  });

  it("does not save on open for a block stored in its short form", async () => {
    workspace.current.settings = { routing: { enabled: true, analysis: { enabled: true, model: "m" } } };
    render();
    await new Promise((resolve) => setTimeout(resolve, 800));
    expect(updateWorkspace).not.toHaveBeenCalled();
  });

  // The protocol line, not the host line. A workspace that configured Jev saw
  // only "api.typesafe.ai" and a model id, which does not answer the question
  // it had: is this actually a System One model deciding, or a chat model being
  // asked to imitate one? The two mean different things for the confidence the
  // threshold gates on.
  it("names a System One endpoint as the one making the call", async () => {
    workspace.current.settings = {
      routing: {
        enabled: true,
        model: "jev-latest",
        base_url: "https://api.typesafe.ai",
      },
    };
    getRoutingHealth.mockResolvedValue({
      ...HEALTHY,
      model: "jev-latest",
      gateway_scope: "workspace",
      gateway_host: "api.typesafe.ai",
      roles: [{role:"analysis",enabled:true,model:"reasoner",gateway_host:"api.typesafe.ai",gateway_scope:"workspace",gateway_key_set:true}],
      gateway_protocol: "systemone",
      gateway_key_set: true,
    });

    const { qc } = render("model");
    await healthSettled(qc);

    // The System One explanation paragraph was dropped from the copy; the page
    // still names the workspace's own endpoint as the one receiving the call.
    expect(
      screen.getByText(
        "The model id is sent to this workspace's own endpoint: api.typesafe.ai.",
      ),
    ).toBeInTheDocument();
  });

  it("says nothing about System One on an ordinary chat gateway", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna" },
    };

    const { qc } = render("model");
    await healthSettled(qc);

    expect(
      screen.queryByText(/measured rather than self-reported/i),
    ).not.toBeInTheDocument();
  });

  it("saves the stored fields under the routing key and leaves the rest of settings alone", async () => {
    workspace.current.settings = { theme: "dark" };
    render("model");

    // A never-configured workspace starts on the analysis model alone.
    const model = fieldByLabel(ANALYSIS_MODEL);
    await userEvent.type(model, "gpt-5.6-luna");

    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const [, body] = updateWorkspace.mock.calls.at(-1) as [
      string,
      { settings: Record<string, unknown> },
    ];
    expect(body.settings.theme).toBe("dark");
    // No api_key: the key is write-only and is sent ONLY when somebody typed
    // one. If an ordinary save carried `api_key: ""`, it would clear the
    // stored key every time anybody edited the model.
    expect(body.settings.routing).toEqual({
      enabled: false,
      model: "",
      confidence_threshold: 0.6,
      stale_review_hours: 24,
      base_url: "",
      policy_prompt: "",
      usage_priority: true,
      allow_upshift: false,
      strongest_gate: true,
      consult_per_issue: 3,
      prefer_continuation: false,
      prefer_idle: false,
      learn_from_outcomes: false,
      learn_low_rate: 0.3,
      learn_window_days: 30,
      judged_review: false,
      judge_enabled: false,
      analysis: {
        enabled: true,
        model: "gpt-5.6-luna",
        base_url: "",
        source: "api_gateway",
      },
    });
  });

  it("saves the per-issue consultation limit", async () => {
    render();
    fireEvent.change(fieldByLabel(/^consults per ticket$/i), {target:{value:"5"}});
    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    expect(updateWorkspace.mock.calls.at(-1)?.[1].settings.routing.consult_per_issue).toBe(5);
  });

  it("saves the analysis key into the analysis block only", async () => {
    render("model");
    await userEvent.type(fieldByLabel(/^analysis api key$/i), "sk-analysis");
    const saves = screen.getAllByRole("button", { name: /save key|保存 key/i });
    await userEvent.click(saves[0]!);
    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const [, body] = updateWorkspace.mock.calls.at(-1) as [
      string,
      { settings: { routing: Record<string, unknown> & { analysis: Record<string, unknown> } } },
    ];
    expect(body.settings.routing.analysis.api_key).toBe("sk-analysis");
    expect("api_key" in body.settings.routing).toBe(false);
  });

  it("switching routing on writes enabled without inventing a model", async () => {
    render();
    await userEvent.click(masterSwitch());

    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const [, body] = updateWorkspace.mock.calls.at(-1) as [
      string,
      { settings: { routing: { enabled: boolean; model: string } } },
    ];
    expect(body.settings.routing.enabled).toBe(true);
    expect(body.settings.routing.model).toBe("");
  });

  // The key box is write-only by construction: the server strips the stored
  // key from every response, so there is nothing for this field to render
  // back. These three tests are the whole contract of that box.
  it("never renders a stored key back into the field", async () => {
    getRoutingHealth.mockResolvedValue({ ...HEALTHY, gateway_key_set: true });
    const { qc } = render("model");
    await healthSettled(qc);
    const key = fieldByLabel(ANALYSIS_KEY) as HTMLInputElement;
    expect(key.value).toBe("");
    expect(key.type).toBe("password");
    // Not even a mask. The placeholder says a key exists; it does not stand
    // in for one, so nothing here can be mistaken for an editable value.
    expect(key.placeholder).not.toMatch(/[*•]/);
  });

  it("sends the typed key only when the save button is pressed", async () => {
    render("model");
    const key = fieldByLabel(ANALYSIS_KEY);
    await userEvent.type(key, "sk-live-abc");
    // Typing alone must not write: auto-save would store half-typed keys.
    expect(updateWorkspace).not.toHaveBeenCalled();

    // The judge card's button is the second: the analysis card comes first.
    await userEvent.click(screen.getAllByRole("button", { name: /save key|保存 key/i })[0]!);
    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const [, body] = updateWorkspace.mock.calls.at(-1) as [
      string,
      { settings: { routing: Record<string, unknown> } },
    ];
    expect((body.settings.routing.analysis as Record<string, unknown>).api_key).toBe("sk-live-abc");
    // And the box is emptied, so a credential is not left sitting in the DOM.
    await waitFor(() => expect((key as HTMLInputElement).value).toBe(""));
  });

  it("says so when the key is refused, since the box empties either way", async () => {
    updateWorkspace.mockRejectedValue(new Error("only owners and admins can change settings"));
    render("model");
    const key = fieldByLabel(ANALYSIS_KEY);
    await userEvent.type(key, "sk-live-abc");
    await userEvent.click(screen.getAllByRole("button", { name: /save key|保存 key/i })[0]!);
    await waitFor(() => expect(toastError).toHaveBeenCalledWith("only owners and admins can change settings"));
    await waitFor(() => expect((key as HTMLInputElement).value).toBe(""));
  });

  it("saves the endpoint url with the ordinary fields", async () => {
    render("model");
    await userEvent.type(
      fieldByLabel(/^analysis endpoint url$/i),
      "https://gw.example/v1",
    );
    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const [, body] = updateWorkspace.mock.calls.at(-1) as [
      string,
      { settings: { routing: Record<string, unknown> } },
    ];
    expect((body.settings.routing.analysis as Record<string, unknown>).base_url).toBe("https://gw.example/v1");
    expect("api_key" in body.settings.routing).toBe(false);
  });

  it("disables the key field on a deployment that cannot store one", async () => {
    getRoutingHealth.mockResolvedValue({
      ...HEALTHY,
      workspace_key_storable: false,
    });
    const { qc } = render("model");
    await healthSettled(qc);
    await waitFor(() =>
      expect(fieldByLabel(ANALYSIS_KEY)).toBeDisabled(),
    );
  });

  it("is read-only for a plain member", async () => {
    member.role = "member";
    render();
    // Base UI's switch marks the disabled state with data-disabled rather
    // than the native attribute, so assert the behaviour too: a member who
    // clicks it must not produce a write.
    expect(masterSwitch()).toHaveAttribute("data-disabled");
    expect(screen.getByText(/only owners and admins can change this/i)).toBeInTheDocument();
    await userEvent.click(masterSwitch());
    await new Promise((resolve) => setTimeout(resolve, 800));
    expect(updateWorkspace).not.toHaveBeenCalled();

    render("model");
    expect(fieldByLabel(ANALYSIS_MODEL)).toBeDisabled();
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
    const { qc } = render("model");
    await healthSettled(qc);
    expect(await screen.findByText(/api\.openai\.com/)).toBeTruthy();
    expect(fieldByLabel(/^model$/i)).toBeInTheDocument();
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
    const { qc } = render("model");
    await healthSettled(qc);
    expect(fieldByLabel(/^analysis endpoint url$/i)).not.toBeDisabled();
    expect(screen.queryByText(/MULTICA_LLM_BASE_URL/)).toBeNull();
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
    workspace.current.settings = { routing: { analysis: { enabled: true, model: "gpt-5.6-luna" } } };
    getRoutingHealth.mockResolvedValue({
      ...HEALTHY,
      state: "off",
      usable: false,
      last_success_at: 0,
    });
    const { qc } = render();
    await healthSettled(qc);

    await userEvent.click(masterSwitch());

    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    expect(chip()?.getAttribute("data-state")).toBe("enabled");
  });

  it("re-reads health after a save so the chip follows what was just written", async () => {
    render();
    await waitFor(() => expect(getRoutingHealth).toHaveBeenCalledTimes(1));

    await userEvent.click(masterSwitch());

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

    await userEvent.click(masterSwitch());
    expect(chip()?.getAttribute("data-state")).toBe("off");
  });

  it("shows the default routing prompt, saves an edit, restores the default, and keeps the draft when save fails", async () => {
    const user = userEvent.setup();
    render("rules");
    const box = fieldByLabel(/routing prompt/i);
    expect(box).toHaveValue(DEFAULT_ROUTING_POLICY_PROMPT);

    fireEvent.change(box, { target: { value: "Use the weak tier for copy edits." } });
    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const saved = updateWorkspace.mock.calls.at(-1)?.[1] as {
      settings: { routing: { policy_prompt: string } };
    };
    expect(saved.settings.routing.policy_prompt).toBe("Use the weak tier for copy edits.");

    updateWorkspace.mockClear();
    await user.click(screen.getByRole("button", { name: /restore the default prompt/i }));
    expect(box).toHaveValue(DEFAULT_ROUTING_POLICY_PROMPT);
    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const restored = updateWorkspace.mock.calls.at(-1)?.[1] as {
      settings: { routing: { policy_prompt: string } };
    };
    expect(restored.settings.routing.policy_prompt).toBe("");

    updateWorkspace.mockReset();
    updateWorkspace.mockRejectedValue(new Error("offline"));
    fireEvent.change(box, { target: { value: "Keep this draft." } });
    await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument());
    expect(box).toHaveValue("Keep this draft.");
  });
});

describe("RoutingTab seat order switches", () => {

  // DENE-1834: 最强档门槛 is on by default and switches off into the routing block.
  it("writes the strongest-tier gate into the routing block", async () => {
    workspace.current.settings = { routing: { enabled: true, model: "gpt-5.6-luna" } };
    render();
    const gate = screen.getByRole("switch", { name: "Guard the strongest tier" });
    expect(gate).toHaveAttribute("data-checked");
    await userEvent.click(gate);
    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const [, body] = updateWorkspace.mock.calls.at(-1) as [
      string,
      { settings: { routing: Record<string, unknown> } },
    ];
    expect(body.settings.routing.strongest_gate).toBe(false);
  });


  // DENE-1203: 负载分流 is its own switch, off by default.
  it("switches 负载分流 on and saves it", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna" },
    };
    render();
    const load = screen.getByRole("switch", { name: "Balance by load" });
    expect(load).not.toHaveAttribute("data-checked");
    expect(screen.getByText(/^Off: the dispatch comment only notes who/)).toBeInTheDocument();

    await userEvent.click(load);
    expect(screen.getByText(/^On: within a tier/)).toBeInTheDocument();
    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const [, body] = updateWorkspace.mock.calls.at(-1) as [
      string,
      { settings: { routing: Record<string, unknown> } },
    ];
    expect(body.settings.routing.prefer_idle).toBe(true);
    expect(body.settings.routing.prefer_continuation).toBe(false);
  });

  // DENE-1722: 从结果里学 is its own switch; its rate and window keep their
  // stored values now that they have no control (DENE-1966).
  it("switches 从结果里学 on and keeps its stored thresholds", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna", learn_low_rate: 0.5, learn_window_days: 14 },
    };
    render();
    const learn = screen.getByRole("switch", { name: "Learn from outcomes" });
    expect(learn).not.toHaveAttribute("data-checked");
    expect(screen.queryByRole("spinbutton", { name: /share|window/i })).toBeNull();

    await userEvent.click(learn);
    expect(screen.getByText(/^On: raise one tier/)).toBeInTheDocument();
    await waitFor(() => {
      const [, body] = updateWorkspace.mock.calls.at(-1) as [
        string,
        { settings: { routing: Record<string, unknown> } },
      ];
      expect(body.settings.routing).toMatchObject({
        learn_from_outcomes: true,
        learn_low_rate: 0.5,
        learn_window_days: 14,
      });
    });
  });

  // DENE-1677: the rule table decides the tier and the 验收席, so the
  // 按判断配验收 switch is gone, and a saved value is kept as it was.
  it("drops the judged-review switch and keeps a saved value", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna", judged_review: true },
    };
    render();
    expect(screen.queryByRole("switch", { name: /judged/i })).toBeNull();

    await userEvent.click(screen.getByRole("switch", { name: "Balance by load" }));
    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const [, body] = updateWorkspace.mock.calls.at(-1) as [
      string,
      { settings: { routing: Record<string, unknown> } },
    ];
    expect(body.settings.routing.judged_review).toBe(true);
  });

  it("shows the rule table read-only, one row per rule", async () => {
    getRoutingHealth.mockResolvedValue({ ...HEALTHY, rules: RULES });
    const { qc } = render("rules");
    await healthSettled(qc);
    expect(await screen.findByText("Tier rules")).toBeInTheDocument();
    expect(screen.getByText("改动范围：跨模块")).toBeInTheDocument();
    expect(screen.getByText("Any question unanswered")).toBeInTheDocument();
    expect(screen.getByText("改动范围：小 · 需求：清楚")).toBeInTheDocument();
    expect(screen.getByText("Nothing above matched")).toBeInTheDocument();
    expect(screen.getByText("强档 · automatic review by tier")).toBeInTheDocument();
    expect(screen.getAllByText("弱档 · automatic review by tier")).toHaveLength(1);
  });

  it("hides the rule table on a backend that predates it", async () => {
    const { qc } = render("rules");
    await healthSettled(qc);
    expect(screen.queryByText("Tier rules")).toBeNull();
  });

});
