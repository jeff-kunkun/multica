import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { configStore } from "@multica/core/config";

// The shared LoginPage renders the Google button whenever `onGoogleLogin` is
// passed. These tests pin the desktop wiring: the handler reaches it only when
// the connected server advertises a Google client id.
vi.mock("@multica/views/auth", () => ({
  LoginPage: ({ onGoogleLogin }: { onGoogleLogin?: () => void }) =>
    onGoogleLogin ? (
      <button type="button" onClick={onGoogleLogin}>
        Continue with Google
      </button>
    ) : null,
}));

vi.mock("@multica/views/platform", () => ({ DragStrip: () => null }));
vi.mock("@multica/ui/components/common/multica-icon", () => ({
  MulticaIcon: () => null,
}));

const { DesktopLoginPage } = await import("./login");

function setGoogleClientId(googleClientId: string) {
  configStore.getState().setAuthConfig({ allowSignup: true, googleClientId });
}

beforeEach(() => {
  (window as unknown as { desktopAPI: Record<string, unknown> }).desktopAPI = {
    runtimeConfig: { ok: true, config: { appUrl: "https://ai.example.test" } },
    openExternal: vi.fn(),
  };
});

describe("DesktopLoginPage Google entry", () => {
  it("hides the Google button when the server advertises no client id", () => {
    setGoogleClientId("");
    render(<DesktopLoginPage />);
    expect(
      screen.queryByRole("button", { name: /continue with google/i }),
    ).not.toBeInTheDocument();
  });

  it("keeps the Google button when the server advertises a client id", () => {
    setGoogleClientId("goog-123");
    render(<DesktopLoginPage />);
    expect(
      screen.getByRole("button", { name: /continue with google/i }),
    ).toBeInTheDocument();
  });
});
