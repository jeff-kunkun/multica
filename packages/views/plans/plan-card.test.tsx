import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { renderWithI18n } from "../test/i18n";

const approveMutate = vi.fn();
const commentMutate = vi.fn();
const queryData = vi.hoisted(() => ({ plans: undefined as unknown }));

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: (opts: { queryKey: unknown[] }) => {
    const key = String(opts.queryKey[0]);
    if (key === "plans") return { data: queryData.plans };
    if (key === "agents") return { data: [{ id: "a-1", name: "Goku", archived_at: null }] };
    if (key === "projects") return { data: [{ id: "proj-1", title: "Office revamp", icon: "🏢" }] };
    return { data: [] };
  },
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/plans", () => ({
  issuePlansOptions: () => ({ queryKey: ["plans"] }),
  useApprovePlan: () => ({ mutate: approveMutate, isPending: false }),
}));
vi.mock("@multica/core/issues/queries", () => ({
  childIssuesOptions: () => ({ queryKey: ["children"] }),
}));
vi.mock("@multica/core/projects/queries", () => ({
  projectListOptions: () => ({ queryKey: ["projects"] }),
}));
vi.mock("@multica/core/issues/mutations", () => ({
  useCreateComment: () => ({ mutate: commentMutate, isPending: false }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  agentListOptions: () => ({ queryKey: ["agents"] }),
  memberListOptions: () => ({ queryKey: ["members"] }),
}));
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: () => "Kun" }),
}));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (sel: (s: { user: { id: string } }) => unknown) => sel({ user: { id: "me" } }),
}));
vi.mock("../common/actor-avatar", () => ({ ActorAvatar: () => null }));
vi.mock("../issues/components/issue-mention-card", () => ({ IssueMentionCard: () => null }));

import { IssuePlanCard } from "./plan-card";

const plan = {
  id: "p-1",
  issue_id: "issue-1",
  version: 1,
  status: "proposed",
  author_type: "agent",
  author_id: "a-1",
  goal: "Ship the plan card",
  out_of_scope: "",
  steps: [{ title: "Build it", assignee_type: "agent", assignee_id: "a-1", issue_id: null }],
  done_when: "",
  project_id: "proj-1",
  comment_id: "c-plan",
  approved_by: null,
  approved_at: null,
  created_at: "",
};

describe("IssuePlanCard", () => {
  beforeEach(() => {
    approveMutate.mockReset();
    commentMutate.mockReset();
  });

  it("renders nothing without a plan", () => {
    queryData.plans = { plans: [], can_approve: true };
    const { container } = renderWithI18n(<IssuePlanCard issueId="issue-1" />);
    expect(container).toBeEmptyDOMElement();
  });

  it("shows the project the plan files its work in", () => {
    queryData.plans = { plans: [plan], can_approve: true };
    renderWithI18n(<IssuePlanCard issueId="issue-1" />);
    expect(screen.getByText("Project")).toBeInTheDocument();
    expect(screen.getByText("Office revamp")).toBeInTheDocument();
  });

  it("says general when the plan names no project", () => {
    queryData.plans = { plans: [{ ...plan, project_id: null }], can_approve: true };
    renderWithI18n(<IssuePlanCard issueId="issue-1" />);
    expect(screen.getByText("General (no project)")).toBeInTheDocument();
    expect(screen.queryByText("Office revamp")).toBeNull();
  });

  it("approves the shown version when allowed", () => {
    queryData.plans = { plans: [plan], can_approve: true };
    renderWithI18n(<IssuePlanCard issueId="issue-1" />);
    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(approveMutate).toHaveBeenCalledWith(
      { issueId: "issue-1", version: 1 },
      expect.anything(),
    );
  });

  it("disables approve for people without the right", () => {
    queryData.plans = { plans: [plan], can_approve: false };
    renderWithI18n(<IssuePlanCard issueId="issue-1" />);
    expect(screen.getByRole("button", { name: "Approve" })).toBeDisabled();
  });

  it("asks someone to cut the plan with a mention under the plan comment", async () => {
    queryData.plans = { plans: [plan], can_approve: true };
    renderWithI18n(<IssuePlanCard issueId="issue-1" />);
    fireEvent.click(screen.getByRole("button", { name: "Ask someone to cut" }));
    fireEvent.click(await screen.findByRole("button", { name: /Goku/ }));
    fireEvent.click(screen.getByRole("button", { name: "Ask" }));
    await waitFor(() => expect(commentMutate).toHaveBeenCalled());
    const [input] = commentMutate.mock.calls[0]!;
    expect(input.parentId).toBe("c-plan");
    expect(input.content).toContain("[@Goku](mention://agent/a-1)");
  });
});
