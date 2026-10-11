import { describe, expect, it } from "vitest";
import { linkedRows } from "./linked-project-rows";

describe("linked project choices", () => {
  it("keeps revoked references removable without offering them as live choices", () => {
    const rows = linkedRows([], [{ link_id: "revoked", project_id: "project", title: "Source", icon: null, source_name: "Other workspace", available: false }]);
    expect(rows).toEqual([expect.objectContaining({ available: false, ref: { link_id: "revoked", project_id: "project" } })]);
  });
  it("does not conflate the same project offered through different links", () => {
    const options = ["first", "second"].map((link_id) => ({ link_id, id: "project", title: "Source", icon: null, source: { name: "Other workspace", avatar_url: null } }));
    expect(new Set(linkedRows(options, []).map((row) => row.key)).size).toBe(2);
  });
});
