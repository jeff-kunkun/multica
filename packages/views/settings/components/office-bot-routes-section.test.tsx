// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import type { ExternalBot } from "@multica/core/types";
import enSettings from "../../locales/en/settings.json";
import { OfficeBotRoutesSection } from "./office-bot-routes-section";
const mocks = vi.hoisted(() => ({
  save: vi.fn(),
  remove: vi.fn(),
  routes: [] as unknown[],
}));
vi.mock("@multica/core/projects", () => ({
  projectListOptions: () => ({ kind: "projects" }),
}));
vi.mock("@multica/core/bots", () => ({
  officeBotRoutesOptions: () => ({ kind: "routes" }),
  useSaveOfficeBotRoute: () => ({ mutate: mocks.save, isPending: false }),
  useDeleteOfficeBotRoute: () => ({ mutate: mocks.remove, isPending: false }),
}));
vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { kind: string }) => ({
    data:
      options.kind === "projects"
        ? [{ id: "p", title: "Project" }]
        : mocks.routes,
    isLoading: false,
    isError: false,
  }),
}));
const bot = { id: "b", name: "Grok", status: "active" } as ExternalBot;
function mount() {
  return render(
    <I18nProvider locale="en" resources={{ en: { settings: enSettings } }}>
      <OfficeBotRoutesSection wsId="ws" canManage bots={[bot]} />
    </I18nProvider>,
  );
}
beforeEach(() => {
  cleanup();
  vi.clearAllMocks();
  mocks.routes = [];
});
describe("project notification destinations", () => {
  it("sends a default destination only after Bot and actual group ID are supplied", async () => {
    mount();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Add destination" }));
    expect(
      screen.getByRole("button", { name: "Save" }).hasAttribute("disabled"),
    ).toBe(true);
    await user.click(screen.getByRole("combobox", { name: "Bot" }));
    await user.click(await screen.findByRole("option", { name: "Grok" }));
    await user.type(screen.getByLabelText("Multica group ID"), "  group-qa  ");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(mocks.save).toHaveBeenCalledWith(
      { projectId: null, botId: "b", channelRef: "group-qa" },
      expect.any(Object),
    );
  });
  it("removes only the chosen project destination after confirmation", async () => {
    mocks.routes = [{ projectId: "p", botId: "b", channelRef: "group-p" }];
    mount();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Remove" }));
    expect(mocks.remove).not.toHaveBeenCalled();
    await user.click(screen.getAllByRole("button", { name: "Remove" }).at(-1)!);
    expect(mocks.remove).toHaveBeenCalledWith("p", expect.any(Object));
  });
});
