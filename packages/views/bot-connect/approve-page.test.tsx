import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../locales/en/common.json";

const mockAuthState = vi.hoisted(() => ({
  user: null as { id: string } | null,
  isLoading: false,
}));
const mockPush = vi.hoisted(() => vi.fn());
const mockApi = vi.hoisted(() => ({
  getBotConnect: vi.fn(),
  approveBotConnect: vi.fn(),
  denyBotConnect: vi.fn(),
  listWorkspaces: vi.fn(),
}));

vi.mock("@multica/core/auth", () => {
  const useAuthStore = Object.assign(
    (selector?: (state: typeof mockAuthState) => unknown) => (selector ? selector(mockAuthState) : mockAuthState),
    { getState: () => mockAuthState },
  );
  return { useAuthStore };
});
vi.mock("../navigation/context", () => ({
  useNavigation: () => ({ push: mockPush }),
  useOptionalNavigation: () => ({ push: mockPush }),
}));
vi.mock("@multica/core/api", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  api: mockApi,
}));

import { ApiError } from "@multica/core/api";
import { BotConnectApprovePage } from "./approve-page";

const pending = {
  code: "ABCD-EFGH",
  bot_name: "Grok",
  bot_type: "grok",
  status: "pending",
  receives_push: false,
  expires_at: "2026-10-13T12:00:00Z",
};

function renderPage(code: string | null) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <I18nProvider locale="en" resources={{ en: { common: enCommon } }}>
        {children}
      </I18nProvider>
    </QueryClientProvider>
  );
  return render(<BotConnectApprovePage code={code} />, { wrapper });
}

describe("BotConnectApprovePage", () => {
  beforeEach(() => {
    mockAuthState.user = { id: "u1" };
    mockAuthState.isLoading = false;
    mockPush.mockReset();
    Object.values(mockApi).forEach((fn) => fn.mockReset());
    mockApi.listWorkspaces.mockResolvedValue([{ id: "ws-1", name: "Acme" }]);
    mockApi.getBotConnect.mockResolvedValue(pending);
  });

  it("asks a signed-out person to sign in and comes back to the same code", () => {
    mockAuthState.user = null;
    renderPage("ABCD-EFGH");
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(mockPush).toHaveBeenCalledWith(`/login?next=${encodeURIComponent("/bot-connect?code=ABCD-EFGH")}`);
    expect(mockApi.getBotConnect).not.toHaveBeenCalled();
  });

  it("connects the Bot with one yes, never showing a key", async () => {
    mockApi.approveBotConnect.mockResolvedValue({ ...pending, status: "approved", workspace_id: "ws-1" });
    renderPage("ABCD-EFGH");
    const agree = await screen.findByRole("button", { name: "Agree" });
    await waitFor(() => expect(agree).toBeEnabled());
    fireEvent.click(agree);
    await waitFor(() => expect(mockApi.approveBotConnect).toHaveBeenCalledWith("ABCD-EFGH", "ws-1"));
    expect(await screen.findByText(/"Grok" is connected to Acme/)).toBeInTheDocument();
    expect(document.body.textContent).not.toContain("mul_");
  });

  it("refuses when asked to", async () => {
    mockApi.denyBotConnect.mockResolvedValue({ ...pending, status: "denied" });
    renderPage("ABCD-EFGH");
    fireEvent.click(await screen.findByRole("button", { name: "Refuse" }));
    expect(await screen.findByText("You refused this request.")).toBeInTheDocument();
  });

  it("explains that only owners and admins can agree", async () => {
    mockApi.approveBotConnect.mockRejectedValue(new ApiError("forbidden", 403, "Forbidden"));
    renderPage("ABCD-EFGH");
    const agree = await screen.findByRole("button", { name: "Agree" });
    await waitFor(() => expect(agree).toBeEnabled());
    fireEvent.click(agree);
    expect(
      await screen.findByText("Only an owner or admin of this workspace can let a Bot in."),
    ).toBeInTheDocument();
  });

  it("says the link is broken when it has no code", () => {
    renderPage(null);
    expect(screen.getByText("The link has no code. Ask the Bot to send it again.")).toBeInTheDocument();
  });
});
