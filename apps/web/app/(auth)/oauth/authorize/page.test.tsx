import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "@multica/views/locales/en/common.json";
import enAuth from "@multica/views/locales/en/auth.json";
import type { ReactNode } from "react";

const TEST_RESOURCES = { en: { common: enCommon, auth: enAuth } };

function createWrapper() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return ({ children }: { children: ReactNode }) => (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    </I18nProvider>
  );
}

const { mockListOAuthBots, mockApproveOAuth, mockReplace, searchParamsState, authState } =
  vi.hoisted(() => ({
    mockListOAuthBots: vi.fn(),
    mockApproveOAuth: vi.fn(),
    mockReplace: vi.fn(),
    searchParamsState: { params: new URLSearchParams() },
    authState: { user: null as null | { id: string }, isLoading: false },
  }));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: mockReplace }),
  useSearchParams: () => searchParamsState.params,
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: Object.assign(
    (selector: (s: typeof authState) => unknown) => selector(authState),
    { getState: () => authState },
  ),
}));

vi.mock("@multica/core/api", () => ({
  api: { listOAuthBots: mockListOAuthBots, approveOAuth: mockApproveOAuth },
}));

import OAuthAuthorizePage from "./page";

const REQUEST = {
  response_type: "code",
  client_id: "mcp_1",
  redirect_uri: "https://gemini.google.com/cb",
  code_challenge: "c".repeat(43),
  code_challenge_method: "S256",
  state: "s1",
};

describe("OAuthAuthorizePage", () => {
  const hrefSetter = vi.fn();
  const originalLocation = window.location;

  beforeEach(() => {
    vi.clearAllMocks();
    searchParamsState.params = new URLSearchParams(REQUEST);
    authState.user = { id: "u1" };
    authState.isLoading = false;
    mockListOAuthBots.mockResolvedValue([
      { id: "w1", name: "Office", bots: [{ id: "b1", name: "Grokbot" }] },
    ]);
    Object.defineProperty(window, "location", {
      configurable: true,
      value: { ...originalLocation, set href(value: string) { hrefSetter(value); } },
    });
  });

  afterEach(() => {
    Object.defineProperty(window, "location", { configurable: true, value: originalLocation });
  });

  it("sends a signed-out visitor to login and back", () => {
    authState.user = null;
    render(<OAuthAuthorizePage />, { wrapper: createWrapper() });
    const target = mockReplace.mock.calls[0]?.[0] as string;
    expect(target.startsWith("/login?next=")).toBe(true);
    expect(decodeURIComponent(target.slice("/login?next=".length))).toBe(
      `/oauth/authorize?${new URLSearchParams(REQUEST).toString()}`,
    );
  });

  it("creates a new Bot by default and follows the returned redirect", async () => {
    mockApproveOAuth.mockResolvedValue("https://gemini.google.com/cb?code=x&state=s1");
    render(<OAuthAuthorizePage />, { wrapper: createWrapper() });

    const name = await screen.findByLabelText("Name");
    expect(name).toHaveValue("gemini.google.com");
    await userEvent.clear(name);
    await userEvent.type(name, "Spark");
    await userEvent.click(screen.getByRole("button", { name: "Allow" }));

    await waitFor(() => expect(hrefSetter).toHaveBeenCalledWith("https://gemini.google.com/cb?code=x&state=s1"));
    expect(mockApproveOAuth).toHaveBeenCalledWith({
      workspace_id: "w1",
      bot_name: "Spark",
      client_id: "mcp_1",
      redirect_uri: "https://gemini.google.com/cb",
      code_challenge: "c".repeat(43),
      code_challenge_method: "S256",
      state: "s1",
    });
  });

  it("returns access_denied to the client on cancel", async () => {
    render(<OAuthAuthorizePage />, { wrapper: createWrapper() });
    await screen.findByLabelText("Name");
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(hrefSetter).toHaveBeenCalledWith("https://gemini.google.com/cb?error=access_denied&state=s1");
  });

  it("explains an incomplete request instead of offering Allow", () => {
    searchParamsState.params = new URLSearchParams({ ...REQUEST, code_challenge: "" });
    render(<OAuthAuthorizePage />, { wrapper: createWrapper() });
    expect(screen.getByText(/link is incomplete/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Allow" })).toBeNull();
    expect(mockListOAuthBots).not.toHaveBeenCalled();
  });

  it("tells a non-owner only owners can connect", async () => {
    mockListOAuthBots.mockResolvedValue([]);
    render(<OAuthAuthorizePage />, { wrapper: createWrapper() });
    expect(await screen.findByText(/Only a workspace owner/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Allow" })).toBeDisabled();
  });
});
