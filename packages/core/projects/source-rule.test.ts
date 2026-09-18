// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { ProjectResource } from "../types";
import {
  findDuplicateSources,
  findRedundantRemotes,
  normalizeRepoUrl,
  repoNameFromLocalPath,
  repoNameFromUrl,
  resolveTaskCodeSource,
} from "./source-rule";

// Canonical matrix for the source rule. The project picker and the task badge
// both read this module; their component suites cover wiring only.

function remote(id: string, url: string): ProjectResource {
  return {
    id,
    project_id: "p",
    workspace_id: "w",
    resource_type: "github_repo",
    resource_ref: { url },
    label: null,
    position: 0,
    created_at: "2026-09-19T00:00:00Z",
    created_by: null,
  } as ProjectResource;
}

function local(
  id: string,
  local_path: string,
  extra: Record<string, unknown> = {},
): ProjectResource {
  return {
    id,
    project_id: "p",
    workspace_id: "w",
    resource_type: "local_directory",
    resource_ref: { local_path, daemon_id: "d1", ...extra },
    label: null,
    position: 0,
    created_at: "2026-09-19T00:00:00Z",
    created_by: null,
  } as ProjectResource;
}

describe("normalizeRepoUrl", () => {
  it("collapses transport, credentials, port, case and .git", () => {
    const want = "github.com/jeff-kunkun/multica";
    for (const url of [
      "https://github.com/jeff-kunkun/multica",
      "https://github.com/jeff-kunkun/multica.git",
      "http://github.com/jeff-kunkun/multica/",
      "git@github.com:jeff-kunkun/multica.git",
      "ssh://git@github.com:22/jeff-kunkun/multica",
      "https://token:x-oauth-basic@github.com/jeff-kunkun/multica.git",
      "https://GitHub.com/jeff-kunkun/Multica",
    ]) {
      expect(normalizeRepoUrl(url)).toBe(want);
    }
  });

  it("refuses to identify anything that is not a remote repository", () => {
    for (const bad of ["", "   ", "github.com", "https://github.com/", "/Users/kunkun/.agents/multica", "./multica", "C:\\src\\repo"]) {
      expect(normalizeRepoUrl(bad)).toBe("");
    }
  });

  it("does not make two unidentifiable inputs equal", () => {
    expect(normalizeRepoUrl("nonsense")).toBe(normalizeRepoUrl("other"));
    // ...which is why callers must check for "" before comparing. The
    // duplicate finders below are what actually enforce that.
    expect(findRedundantRemotes([remote("a", "nonsense"), remote("b", "other")])).toEqual([]);
  });
});

describe("repo names", () => {
  it("reads a name from a URL and from a path", () => {
    expect(repoNameFromUrl("git@github.com:kun/Online-Tarot.git")).toBe("online-tarot");
    expect(repoNameFromLocalPath("/Users/kunkun/code/Online-Tarot/")).toBe("online-tarot");
  });
});

describe("findDuplicateSources", () => {
  it("flags one repository configured both remotely and locally", () => {
    const resources = [
      remote("r1", "https://github.com/jeff-kunkun/multica"),
      local("l1", "/Users/kunkun/.agents/multica"),
    ];
    const groups = findDuplicateSources(resources);
    expect(groups).toHaveLength(1);
    expect(groups[0]!.repoName).toBe("multica");
    expect(groups[0]!.local.id).toBe("l1");
    expect(groups[0]!.remotes.map((r) => r.id)).toEqual(["r1"]);
  });

  it("groups every remote row that names the same repository", () => {
    // The real workspace had multica configured as github_repo twice plus a
    // local directory; a merge that only removed one row would leave the bug.
    const groups = findDuplicateSources([
      remote("r1", "https://github.com/jeff-kunkun/multica"),
      remote("r2", "git@github.com:jeff-kunkun/multica.git"),
      local("l1", "/Users/kunkun/.agents/multica"),
    ]);
    expect(groups[0]!.remotes.map((r) => r.id)).toEqual(["r1", "r2"]);
  });

  it("leaves unrelated sources alone", () => {
    expect(
      findDuplicateSources([
        remote("r1", "https://github.com/front/game_web_all"),
        local("l1", "/Users/kunkun/.agents/multica"),
      ]),
    ).toEqual([]);
  });

  it("does not flag a directory whose name cannot be read", () => {
    expect(findDuplicateSources([remote("r1", "https://github.com/o/r"), local("l1", "")])).toEqual([]);
  });
});

describe("findRedundantRemotes", () => {
  it("keeps the first row of an identity and reports the rest", () => {
    const rows = [
      remote("r1", "https://github.com/jeff-kunkun/multica"),
      remote("r2", "git@github.com:jeff-kunkun/multica.git"),
      remote("r3", "https://github.com/other/thing"),
    ];
    expect(findRedundantRemotes(rows).map((r) => r.id)).toEqual(["r2"]);
  });
});

describe("resolveTaskCodeSource", () => {
  it("uses the local directory pinned for the task's machine", () => {
    const src = resolveTaskCodeSource({
      resources: [
        remote("r1", "https://github.com/jeff-kunkun/multica"),
        local("l1", "/Users/kunkun/.agents/multica", { daemon_id: "d1", execution_mode: "worktree" }),
        local("l2", "/other/machine/multica", { daemon_id: "d2" }),
      ],
      daemonId: "d1",
      hasReportedWorkDir: true,
    });
    expect(src.kind).toBe("local_directory");
    expect(src.resource?.id).toBe("l1");
    expect(src.localPath).toBe("/Users/kunkun/.agents/multica");
    expect(src.executionMode).toBe("worktree");
    expect(src.predicted).toBe(false);
  });

  it("stays remote when no directory is pinned on that machine", () => {
    const src = resolveTaskCodeSource({
      resources: [local("l2", "/other/machine/multica", { daemon_id: "d2" })],
      daemonId: "d1",
      hasReportedWorkDir: true,
    });
    expect(src.kind).toBe("remote_checkout");
    expect(src.resource).toBeNull();
    expect(src.localPath).toBe("");
  });

  it("refuses to name a path when several machines are candidates", () => {
    // Showing the wrong machine's path is worse than showing none: the user
    // would go looking in a directory this task never touches.
    const src = resolveTaskCodeSource({
      resources: [
        local("l1", "/a/multica", { daemon_id: "d1" }),
        local("l2", "/b/multica", { daemon_id: "d2" }),
      ],
      daemonId: null,
    });
    expect(src.kind).toBe("remote_checkout");
    expect(src.localPath).toBe("");
  });

  it("marks a source as predicted until the task reports a work dir", () => {
    const src = resolveTaskCodeSource({
      resources: [local("l1", "/a/multica", { daemon_id: "d1" })],
      daemonId: "d1",
    });
    expect(src.predicted).toBe(true);
    expect(src.localPath).toBe("/a/multica");
  });

  it("defaults an absent execution_mode to in_place rather than claiming isolation", () => {
    const src = resolveTaskCodeSource({
      resources: [local("l1", "/a/multica", { daemon_id: "d1" })],
      daemonId: "d1",
    });
    expect(src.executionMode).toBe("in_place");
  });
});
