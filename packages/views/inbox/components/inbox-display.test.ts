// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { InboxItem } from "@multica/core/types";
import { resolveDetailItem } from "./inbox-display";

function item(overrides: Partial<InboxItem>): InboxItem {
  return {
    id: "inbox-1",
    workspace_id: "workspace-1",
    recipient_type: "member",
    recipient_id: "member-1",
    actor_type: "agent",
    actor_id: "agent-1",
    type: "new_comment",
    severity: "info",
    issue_id: "issue-1",
    title: "Issue title",
    body: null,
    issue_status: null,
    read: false,
    archived: false,
    created_at: "2026-04-29T12:00:00Z",
    details: null,
    ...overrides,
  };
}

describe("resolveDetailItem", () => {
  // Rows the inbox keys by issue_id, plus one plain notification that has none
  // and is therefore keyed by its own id.
  const a = item({ id: "inbox-a", issue_id: "issue-a" });
  const b = item({ id: "inbox-b", issue_id: "issue-b" });
  const plain = item({ id: "inbox-plain", issue_id: null });
  const list = [a, b, plain];

  it("holds the row the pane is still on while the list has moved on", () => {
    expect(resolveDetailItem(list, "issue-b", "issue-a")).toBe(a);
  });

  it("shows the live selection once the pane has caught up", () => {
    expect(resolveDetailItem(list, "issue-b", "issue-b")).toBe(b);
  });

  it("keys a notification without an issue by its own id", () => {
    expect(resolveDetailItem(list, "inbox-plain", "inbox-plain")).toBe(plain);
  });

  // Archive moves the selection to the neighbour and drops the actioned row in
  // the same commit, so the deferred key resolves to nothing. Holding that miss
  // would blink the empty state between the two issues.
  it("falls forward to the live selection when the held row is gone", () => {
    expect(resolveDetailItem([b, plain], "issue-b", "issue-a")).toBe(b);
  });

  it("stays empty while the first selection is still rendering", () => {
    expect(resolveDetailItem(list, "issue-a", "")).toBeNull();
  });

  it("stays empty when the selection is cleared and the pane agrees", () => {
    expect(resolveDetailItem(list, "", "")).toBeNull();
  });

  // Deep link to a notification that is not in this user's inbox: the page
  // redirects to the issue page, and the pane must not invent a row meanwhile.
  it("resolves nothing when neither key is in the list", () => {
    expect(resolveDetailItem(list, "issue-gone", "issue-also-gone")).toBeNull();
  });
});
