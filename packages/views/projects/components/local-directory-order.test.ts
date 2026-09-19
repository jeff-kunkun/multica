// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { ProjectResource } from "@multica/core/types";
import {
  canMoveLocalDirectory,
  localDirectoryIndexes,
  moveLocalDirectory,
} from "./local-directory-order";

// Canonical matrix for the ordering rule (DENE-617). Order decides which
// directory a run WRITES, so these cases are about that decision, not about
// how the list looks.

function localDir(id: string, position: number, daemonId: string): ProjectResource {
  return {
    id,
    project_id: "p1",
    workspace_id: "w1",
    resource_type: "local_directory",
    resource_ref: { local_path: `/Users/me/${id}`, daemon_id: daemonId },
    label: null,
    position,
    created_at: "2026-01-01T00:00:00Z",
    created_by: null,
  };
}

function repo(id: string, position: number): ProjectResource {
  return {
    id,
    project_id: "p1",
    workspace_id: "w1",
    resource_type: "github_repo",
    resource_ref: { url: `https://github.com/o/${id}` },
    label: null,
    position,
    created_at: "2026-01-01T00:00:00Z",
    created_by: null,
  };
}

/** Apply patches and read back the resulting order, as the server would. */
function orderAfter(
  resources: ProjectResource[],
  patches: { resourceId: string; position: number }[],
): string[] {
  const byId = new Map(patches.map((p) => [p.resourceId, p.position]));
  return resources
    .map((r) => ({ id: r.id, position: byId.get(r.id) ?? r.position }))
    .sort((a, b) => a.position - b.position)
    .map((r) => r.id);
}

describe("moveLocalDirectory", () => {
  it("moves a directory past its nearest sibling on the same machine", () => {
    const resources = [localDir("a", 0, "d1"), localDir("b", 1, "d1"), localDir("c", 2, "d1")];

    expect(orderAfter(resources, moveLocalDirectory(resources, "c", "d1", "up"))).toEqual([
      "a",
      "c",
      "b",
    ]);
    expect(orderAfter(resources, moveLocalDirectory(resources, "a", "d1", "down"))).toEqual([
      "b",
      "a",
      "c",
    ]);
  });

  // The user is choosing which directory a run writes. A repository row sitting
  // between two directories is not part of that choice, so moving past it must
  // actually change which directory comes first.
  it("skips rows that are not this machine's directories", () => {
    const resources = [
      localDir("a", 0, "d1"),
      repo("r", 1),
      localDir("other", 2, "d2"),
      localDir("b", 3, "d1"),
    ];

    const order = orderAfter(resources, moveLocalDirectory(resources, "b", "d1", "up"));
    expect(order.indexOf("b")).toBeLessThan(order.indexOf("a"));
  });

  // An edge move, an unknown row and a foreign machine's row all produce no
  // write at all — a caller that skipped the disabled check cannot send one.
  it("returns nothing when there is no sibling to move past", () => {
    const resources = [localDir("a", 0, "d1"), localDir("b", 1, "d1"), localDir("other", 2, "d2")];

    expect(moveLocalDirectory(resources, "a", "d1", "up")).toEqual([]);
    expect(moveLocalDirectory(resources, "b", "d1", "down")).toEqual([]);
    expect(moveLocalDirectory(resources, "other", "d1", "up")).toEqual([]);
    expect(moveLocalDirectory(resources, "missing", "d1", "up")).toEqual([]);
    expect(moveLocalDirectory(resources, "a", null, "down")).toEqual([]);
  });

  // Rows created without an explicit position can tie, and the list then falls
  // back to created_at. Renumbering has to state the order outright rather than
  // swap two numbers that are equal — a swap here would be a no-op.
  it("states the resulting order even when the stored positions tie", () => {
    const resources = [localDir("a", 0, "d1"), localDir("b", 0, "d1")];

    const patches = moveLocalDirectory(resources, "b", "d1", "up");
    expect(patches.length).toBeGreaterThan(0);
    expect(orderAfter(resources, patches)).toEqual(["b", "a"]);
  });

  it("leaves the caller's list untouched", () => {
    const resources = [localDir("a", 0, "d1"), localDir("b", 1, "d1")];
    moveLocalDirectory(resources, "b", "d1", "up");
    expect(resources.map((r) => r.id)).toEqual(["a", "b"]);
    expect(resources.map((r) => r.position)).toEqual([0, 1]);
  });
});

describe("canMoveLocalDirectory / localDirectoryIndexes", () => {
  it("reports the ends of this machine's group as unmovable", () => {
    const resources = [localDir("a", 0, "d1"), repo("r", 1), localDir("b", 2, "d1")];

    expect(localDirectoryIndexes(resources, "d1")).toEqual([0, 2]);
    expect(localDirectoryIndexes(resources, null)).toEqual([]);
    expect(canMoveLocalDirectory(resources, "a", "d1", "up")).toBe(false);
    expect(canMoveLocalDirectory(resources, "a", "d1", "down")).toBe(true);
    expect(canMoveLocalDirectory(resources, "b", "d1", "up")).toBe(true);
    expect(canMoveLocalDirectory(resources, "b", "d1", "down")).toBe(false);
  });
});
