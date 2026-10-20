import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Workspace } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const mocks = vi.hoisted(() => ({ mutate: vi.fn() }));

vi.mock("@multica/core/workspace/magpie", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/workspace/magpie")>()),
  useUpdateWorkspaceMagpie: () => ({ mutate: mocks.mutate, isPending: false, variables: undefined }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { MagpieModeSection } from "./magpie-mode-section";

function workspace(settings: Record<string, unknown>): Workspace {
  return { id: "ws-1", settings } as unknown as Workspace;
}

beforeEach(() => vi.clearAllMocks());

describe("MagpieModeSection", () => {
  it("is off for a workspace without the setting, and turns it on", async () => {
    renderWithI18n(<MagpieModeSection workspace={workspace({})} canManage />);
    const toggle = screen.getByRole("switch", { name: "Connect to Magpie" });
    expect(toggle).not.toBeChecked();
    expect(screen.getByTestId("magpie-state")).toHaveTextContent("Not connected to Magpie");
    await userEvent.click(toggle);
    expect(mocks.mutate).toHaveBeenCalledWith(true, expect.anything());
  });

  it("shows the stored state and is read-only for non-admins", () => {
    renderWithI18n(<MagpieModeSection workspace={workspace({ magpie: { enabled: true } })} canManage={false} />);
    const toggle = screen.getByRole("switch", { name: "Connect to Magpie" });
    expect(toggle).toBeChecked();
    expect(screen.getByTestId("magpie-state")).toHaveTextContent("Connected to Magpie");
    expect(toggle).toHaveAttribute("aria-disabled", "true");
  });
});
