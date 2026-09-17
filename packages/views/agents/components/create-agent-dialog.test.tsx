// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent, MemberWithUser, RuntimeDevice } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import { WorkspaceSlugProvider } from "@multica/core/paths";
import { configStore } from "@multica/core/config";
import { COMPOSIO_MCP_APPS_FLAG } from "@multica/core/feature-flags";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import enCommon from "../../locales/en/common.json";
import enAgents from "../../locales/en/agents.json";

const navigationStub: NavigationAdapter = {
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/",
  searchParams: new URLSearchParams(),
  hash: "",
  getShareableUrl: (path: string) => path,
};

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

// ModelDropdown talks to the api; the create dialog only needs it as a
// stand-in here, so swap it out.
vi.mock("./model-dropdown", () => ({
  ModelDropdown: () => null,
}));

// Provider logos don't matter for these assertions but they pull in SVGs.
vi.mock("../../runtimes/components/provider-logo", () => ({
  ProviderLogo: () => null,
}));

// Avatars hit the api for member metadata.
vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: () => null,
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

import { CreateAgentDialog } from "./create-agent-dialog";

const ME = "user-me";
const OTHER = "user-other";

const members: MemberWithUser[] = [
  {
    id: "m-me",
    user_id: ME,
    workspace_id: "ws-1",
    role: "member",
    name: "Me",
    email: "me@example.com",
    avatar_url: null,
    created_at: "2026-01-01T00:00:00Z",
  },
  {
    id: "m-other",
    user_id: OTHER,
    workspace_id: "ws-1",
    role: "member",
    name: "Other",
    email: "other@example.com",
    avatar_url: null,
    created_at: "2026-01-01T00:00:00Z",
  },
];

function makeRuntime(overrides: Partial<RuntimeDevice>): RuntimeDevice {
  return {
    id: "rt",
    workspace_id: "ws-1",
    daemon_id: null,
    name: "Test Runtime",
    runtime_mode: "local",
    provider: "claude",
    launch_header: "",
    status: "online",
    device_info: "host.local",
    metadata: {},
    owner_id: ME,
    visibility: "private",
    last_seen_at: "2026-04-27T11:59:50Z",
    created_at: "2026-04-01T00:00:00Z",
    updated_at: "2026-04-01T00:00:00Z",
    ...overrides,
  };
}

function makeDuplicateSource(runtimeId: string): Agent {
  return makeWorkspaceAgent({ id: "agent-source", runtime_id: runtimeId });
}

function makeWorkspaceAgent(overrides: Partial<Agent>): Agent {
  return {
    id: "agent-source",
    workspace_id: "ws-1",
    runtime_id: "rt",
    name: "Source Agent",
    description: "",
    instructions: "",
    avatar_url: null,
    runtime_mode: "local",
    runtime_config: {},
    custom_args: [],
    visibility: "private",
    permission_mode: "private",
    invocation_targets: [],
    status: "idle",
    max_concurrent_tasks: 1,
    model: "",
    owner_id: ME,
    skills: [],
    created_at: "2026-04-01T00:00:00Z",
    updated_at: "2026-04-01T00:00:00Z",
    archived_at: null,
    archived_by: null,
    ...overrides,
  };
}

function renderDialog(
  runtimes: RuntimeDevice[],
  template?: Agent,
  workspaceAgents: Agent[] = [],
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const onCreate = vi.fn().mockResolvedValue(undefined);
  const onClose = vi.fn();
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={queryClient}>
        <WorkspaceSlugProvider slug="test-ws">
        <NavigationProvider value={navigationStub}>
          <CreateAgentDialog
            runtimes={runtimes}
            members={members}
            agents={workspaceAgents}
            currentUserId={ME}
            template={template}
            onClose={onClose}
            onCreate={onCreate}
          />
        </NavigationProvider>
        </WorkspaceSlugProvider>
      </QueryClientProvider>
    </I18nProvider>,
  );
  return { onCreate, onClose };
}

