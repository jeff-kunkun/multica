import { beforeEach, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderWithI18n } from "../../test/i18n";
import { BotBindingSection } from "./bot-binding-section";

const fixture = vi.hoisted(() => ({
  binding: {} as Record<string, unknown>,
  put: vi.fn(),
  writes: [] as Record<string, unknown>[],
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws" }));
vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ role: "admin" }),
}));
vi.mock("@multica/core/bots", () => ({
  botBindingOptions: () => ({
    queryKey: ["binding", "ws"],
    queryFn: async () => fixture.binding,
  }),
  botWritesOptions: () => ({
    queryKey: ["writes", "ws"],
    queryFn: async () => ({ writes: fixture.writes, next_cursor: "" }),
    initialPageParam: "",
    getNextPageParam: () => undefined,
  }),
  externalBotListOptions: () => ({
    queryKey: ["bots", "ws"],
    queryFn: async () => [{ id: "bot-1", name: "Grok Bot", status: "active" }],
  }),
  usePutBotBinding: () => ({ mutate: fixture.put, isPending: false }),
  useDeleteBotBinding: () => ({ mutate: vi.fn(), isPending: false }),
  useVerifyBotBinding: () => ({ mutate: vi.fn(), isPending: false }),
}));

function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <BotBindingSection />
    </QueryClientProvider>,
  );
}

const bound = (scope: string[]) => ({
  bound: true,
  state: "ok",
  message: "",
  bot_id: "bot-1",
  bot_name: "Grok Bot",
  mode: "bot_first",
  activity_scope: scope,
  open_count: 0,
});

beforeEach(() => {
  fixture.put.mockReset();
  fixture.binding = bound([]);
  fixture.writes = [];
});

it("hides the activity stream row until a Bot is connected", async () => {
  fixture.binding = { bound: false, state: "none", message: "", open_count: 0, activity_scope: [] };
  mount();
  await screen.findByText("Send them to my Bot");
  expect(screen.queryByRole("combobox", { name: "Send everything to my Bot" })).toBeNull();
});

it("shows the stream off by default and saves only the scope when changed", async () => {
  mount();
  const trigger = await screen.findByRole("combobox", { name: "Send everything to my Bot" });
  expect(trigger).toHaveTextContent("Off");

  await userEvent.click(trigger);
  await userEvent.click(await screen.findByRole("option", { name: "Mine and followed" }));
  await waitFor(() =>
    expect(fixture.put).toHaveBeenCalledWith(
      { activity_scope: ["owned", "followed"] },
      expect.anything(),
    ),
  );
});

it("reads a followed-only scope back as its own choice", async () => {
  fixture.binding = bound(["followed"]);
  mount();
  const trigger = await screen.findByRole("combobox", { name: "Send everything to my Bot" });
  expect(trigger).toHaveTextContent("Tickets I follow");
});

it("offers the whole workspace and saves it as one scope", async () => {
  mount();
  const trigger = await screen.findByRole("combobox", { name: "Send everything to my Bot" });
  await userEvent.click(trigger);
  await userEvent.click(await screen.findByRole("option", { name: "Everything I can see" }));
  await waitFor(() =>
    expect(fixture.put).toHaveBeenCalledWith({ activity_scope: ["owner_visible_all"] }, expect.anything()),
  );
});

it("reads the whole-workspace scope back and lists what the Bot did in their name", async () => {
  fixture.binding = bound(["owner_visible_all"]);
  fixture.writes = [
    { id: "w1", bot_name: "Grok Bot", on_behalf_of_name: "Kun", family: "agent", fields: ["name"], outcome: "done" },
    { id: "w2", bot_name: "Grok Bot", on_behalf_of_name: "Kun", family: "member", fields: [], outcome: "blocked" },
  ];
  mount();
  const trigger = await screen.findByRole("combobox", { name: "Send everything to my Bot" });
  expect(trigger).toHaveTextContent("Everything I can see");
  expect(await screen.findAllByText("Via Bot “Grok Bot” for Kun")).toHaveLength(2);
});
