import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";

const mockCopyText = vi.hoisted(() => vi.fn(async () => true));
const createdRef = vi.hoisted(() => ({ current: {} as Record<string, unknown> }));

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: [], isLoading: false, isError: false }),
}));
vi.mock("@multica/core/api", () => ({ api: { getBaseUrl: () => "https://office.example" } }));
vi.mock("@multica/core/paths", () => ({ useCurrentWorkspace: () => ({ id: "workspace-1" }) }));
vi.mock("@multica/core/permissions", () => ({ useCurrentMember: () => ({ role: "owner" }) }));
vi.mock("@multica/core/workspace/queries", () => ({ workspaceBotsOptions: () => ({ queryKey: ["bots"] }) }));
vi.mock("@multica/core/workspace/mutations", () => {
  const idle = () => ({ mutate: vi.fn(), isPending: false });
  return {
    useCreateBot: () => ({
      isPending: false,
      mutate: (_values: unknown, opts: { onSuccess: (bot: unknown) => void }) => opts.onSuccess(createdRef.current),
    }),
    useUpdateBot: idle,
    useRotateBotToken: idle,
    useRevokeBotToken: idle,
    useDeleteBot: idle,
  };
});
vi.mock("@multica/ui/lib/clipboard", () => ({ copyText: mockCopyText }));
vi.mock("sonner", () => ({ toast: { error: vi.fn() } }));

import { BotsTab } from "./bots-tab";

const TEST_RESOURCES = { en: { common: enCommon, settings: enSettings } };

function Wrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

const bot = {
  id: "bot-1",
  workspace_id: "workspace-1",
  user_id: "user-1",
  name: "Grok",
  can_release: false,
  webhook_url: "",
  webhook_failures: 0,
  webhook_last_error: "",
  webhook_key_set: false,
  token_prefix: "mul_abcdefgh",
  token_last_used_at: null,
  created_at: "2026-10-14T00:00:00Z",
  token: "mul_abcdefgh123",
  webhook_secret: "secret",
};

async function createGrok() {
  const user = userEvent.setup();
  render(<BotsTab />, { wrapper: Wrapper });
  await user.click(screen.getByRole("button", { name: "New Bot" }));
  await user.type(screen.getByLabelText("Name"), "Grok");
  await user.click(screen.getAllByRole("button", { name: "New Bot" }).at(-1)!);
  return user;
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("BotsTab — setup prompt", () => {
  it("copies the server's setup prompt from the one-time secrets screen", async () => {
    createdRef.current = { ...bot, setup_prompt: "connect with Bearer mul_abcdefgh123" };
    const user = await createGrok();

    expect(screen.getByText("Setup prompt")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Copy prompt" }));
    expect(mockCopyText).toHaveBeenCalledWith("connect with Bearer mul_abcdefgh123");
    expect(await screen.findByRole("button", { name: "Copied" })).toBeInTheDocument();
  });

  it("hides the row when the server sent no prompt", async () => {
    createdRef.current = { ...bot };
    await createGrok();

    expect(screen.getByText("Grok is ready")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Copy prompt" })).not.toBeInTheDocument();
  });

  it("shows the office token but no HMAC signing secret", async () => {
    createdRef.current = { ...bot };
    await createGrok();

    expect(screen.getByText("Multica office token (starts with mul_)")).toBeInTheDocument();
    expect(screen.getByText("mul_abcdefgh123")).toBeInTheDocument();
    // The HMAC signing secret is not something the Bot can use.
    expect(screen.queryByText("secret")).not.toBeInTheDocument();
  });
});

describe("BotsTab — new Bot form", () => {
  it("asks only for the name", async () => {
    const user = userEvent.setup();
    render(<BotsTab />, { wrapper: Wrapper });
    await user.click(screen.getByRole("button", { name: "New Bot" }));

    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getAllByRole("textbox")).toHaveLength(1);
    expect(within(dialog).getByLabelText("Name")).toBeInTheDocument();
    expect(within(dialog).queryByRole("switch")).not.toBeInTheDocument();
  });
});
