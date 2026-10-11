import { describe, expect, it } from "vitest";
import { WorkspaceLinksSchema, WorkspaceLinkSchema, LinkedViewSchema, ChatLinkedProjectSchema } from "./workspace-link-schemas";

const workspace = { name: "Source", slug: "source" };
const link = { id: "link", side: "viewer", status: "active", source: workspace, target: workspace };
describe("mobile workspace link response compatibility", () => {
  it("defaults missing and malformed abilities to denial", () => {
    const result = WorkspaceLinksSchema.parse({ links: [link], can: { create: "true" } });
    expect(Object.values(result.can)).toEqual([false, false, false, false, false]);
    expect(result.links[0]).toMatchObject({ managed: false, can_set_managed: false, can_update_projects: false });
  });
  it("does not turn unknown direction or status into a writable link", () => {
    expect(WorkspaceLinkSchema.safeParse({ ...link, side: "future" }).success).toBe(false);
    expect(WorkspaceLinkSchema.safeParse({ ...link, status: "revoked" }).success).toBe(false);
  });
  it("preserves custom statuses and pagination without leaking extra issue fields", () => {
    const result = LinkedViewSchema.parse({ link_id: "link", source: { name: "Source" }, projects: [], statuses: [{ key: "custom", name: "In QA", category: "started" }], next_cursor: "next", issues: [{ identifier: "A-1", title: "Title", status: "custom", priority: "high", project_id: "p", assignee_name: null, assignee_avatar_url: null, due_date: null, updated_at: "now", description: "private" }] });
    expect(result.next_cursor).toBe("next");
    expect(result.issues[0].status).toBe("custom");
    expect(result.issues[0]).not.toHaveProperty("description");
  });
  it("treats missing reference availability as unavailable", () => {
    expect(ChatLinkedProjectSchema.parse({ link_id: "link", project_id: "p" }).available).toBe(false);
  });
});
