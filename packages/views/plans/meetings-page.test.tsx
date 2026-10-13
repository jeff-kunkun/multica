import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";
import { renderWithI18n } from "../test/i18n";

const h = vi.hoisted(() => ({
  createMutate: vi.fn(),
  push: vi.fn(),
  invalidate: vi.fn(),
}));

const AGENTS = [
  { id: "a-1", name: "Goku", archived_at: null },
  { id: "a-2", name: "Gohan", archived_at: null },
  { id: "a-3", name: "Vegeta", archived_at: null },
  { id: "a-4", name: "Piccolo", archived_at: null },
];

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: (opts: { queryKey: unknown[] }) => {
    const key = String(opts.queryKey[0]);
    if (key === "agents") return { data: AGENTS, isLoading: false };
    return { data: [], isLoading: false };
  },
  useQueryClient: () => ({ invalidateQueries: h.invalidate }),
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ issueDetail: (id: string) => `/acme/issues/${id}` }),
}));
vi.mock("@multica/core/plans", () => ({
  meetingListOptions: () => ({ queryKey: ["meetings"] }),
  planKeys: { meetings: () => ["meetings"] },
}));
vi.mock("@multica/core/issues/mutations", () => ({
  useCreateIssue: () => ({ mutate: h.createMutate, isPending: false }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  agentListOptions: () => ({ queryKey: ["agents"] }),
  memberListOptions: () => ({ queryKey: ["members"] }),
}));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (sel: (s: { user: { id: string } }) => unknown) => sel({ user: { id: "me" } }),
}));
vi.mock("../navigation", () => ({
  AppLink: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a>,
  useNavigation: () => ({ push: h.push }),
}));
vi.mock("../common/actor-avatar", () => ({ ActorAvatar: () => null }));
vi.mock("../issues/components/issue-agent-activity-indicator", () => ({
  IssueAgentActivityIndicator: () => null,
}));

import { MeetingsPage } from "./meetings-page";

function openDialog() {
  renderWithI18n(<MeetingsPage />);
  fireEvent.click(screen.getAllByRole("button", { name: "Start a meeting" })[0]!);
}

const pressed = () =>
  screen.getAllByRole("button", { pressed: true }).map((b) => b.textContent);

describe("MeetingsPage", () => {
  beforeEach(() => {
    h.createMutate.mockReset();
    h.push.mockReset();
  });

  it("picks one attendee by default and caps the pick at three", async () => {
    openDialog();
    expect(await screen.findByRole("button", { name: /Goku/, pressed: true })).toBeTruthy();
    expect(pressed()).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: /Gohan/ }));
    fireEvent.click(screen.getByRole("button", { name: /Vegeta/ }));
    fireEvent.click(screen.getByRole("button", { name: /Piccolo/ }));
    expect(pressed()).toHaveLength(3);
    expect(screen.getByRole("button", { name: /Piccolo/ })).toHaveAttribute("aria-pressed", "false");
  });

  it("creates a meeting issue assigned to the lead and opens it", async () => {
    openDialog();
    fireEvent.change(await screen.findByRole("textbox"), {
      target: { value: "Should we ship on Friday?" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Gohan/ }));
    fireEvent.click(screen.getByRole("button", { name: "Start" }));
    const [input, handlers] = h.createMutate.mock.calls[0]!;
    expect(input).toMatchObject({
      title: "Should we ship on Friday?",
      assignee_type: "agent",
      assignee_id: "a-1",
      meeting: true,
    });
    expect(input.description).toContain("[@Gohan](mention://agent/a-2)");
    handlers.onSuccess({ id: "issue-9" });
    expect(h.push).toHaveBeenCalledWith("/acme/issues/issue-9");
  });
});
