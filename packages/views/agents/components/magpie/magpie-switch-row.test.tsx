import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";
import { renderWithI18n } from "../../../test/i18n";
import { MagpieSwitchRow } from "./magpie-switch-row";

const mocks = vi.hoisted(() => ({
  role: "owner" as string | null,
  available: true,
  enabled: false,
  mutate: vi.fn(),
  queryEnabled: undefined as boolean | undefined,
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { enabled?: boolean }) => {
    mocks.queryEnabled = options.enabled;
    return { data: options.enabled ? { enabled: mocks.enabled, available: mocks.available } : undefined };
  },
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "ws-1", settings: { magpie: { enabled: mocks.enabled } } }),
}));

vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ role: mocks.role }),
}));

vi.mock("@multica/core/workspace/magpie", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/workspace/magpie")>()),
  workspaceMagpieOptions: () => ({ queryKey: ["magpie"] }),
  useUpdateWorkspaceMagpie: () => ({ mutate: mocks.mutate, isPending: false, variables: undefined }),
}));

beforeEach(() => {
  mocks.role = "owner";
  mocks.available = true;
  mocks.enabled = false;
  mocks.mutate.mockReset();
});

describe("MagpieSwitchRow", () => {
  it("lets the owner connect a workspace that has magpie", () => {
    renderWithI18n(<MagpieSwitchRow />);
    const toggle = screen.getByRole("switch", { name: /^Connect to Magpie/ });
    expect(toggle).not.toBeChecked();
    fireEvent.click(toggle);
    expect(mocks.mutate).toHaveBeenCalledWith(true, expect.anything());
  });

  it("shows the stored state", () => {
    mocks.enabled = true;
    renderWithI18n(<MagpieSwitchRow />);
    expect(screen.getByRole("switch", { name: /^Connect to Magpie/ })).toBeChecked();
  });

  it("is hidden where no runtime reports magpie", () => {
    mocks.available = false;
    renderWithI18n(<MagpieSwitchRow />);
    expect(screen.queryByRole("switch")).not.toBeInTheDocument();
  });

  it("is hidden from anyone but the owner, who never asks the server", () => {
    for (const role of ["admin", "member", null]) {
      mocks.role = role;
      const { unmount } = renderWithI18n(<MagpieSwitchRow />);
      expect(screen.queryByRole("switch")).not.toBeInTheDocument();
      expect(mocks.queryEnabled).toBe(false);
      unmount();
    }
  });
});
