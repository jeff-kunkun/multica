import { beforeEach, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderWithI18n } from "../../test/i18n";
import { BotBindingSection } from "./bot-binding-section";

const fixture = vi.hoisted(() => ({
  binding: {} as Record<string, unknown>,
  writes: [] as Record<string, unknown>[],
  deliveries: [] as Record<string, unknown>[],
  retry: vi.fn(),
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws" }));
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
  officeDeliveriesOptions: () => ({
    queryKey: ["deliveries", "ws"],
    queryFn: async () => ({ deliveries: fixture.deliveries, next_cursor: 0, connected: true }),
    initialPageParam: 0,
    getNextPageParam: () => undefined,
  }),
  useRetryOfficeDeliveries: () => ({ mutate: fixture.retry, isPending: false }),
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
  fixture.binding = bound([]);
  fixture.writes = [];
  fixture.deliveries = [];
  fixture.retry.mockReset();
});

it("never asks the person to pick a Bot or a scope (DENE-2009)", async () => {
  fixture.binding = { bound: false, state: "none", message: "", open_count: 0, activity_scope: [] };
  mount();
  await screen.findByText("When you agree to a Bot's connect request, what needs your decision goes to it automatically.");
  expect(screen.queryByRole("combobox")).toBeNull();
  expect(screen.queryByRole("switch")).toBeNull();
});

it("says where decisions go and lists what the Bot did in their name", async () => {
  fixture.binding = bound(["owner_visible_all"]);
  fixture.writes = [
    { id: "w1", bot_name: "Grok Bot", on_behalf_of_name: "Kun", family: "agent", fields: ["name"], outcome: "done" },
    { id: "w2", bot_name: "Grok Bot", on_behalf_of_name: "Kun", family: "member", fields: [], outcome: "blocked" },
  ];
  mount();
  await screen.findByText("What needs your decision goes to Grok Bot first.");
  expect(screen.queryByRole("combobox")).toBeNull();
  expect(await screen.findAllByText("Via Bot “Grok Bot” for Kun")).toHaveLength(2);
});

const delivery = (over: Record<string, unknown>) => ({
  event_id: "ev-1",
  status: "relayed",
  must_relay: false,
  attempts: 1,
  last_error: "",
  blocked_reason: "",
  cursor: 1,
  event: { summary: "Fix login · done" },
  ...over,
});

it("shows where each result stands and retries only what is stuck", async () => {
  fixture.deliveries = [
    delivery({ event_id: "ev-1", cursor: 2, status: "relayed" }),
    delivery({
      event_id: "ev-2",
      cursor: 1,
      status: "blocked",
      must_relay: true,
      blocked_reason: "Bot key revoked",
      event: { summary: "Release ticket · done" },
    }),
  ];
  mount();
  await screen.findByText("Release ticket · done");
  expect(screen.getByText("Passed on to you")).toBeInTheDocument();
  expect(screen.getByText("Bot key revoked · Must reach you")).toBeInTheDocument();

  const retries = screen.getAllByRole("button", { name: "Retry" });
  expect(retries).toHaveLength(1);
  await userEvent.click(retries[0]!);
  expect(fixture.retry).toHaveBeenCalledWith("ev-2", expect.anything());

  await userEvent.click(screen.getByRole("button", { name: "Retry all" }));
  expect(fixture.retry).toHaveBeenLastCalledWith(undefined, expect.anything());
});
