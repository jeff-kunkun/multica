import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement, ReactNode } from "react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../locales/en/common.json";
import enAuth from "../locales/en/auth.json";
import enSettings from "../locales/en/settings.json";

const TEST_RESOURCES = {
  en: { common: enCommon, auth: enAuth, settings: enSettings },
};

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

function renderWithI18n(ui: ReactElement) {
  return render(ui, { wrapper: I18nWrapper });
}

const mockSignupWithPassword = vi.hoisted(() => vi.fn());
const mockApiListWorkspaces = vi.hoisted(() => vi.fn());
const mockSetQueryData = vi.hoisted(() => vi.fn());
const mockConfigState = vi.hoisted(() => ({
  passwordAuth: true,
  allowSignup: true,
  signupTotpRequired: false,
}));

vi.mock("@tanstack/react-query", async () => {
  const actual = await vi.importActual<typeof import("@tanstack/react-query")>(
    "@tanstack/react-query",
  );
  return { ...actual, useQueryClient: () => ({ setQueryData: mockSetQueryData }) };
});

vi.mock("@multica/core/auth", () => ({
  useAuthStore: Object.assign(
    (selector?: (s: unknown) => unknown) => {
      const state = { signupWithPassword: mockSignupWithPassword };
      return selector ? selector(state) : state;
    },
    {
      getState: () => ({
        signupWithPassword: mockSignupWithPassword,
      }),
    },
  ),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    listWorkspaces: mockApiListWorkspaces,
  },
}));

vi.mock("@multica/core/config", () => ({
  useConfigStore: (
    selector?: (s: {
      passwordAuth: boolean;
      allowSignup: boolean;
      signupTotpRequired: boolean;
    }) => unknown,
  ) => {
    const state = {
      passwordAuth: mockConfigState.passwordAuth,
      allowSignup: mockConfigState.allowSignup,
      signupTotpRequired: mockConfigState.signupTotpRequired,
    };
    return selector ? selector(state) : state;
  },
}));

vi.mock("@multica/core/types", () => ({}));

import { SignupPage } from "./signup-page";

describe("SignupPage", () => {
  const onSuccess = vi.fn();

  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.clearAllMocks();
    mockConfigState.passwordAuth = true;
    mockConfigState.allowSignup = true;
    mockConfigState.signupTotpRequired = false;
    mockApiListWorkspaces.mockResolvedValue([]);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("renders username, email, and password fields", () => {
    renderWithI18n(<SignupPage onSuccess={onSuccess} />);
    expect(screen.getByText(/create your account/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/username/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/^email$/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/^password$/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/confirm password/i)).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /create account/i }),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /^sign in$/i })).toHaveAttribute(
      "href",
      "/login",
    );
    expect(screen.queryByLabelText(/team 2fa code/i)).not.toBeInTheDocument();
  });

  it("shows disabled copy when signup is off", () => {
    mockConfigState.allowSignup = false;
    renderWithI18n(<SignupPage onSuccess={onSuccess} />);
    expect(
      screen.getByText(/user registration is disabled/i),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText(/username/i)).not.toBeInTheDocument();
  });

  it("rejects mismatched passwords", async () => {
    renderWithI18n(<SignupPage onSuccess={onSuccess} />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText(/username/i), "newbie");
    await user.type(screen.getByLabelText(/^email$/i), "newbie@example.com");
    await user.type(screen.getByLabelText(/^password$/i), "correct-horse");
    await user.type(screen.getByLabelText(/confirm password/i), "other-horse");
    await user.click(screen.getByRole("button", { name: /create account/i }));

    await waitFor(() => {
      expect(screen.getByText(/passwords do not match/i)).toBeInTheDocument();
    });
    expect(mockSignupWithPassword).not.toHaveBeenCalled();
    expect(onSuccess).not.toHaveBeenCalled();
  });

  it("calls signupWithPassword and onSuccess", async () => {
    mockSignupWithPassword.mockResolvedValueOnce(undefined);
    renderWithI18n(<SignupPage onSuccess={onSuccess} />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText(/username/i), "newbie");
    await user.type(screen.getByLabelText(/^email$/i), "newbie@example.com");
    await user.type(screen.getByLabelText(/^password$/i), "correct-horse");
    await user.type(screen.getByLabelText(/confirm password/i), "correct-horse");
    await user.click(screen.getByRole("button", { name: /create account/i }));

    await waitFor(() => {
      expect(mockSignupWithPassword).toHaveBeenCalledWith(
        "newbie",
        "correct-horse",
        "newbie@example.com",
      );
      expect(onSuccess).toHaveBeenCalled();
    });
  });

  it("shows the team 2FA field when signup TOTP is required", () => {
    mockConfigState.signupTotpRequired = true;
    renderWithI18n(<SignupPage onSuccess={onSuccess} />);
    expect(screen.getByLabelText(/team 2fa code/i)).toBeInTheDocument();
    expect(
      screen.getByText(/shared team authenticator/i),
    ).toBeInTheDocument();
  });

  it("does not submit without a team 2FA code when required", async () => {
    mockConfigState.signupTotpRequired = true;
    renderWithI18n(<SignupPage onSuccess={onSuccess} />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText(/username/i), "newbie");
    await user.type(screen.getByLabelText(/^email$/i), "newbie@example.com");
    await user.type(screen.getByLabelText(/^password$/i), "correct-horse");
    await user.type(screen.getByLabelText(/confirm password/i), "correct-horse");
    expect(
      screen.getByRole("button", { name: /create account/i }),
    ).toBeDisabled();
    expect(mockSignupWithPassword).not.toHaveBeenCalled();
  });

  it("sends the team 2FA code when signup TOTP is required", async () => {
    mockConfigState.signupTotpRequired = true;
    mockSignupWithPassword.mockResolvedValueOnce(undefined);
    renderWithI18n(<SignupPage onSuccess={onSuccess} />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText(/username/i), "newbie");
    await user.type(screen.getByLabelText(/^email$/i), "newbie@example.com");
    await user.type(screen.getByLabelText(/^password$/i), "correct-horse");
    await user.type(screen.getByLabelText(/confirm password/i), "correct-horse");
    await user.type(screen.getByLabelText(/team 2fa code/i), "123456");
    await user.click(screen.getByRole("button", { name: /create account/i }));

    await waitFor(() => {
      expect(mockSignupWithPassword).toHaveBeenCalledWith(
        "newbie",
        "correct-horse",
        "newbie@example.com",
        "123456",
      );
      expect(onSuccess).toHaveBeenCalled();
    });
  });
});
