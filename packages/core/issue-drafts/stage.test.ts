// @vitest-environment node
import { describe, expect, it } from "vitest";
import { issueDraftCanConfirm, issueDraftStage, issueDraftStageIndex } from "./stage";

describe("issueDraftStage", () => {
  it("maps the server's live statuses onto the first two stages", () => {
    expect(
      issueDraftStage({ status: "draft", creating: false, createdIssueId: null }),
    ).toBe("aligning");
    expect(
      issueDraftStage({ status: "ready", creating: false, createdIssueId: null }),
    ).toBe("ready");
  });

  it("shows creating while a confirm is in flight, whatever the status says", () => {
    // Finalize keeps the draft at `ready` until the issue exists, so the
    // status alone would keep offering a create that is already underway.
    expect(
      issueDraftStage({ status: "ready", creating: true, createdIssueId: null }),
    ).toBe("creating");
  });

  it("prefers created over everything once an issue exists", () => {
    expect(
      issueDraftStage({
        status: "completed",
        creating: false,
        createdIssueId: "issue-1",
      }),
    ).toBe("created");
    expect(
      issueDraftStage({
        status: "ready",
        creating: true,
        createdIssueId: "issue-1",
      }),
    ).toBe("created");
  });

  it("does not offer an actionable stage for terminal drafts", () => {
    // A completed draft without its issue id, or an abandoned one, has no
    // create left to offer; showing a stage that does would promise a write
    // the server refuses.
    expect(
      issueDraftStage({
        status: "abandoned",
        creating: false,
        createdIssueId: null,
      }),
    ).toBe("aligning");
    expect(
      issueDraftStage({
        status: "completed",
        creating: false,
        createdIssueId: null,
      }),
    ).toBe("aligning");
  });
});

describe("issueDraftStageIndex", () => {
  it("orders the four stages", () => {
    expect(issueDraftStageIndex("aligning")).toBe(0);
    expect(issueDraftStageIndex("ready")).toBe(1);
    expect(issueDraftStageIndex("creating")).toBe(2);
    expect(issueDraftStageIndex("created")).toBe(3);
  });
});

describe("issueDraftCanConfirm", () => {
  it("needs the server's ready status, a title, and no turn in flight", () => {
    expect(issueDraftCanConfirm({ stage: "ready", hasTitle: true, pending: false })).toBe(true);
    expect(issueDraftCanConfirm({ stage: "aligning", hasTitle: true, pending: false })).toBe(false);
    expect(issueDraftCanConfirm({ stage: "ready", hasTitle: false, pending: false })).toBe(false);
    expect(issueDraftCanConfirm({ stage: "ready", hasTitle: true, pending: true })).toBe(false);
    expect(issueDraftCanConfirm({ stage: "creating", hasTitle: true, pending: false })).toBe(false);
  });
});
