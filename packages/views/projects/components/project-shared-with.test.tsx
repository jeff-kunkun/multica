import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderWithI18n } from "../../test/i18n";

const mocks = vi.hoisted(() => ({
  shared: [] as { user_id: string; created_at: string }[],
  listProjectMembers: vi.fn(),
  addProjectMember: vi.fn(),
  removeProjectMember: vi.fn(),
  listMembers: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    listProjectMembers: mocks.listProjectMembers,
    addProjectMember: mocks.addProjectMember,
    removeProjectMember: mocks.removeProjectMember,
    listMembers: mocks.listMembers,
  },
}));

vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: () => null,
}));

import { ProjectSharedWith } from "./project-shared-with";

const MEMBERS = [
  { user_id: "owner-1", name: "Olive Owner", role: "owner" },
  { user_id: "user-2", name: "Bea Member", role: "member" },
  { user_id: "user-3", name: "Cal Member", role: "member" },
];

function renderSharedWith(canEdit: boolean) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <ProjectSharedWith wsId="ws-1" projectId="proj-1" canEdit={canEdit} />
    </QueryClientProvider>,
  );
}

describe("ProjectSharedWith", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.shared = [{ user_id: "user-2", created_at: "2026-01-01T00:00:00Z" }];
    mocks.listProjectMembers.mockImplementation(async () => ({ members: mocks.shared }));
    mocks.listMembers.mockResolvedValue(MEMBERS);
    mocks.addProjectMember.mockImplementation(async (_projectId: string, userId: string) => {
      mocks.shared = [...mocks.shared, { user_id: userId, created_at: "" }];
      return { user_id: userId, created_at: "" };
    });
    mocks.removeProjectMember.mockImplementation(async (_projectId: string, userId: string) => {
      mocks.shared = mocks.shared.filter((m) => m.user_id !== userId);
    });
  });

  it("lets the workspace owner add and remove members, without offering owners", async () => {
    const user = userEvent.setup();
    renderSharedWith(true);

    await user.click(await screen.findByRole("button", { name: /Bea Member/ }));
    expect(screen.getByPlaceholderText("Search members...")).toBeInTheDocument();
    expect(screen.queryByText("Olive Owner")).not.toBeInTheDocument();

    const cal = screen.getByRole("button", { name: /Cal Member/ });
    expect(cal).toHaveAttribute("aria-pressed", "false");
    await user.click(cal);
    await waitFor(() => expect(mocks.addProjectMember).toHaveBeenCalledWith("proj-1", "user-3"));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /Cal Member/ })).toHaveAttribute("aria-pressed", "true"),
    );

    const bea = screen.getAllByRole("button", { name: /Bea Member/ }).find(
      (el) => el.getAttribute("aria-pressed") === "true",
    )!;
    await user.click(bea);
    await waitFor(() => expect(mocks.removeProjectMember).toHaveBeenCalledWith("proj-1", "user-2"));
  });

  it("shows members a read-only list", async () => {
    const user = userEvent.setup();
    renderSharedWith(false);

    await user.click(await screen.findByRole("button", { name: /Bea Member/ }));
    expect(screen.queryByPlaceholderText("Search members...")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { pressed: false })).not.toBeInTheDocument();
    expect(screen.queryByText("Cal Member")).not.toBeInTheDocument();
    expect(mocks.addProjectMember).not.toHaveBeenCalled();
    expect(mocks.removeProjectMember).not.toHaveBeenCalled();
  });

  it("says when nothing is shared", async () => {
    mocks.shared = [];
    renderSharedWith(true);
    expect(await screen.findByText("Not shared")).toBeInTheDocument();
  });
});