describe("CreateAgentDialog runtime visibility gate", () => {
  beforeEach(() => vi.clearAllMocks());
  // Base UI Dialog renders into a portal on document.body and leaves
  // focus-guard / inert wrapper divs around after the React tree unmounts.
  // The auto-cleanup from @testing-library/react drops the container but
  // not the portal residue, so two-tests-in-a-row queries see double
  // matches ("All", "My Runtime"). Force cleanup + wipe body between tests.
  afterEach(() => {
    cleanup();
    document.body.innerHTML = "";
  });

  it("disables another member's private runtime in the picker", () => {
    const mine = makeRuntime({ id: "rt-mine", name: "My Runtime", owner_id: ME, visibility: "private" });
    const othersPrivate = makeRuntime({
      id: "rt-others-private",
      name: "Others Private",
      owner_id: OTHER,
      visibility: "private",
    });
    renderDialog([mine, othersPrivate]);

    // Flip to "All" so other-owned runtimes show.
    fireEvent.click(screen.getByText("All"));
    // Open the picker.
    fireEvent.click(
      screen.getByText("My Runtime", { selector: "span.truncate" }),
    );

    const disabledRow = screen
      .getByText("Others Private")
      .closest("button") as HTMLButtonElement;
    expect(disabledRow).not.toBeNull();
    expect(disabledRow.disabled).toBe(true);
    expect(disabledRow.title).toMatch(/Private runtime/i);
  });

  it("lets a plain member pick another member's public runtime", () => {
    const mine = makeRuntime({ id: "rt-mine", name: "My Runtime", owner_id: ME, visibility: "private" });
    const othersPublic = makeRuntime({
      id: "rt-others-public",
      name: "Others Public",
      owner_id: OTHER,
      visibility: "public",
    });
    renderDialog([mine, othersPublic]);

    fireEvent.click(screen.getByText("All"));
    fireEvent.click(
      screen.getByText("My Runtime", { selector: "span.truncate" }),
    );

    const publicRow = screen
      .getByText("Others Public")
      .closest("button") as HTMLButtonElement;
    expect(publicRow).not.toBeNull();
    expect(publicRow.disabled).toBe(false);
  });

  it("defaults the selected runtime to a usable one, not a locked private", () => {
    const othersPrivate = makeRuntime({
      id: "rt-others-private",
      name: "Others Private",
      owner_id: OTHER,
      visibility: "private",
    });
    const mine = makeRuntime({
      id: "rt-mine",
      name: "My Runtime",
      owner_id: ME,
      visibility: "private",
    });
    renderDialog([othersPrivate, mine]);

    // The trigger label shows the selected runtime name. The picker must
    // not seed with the other-owned private runtime even if it sorted
    // first in the input list.
    expect(screen.queryByText("Others Private", { selector: "span.truncate" })).toBeNull();
    expect(screen.getByText("My Runtime", { selector: "span.truncate" })).toBeInTheDocument();
  });

  it("in duplicate mode, does not pre-fill the source agent's runtime when it's now locked", async () => {
    // The source runtime is owned by someone else and now private — the
    // duplicate flow used to seed with it anyway, leaving the user with
    // a Create button that 403s server-side. Now we fall back to the
    // first usable runtime instead.
    const othersPrivate = makeRuntime({
      id: "rt-others-private",
      name: "Others Private",
      owner_id: OTHER,
      visibility: "private",
    });
    const mine = makeRuntime({
      id: "rt-mine",
      name: "My Runtime",
      owner_id: ME,
      visibility: "private",
    });
    const duplicateSource = makeDuplicateSource("rt-others-private");
    const { onCreate } = renderDialog([othersPrivate, mine], duplicateSource);

    expect(
      screen.getByText("My Runtime", { selector: "span.truncate" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("Others Private", { selector: "span.truncate" }),
    ).toBeNull();

    // Sanity check: with a usable selection seeded, Create should submit.
    fireEvent.click(screen.getByText("Create"));
    await new Promise((r) => setTimeout(r, 0));
    expect(onCreate).toHaveBeenCalledTimes(1);
    expect(onCreate.mock.calls[0]?.[0].runtime_id).toBe("rt-mine");
  });

  it("disables Create when the selected runtime is locked (duplicate source + no usable fallback)", () => {
    // Edge case: the source agent points at a locked runtime AND the workspace
    // has no usable alternatives in scope. The defense-in-depth gate on
    // the Create button must keep the user from submitting a 403.
    const onlyOthersPrivate = makeRuntime({
      id: "rt-only-others-private",
      name: "Only Others Private",
      owner_id: OTHER,
      visibility: "private",
    });
    // Flip the picker to "All" so the locked runtime is at least
    // visible — that's the scope where the selected-but-locked state
    // can persist after the initial seed search returns nothing.
    const duplicateSource = makeDuplicateSource("rt-only-others-private");
    renderDialog([onlyOthersPrivate], duplicateSource);

    // The Create button is rendered by lucide-free CTA text "Create".
    const createBtn = screen
      .getAllByRole("button")
      .find((b) => b.textContent === "Create");
    expect(createBtn).toBeDefined();
    expect((createBtn as HTMLButtonElement).disabled).toBe(true);
  });
});

describe("CreateAgentDialog access picker (MUL-4010, feature-flag gated)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // The dialog's default (workspace) still needs to be usable by ME:
    // reset flags before every test so a stray "on" state in one test
    // can't bleed into the next.
    configStore.getState().setFeatureFlags({});
  });

  afterEach(() => {
    cleanup();
    document.body.innerHTML = "";
    configStore.getState().setFeatureFlags({});
  });

  it("keeps the legacy Workspace/Personal toggle when the flag is OFF", async () => {
    configStore.getState().setFeatureFlags({ [COMPOSIO_MCP_APPS_FLAG]: false });
    const mine = makeRuntime({ id: "rt-mine", name: "My Runtime", owner_id: ME });
    const { onCreate } = renderDialog([mine]);

    // Legacy copy is rendered — matches VISIBILITY_DESCRIPTION.
    expect(screen.getByText(/All members can assign/i)).toBeInTheDocument();

    fireEvent.change(screen.getByPlaceholderText("e.g. Deep Research Agent"), {
      target: { value: "Legacy Agent" },
    });
    fireEvent.click(screen.getByText("Create"));
    await new Promise((r) => setTimeout(r, 0));

    const payload = onCreate.mock.calls[0]?.[0];
    expect(payload).toBeDefined();
    // Legacy path submits visibility, NOT permission_mode/invocation_targets.
    expect(payload.visibility).toBe("workspace");
    expect(payload.permission_mode).toBeUndefined();
    expect(payload.invocation_targets).toBeUndefined();
  });

  it("submits permission_mode=public_to + workspace target when the flag is ON (default)", async () => {
    configStore.getState().setFeatureFlags({ [COMPOSIO_MCP_APPS_FLAG]: true });
    const mine = makeRuntime({ id: "rt-mine", name: "My Runtime", owner_id: ME });
    const { onCreate } = renderDialog([mine]);

    // New copy replaces the old one.
    expect(screen.getByText("Only you can run this agent")).toBeInTheDocument();
    expect(screen.getByText("Entire workspace")).toBeInTheDocument();
    expect(screen.getByText("Specific people")).toBeInTheDocument();

    fireEvent.change(screen.getByPlaceholderText("e.g. Deep Research Agent"), {
      target: { value: "Access Agent" },
    });
    fireEvent.click(screen.getByText("Create"));
    await new Promise((r) => setTimeout(r, 0));

    const payload = onCreate.mock.calls[0]?.[0];
    expect(payload).toBeDefined();
    // MUL-3963 payload shape.
    expect(payload.visibility).toBeUndefined();
    expect(payload.permission_mode).toBe("public_to");
    expect(payload.invocation_targets).toEqual([
      { target_type: "workspace" },
    ]);
  });

  it("submits permission_mode=private with empty targets when Private is chosen", async () => {
    configStore.getState().setFeatureFlags({ [COMPOSIO_MCP_APPS_FLAG]: true });
    const mine = makeRuntime({ id: "rt-mine", name: "My Runtime", owner_id: ME });
    const { onCreate } = renderDialog([mine]);

    fireEvent.change(screen.getByPlaceholderText("e.g. Deep Research Agent"), {
      target: { value: "Private Agent" },
    });
    // Click the Private card. The Private description doubles as a stable
    // click target inside the button.
    fireEvent.click(screen.getByText("Only you can run this agent"));
    fireEvent.click(screen.getByText("Create"));
    await new Promise((r) => setTimeout(r, 0));

    const payload = onCreate.mock.calls[0]?.[0];
    expect(payload).toBeDefined();
    expect(payload.permission_mode).toBe("private");
    expect(payload.invocation_targets).toEqual([]);
  });

  it("does not create when specific-people scope has no member", async () => {
    configStore.getState().setFeatureFlags({ [COMPOSIO_MCP_APPS_FLAG]: true });
    const mine = makeRuntime({ id: "rt-mine", name: "My Runtime", owner_id: ME });
    const { onCreate } = renderDialog([mine]);

    fireEvent.change(screen.getByPlaceholderText("e.g. Deep Research Agent"), {
      target: { value: "Empty Public Agent" },
    });
    fireEvent.click(screen.getByRole("radio", { name: /^Specific people/i }));
    fireEvent.click(screen.getByText("Create"));
    await new Promise((r) => setTimeout(r, 0));

    expect(onCreate).not.toHaveBeenCalled();
  });

  it("includes ticked members in the invocation_targets payload", async () => {
    configStore.getState().setFeatureFlags({ [COMPOSIO_MCP_APPS_FLAG]: true });
    const mine = makeRuntime({ id: "rt-mine", name: "My Runtime", owner_id: ME });
    const { onCreate } = renderDialog([mine]);

    fireEvent.change(screen.getByPlaceholderText("e.g. Deep Research Agent"), {
      target: { value: "Shared Agent" },
    });
    fireEvent.click(screen.getByRole("radio", { name: /^Specific people/i }));
    fireEvent.click(screen.getByRole("checkbox", { name: /^Other/ }));
    fireEvent.click(screen.getByText("Create"));
    await new Promise((r) => setTimeout(r, 0));

    const payload = onCreate.mock.calls[0]?.[0];
    expect(payload).toBeDefined();
    expect(payload.permission_mode).toBe("public_to");
    expect(payload.invocation_targets).toEqual([
      { target_type: "member", target_id: OTHER },
    ]);
  });
});

