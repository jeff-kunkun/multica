// @vitest-environment node
import { describe, expect, it } from "vitest";
import { parseWithFallback } from "./schema";
import { EMPTY_ISSUE_PLANS, IssuePlansSchema, PlanApproverListSchema } from "./schemas";

describe("plan schemas", () => {
  const plan = {
    id: "plan-1",
    issue_id: "issue-1",
    version: 2,
    status: "proposed",
    goal: "Ship it",
    steps: [{ title: "Build", assignee_type: "agent", assignee_id: "a-1", issue_id: null }],
    comment_id: "c-1",
  };

  it("keeps good plans and defaults missing approval to false", () => {
    const parsed = IssuePlansSchema.parse({ plans: [plan] });
    expect(parsed.can_approve).toBe(false);
    expect(parsed.plans[0]?.steps[0]?.title).toBe("Build");
    expect(parsed.plans[0]?.approved_by).toBeNull();
    expect(parsed.plans[0]?.project_id).toBeNull();
  });

  it("keeps the plan's project and treats a bad one as general", () => {
    expect(IssuePlansSchema.parse({ plans: [{ ...plan, project_id: "p-1" }] }).plans[0]?.project_id).toBe("p-1");
    expect(IssuePlansSchema.parse({ plans: [{ ...plan, project_id: 7 }] }).plans[0]?.project_id).toBeNull();
  });

  it("falls back on malformed plan payloads without hiding the card", () => {
    const parsed = IssuePlansSchema.parse({
      plans: [{ ...plan, steps: "nope", version: "x" }],
      can_approve: "yes",
    });
    expect(parsed.can_approve).toBe(false);
    expect(parsed.plans[0]?.steps).toEqual([]);
    expect(parsed.plans[0]?.version).toBe(0);
    expect(
      parseWithFallback("garbage", IssuePlansSchema, EMPTY_ISSUE_PLANS, {
        endpoint: "GET /api/issues/{id}/plans",
      }),
    ).toEqual(EMPTY_ISSUE_PLANS);
  });

  it("defaults approver fields and survives a non-object reply", () => {
    const parsed = PlanApproverListSchema.parse({ approvers: [{ user_id: "u-1" }] });
    expect(parsed.approvers[0]).toEqual({ user_id: "u-1", name: "", granted_by: "", created_at: "" });
    expect(PlanApproverListSchema.parse({ approvers: "x" }).approvers).toEqual([]);
  });
});
