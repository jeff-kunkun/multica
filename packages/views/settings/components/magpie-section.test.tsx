import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Workspace } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const mocks = vi.hoisted(() => ({
  mutate: vi.fn(),
}));

vi.mock("@multica/core/workspace/magpie", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/workspace/magpie")>()),
  useUpdateWorkspaceMagpie: () => ({ mutate: mocks.mutate, isPending: false, variables: undefined }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { MagpieSection } from "./magpie-section";

function workspace(settings: Record<string, unknown>): Workspace {
  return { id: "ws-1", settings } as unknown as Workspace;
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("MagpieSection", () => {
  it("reads the stored switch and lets the owner flip it", async () => {
    renderWithI18n(<MagpieSection workspace={workspace({ magpie: { enabled: true } })} canManage />);
    const toggle = screen.getByRole("switch", { name: "Connect Magpie" });
    expect(toggle).toBeChecked();

    await userEvent.click(toggle);
    expect(mocks.mutate).toHaveBeenCalledWith(false, expect.any(Object));
  });

  it("is off by default and read-only for non-owners, saying who can change it", () => {
    renderWithI18n(<MagpieSection workspace={workspace({})} canManage={false} />);
    const toggle = screen.getByRole("switch", { name: "Connect Magpie" });
    expect(toggle).not.toBeChecked();
    expect(toggle).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByText("Only the workspace owner can change this.")).toBeInTheDocument();
  });
});