// DENE-304: the create surface has to be able to say "this agent is a
// specialisation of X", and it may only offer things the server accepts as a
// base role — a non-archived agent that is not itself a specialisation.
describe("CreateAgentDialog base-role picker (DENE-304)", () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(() => {
    cleanup();
    document.body.innerHTML = "";
  });

  const runtime = () =>
    makeRuntime({ id: "rt-mine", name: "My Runtime", owner_id: ME });
  const baseRole = () =>
    makeWorkspaceAgent({ id: "agent-base", name: "Base Role" });
  const specialization = () =>
    makeWorkspaceAgent({
      id: "agent-spec",
      name: "Spec Agent",
      parent_agent_id: "agent-base",
      parent_agent_name: "Base Role",
    });
  const archivedBase = () =>
    makeWorkspaceAgent({
      id: "agent-archived",
      name: "Archived Base",
      archived_at: "2026-04-02T00:00:00Z",
    });

  it("offers base roles only and defaults to independent", async () => {
    const user = userEvent.setup();
    renderDialog([runtime()], undefined, [
      baseRole(),
      specialization(),
      archivedBase(),
    ]);

    expect(
      screen.getByRole("combobox", { name: "Base role" }),
    ).toHaveTextContent("Independent base role");

    await user.click(screen.getByRole("combobox", { name: "Base role" }));
    expect(
      await screen.findByRole("option", { name: "Base Role" }),
    ).toBeInTheDocument();
    // A specialisation can never be a parent ("特化角色不能再派生"), and an
    // archived agent cannot be one either — both would be a server-side 400.
    expect(screen.queryByRole("option", { name: "Spec Agent" })).toBeNull();
    expect(screen.queryByRole("option", { name: "Archived Base" })).toBeNull();
  });

  it("sends parent_agent_id for the chosen base role", async () => {
    const user = userEvent.setup();
    const { onCreate } = renderDialog([runtime()], undefined, [baseRole()]);

    fireEvent.change(screen.getByPlaceholderText("e.g. Deep Research Agent"), {
      target: { value: "Nightly Variant" },
    });
    await user.click(screen.getByRole("combobox", { name: "Base role" }));
    await user.click(await screen.findByRole("option", { name: "Base Role" }));
    fireEvent.click(screen.getByText("Create"));
    await new Promise((r) => setTimeout(r, 0));

    expect(onCreate.mock.calls[0]?.[0].parent_agent_id).toBe("agent-base");
  });

  it("omits parent_agent_id for an independent base role", async () => {
    const { onCreate } = renderDialog([runtime()], undefined, [baseRole()]);

    fireEvent.change(screen.getByPlaceholderText("e.g. Deep Research Agent"), {
      target: { value: "Standalone" },
    });
    fireEvent.click(screen.getByText("Create"));
    await new Promise((r) => setTimeout(r, 0));

    expect(onCreate.mock.calls[0]?.[0]).not.toHaveProperty("parent_agent_id");
  });

  it("states what the new specialisation inherits", async () => {
    const user = userEvent.setup();
    renderDialog([runtime()], undefined, [baseRole()]);

    await user.click(screen.getByRole("combobox", { name: "Base role" }));
    await user.click(await screen.findByRole("option", { name: "Base Role" }));

    expect(screen.getByTestId("agent-base-role-hint")).toHaveTextContent(
      /prepended to this agent's own prompt/i,
    );
  });
});
