// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { Issue } from "../types";
import { deriveBlockerTree } from "./blocker-tree";

function issue(id: string, overrides: Partial<Issue> = {}): Issue {
  return {
    id, workspace_id: "w", number: 1, identifier: id, title: id,
    description: null, status: "todo", priority: "none", assignee_type: null,
    assignee_id: null, creator_type: "member", creator_id: "u", parent_issue_id: null,
    project_id: null, position: 0, stage: null, start_date: null, due_date: null,
    metadata: {}, properties: {}, created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z", ...overrides,
  };
}

describe("deriveBlockerTree", () => {
  it("marks an attributed blocked child ROOT and its parent PROPAGATED", () => {
    const child = issue("DENE-2", { parent_issue_id: "DENE-1", stage: 1, metadata: {
      "close.conclusion": "blocked", "close.block_kind": "decision", "close.block_action": "decide",
    }});
    const root = issue("DENE-1");
    const result = deriveBlockerTree(root, { childrenByParent: new Map([[root.id, [child]]]) });
    expect(result.state).toBe("PROPAGATED");
    expect(result.rootCauses.map((x) => x.id)).toEqual([child.id]);
    expect(result.nodes.get(child.id)?.state).toBe("ROOT");
  });

  it("does not call a normal in_review hand-off a blocker", () => {
    const root = issue("DENE-1", { status: "in_review", metadata: {
      "close.conclusion": "awaiting_review", "close.status": "in_review",
      "close.at": "2026-09-16T00:00:00Z", "close.next_owner_type": "member",
    }});
    expect(deriveBlockerTree(root, { now: "2026-09-16T12:00:00Z" }).state).toBe("CLEAR");
  });

  it("treats a dependency whose target is done as a derived ROOT", () => {
    const target = issue("DENE-9", { status: "done" });
    const waiting = issue("DENE-2", { metadata: { "close.waiting_on": target.identifier } });
    const result = deriveBlockerTree(waiting, { issueByIdentifier: { [target.identifier]: target } });
    expect(result.state).toBe("ROOT");
    expect(result.derived).toBe(true);
  });

  it("does not count capacity blockers as requiring a human action", () => {
    const root = issue("DENE-1", { metadata: {
      "close.conclusion": "blocked", "close.block_kind": "capacity", "close.block_action": "wait",
    }});
    expect(deriveBlockerTree(root).userActionCount).toBe(0);
  });
});
