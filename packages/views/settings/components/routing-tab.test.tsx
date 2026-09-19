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
  return { ...actual, api: { updateWorkspace } };
});

vi.mock("@multica/core/paths", async (importOriginal) => ({
  paths: (await importOriginal<typeof import("@multica/core/paths")>()).paths,
  useCurrentWorkspace: () => workspace.current,
}));

vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ role: member.role, isLoading: false }),
}));

import { RoutingTab } from "./routing-tab";

function render() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  // The query provider is nested INSIDE the element rather than passed as
  // `wrapper`: renderWithI18n supplies its own wrapper, and a second one here
  // would replace it, leaving every label an empty string.
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <RoutingTab />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  updateWorkspace.mockReset();
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
});
