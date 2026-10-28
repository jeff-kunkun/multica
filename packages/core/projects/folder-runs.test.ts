// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { AgentTask, ProjectResource } from "../types";
import {
  issueSharedFolders,
  normalizeFolderPath,
  runFolder,
  runsInFolder,
  runsInProjectFolder,
} from "./folder-runs";

const daemons: Record<string, string> = { "rt-claude": "mac", "rt-codex": "mac", "rt-other": "linux" };
const daemonOf = (runtimeId: string) => daemons[runtimeId];

function task(id: string, patch: Partial<AgentTask>): AgentTask {
  return {
    id,
    agent_id: `agent-${id}`,
    runtime_id: "rt-claude",
    issue_id: `issue-${id}`,
    status: "running",
    created_at: `2026-10-27T10:00:0${id.length}Z`,
    ...patch,
  } as AgentTask;
}

function folder(id: string, local_path: string, daemon_id = "mac", real_path?: string): ProjectResource {
  return {
    id,
    project_id: "p",
    workspace_id: "w",
    resource_type: "local_directory",
    resource_ref: { local_path, daemon_id, ...(real_path ? { real_path } : {}) },
    label: null,
    position: 0,
    created_at: "2026-10-27T00:00:00Z",
    created_by: null,
  } as ProjectResource;
}

describe("normalizeFolderPath", () => {
  it("ignores trailing slashes and separators", () => {
    expect(normalizeFolderPath("/work/app/")).toBe("/work/app");
    expect(normalizeFolderPath("C:\\work\\app\\")).toBe("C:/work/app");
    expect(normalizeFolderPath("/")).toBe("/");
    expect(normalizeFolderPath("  ")).toBe("");
  });
});

describe("runFolder", () => {
  it("prefers the reported work_dir, then a local decision's path", () => {
    expect(runFolder(task("a", { work_dir: "/work/app/" }), daemonOf)).toEqual({ machine: "mac", path: "/work/app" });
    expect(
      runFolder(task("b", { code_decision: { kind: "local_in_place", path: "/work/app" } }), daemonOf),
    ).toEqual({ machine: "mac", path: "/work/app" });
  });

  it("has no folder for queued or finished runs, or runs with nowhere yet", () => {
    expect(runFolder(task("a", { status: "queued", work_dir: "/work/app" }), daemonOf)).toBeNull();
    expect(runFolder(task("b", { status: "completed", work_dir: "/work/app" }), daemonOf)).toBeNull();
    expect(runFolder(task("c", { code_decision: { kind: "remote_cache", url: "https://x/y" } }), daemonOf)).toBeNull();
  });

  it("falls back to the runtime id when the runtime is unknown", () => {
    expect(runFolder(task("a", { runtime_id: "rt-gone", work_dir: "/w" }), daemonOf)).toEqual({
      machine: "rt-gone",
      path: "/w",
    });
  });
});

describe("runsInFolder", () => {
  it("matches the same path on the same machine across runtimes, not across machines", () => {
    const tasks = [
      task("a", { work_dir: "/work/app" }),
      task("bb", { runtime_id: "rt-codex", code_decision: { kind: "local_in_place", path: "/work/app/" } }),
      task("ccc", { runtime_id: "rt-other", work_dir: "/work/app" }),
      task("dddd", { work_dir: "/work/other" }),
    ];
    expect(runsInFolder(tasks, daemonOf, { machine: "mac", path: "/work/app" }).map((t) => t.id)).toEqual([
      "a",
      "bb",
    ]);
  });
});

describe("runsInProjectFolder", () => {
  it("counts runs the decision sent there and runs in the same path on that machine", () => {
    const tasks = [
      task("a", { code_decision: { kind: "local_in_place", resource_id: "res-1", path: "/elsewhere" } }),
      task("bb", { runtime_id: "rt-codex", work_dir: "/real/app" }),
      task("ccc", { runtime_id: "rt-other", work_dir: "/work/app" }),
      task("dddd", { status: "completed", code_decision: { kind: "local_in_place", resource_id: "res-1" } }),
    ];
    expect(
      runsInProjectFolder(tasks, daemonOf, folder("res-1", "/work/app", "mac", "/real/app")).map((t) => t.id),
    ).toEqual(["a", "bb"]);
  });

  it("is empty for anything but a local directory", () => {
    const repo = { ...folder("r", "/x"), resource_type: "github_repo" } as ProjectResource;
    expect(runsInProjectFolder([task("a", { work_dir: "/x" })], daemonOf, repo)).toEqual([]);
  });
});

describe("issueSharedFolders", () => {
  it("names a folder only when another run is in it too", () => {
    const tasks = [
      task("a", { issue_id: "mine", work_dir: "/work/app" }),
      task("bb", { issue_id: "theirs", runtime_id: "rt-codex", work_dir: "/work/app" }),
      task("ccc", { issue_id: "mine", work_dir: "/work/solo" }),
    ];
    const groups = issueSharedFolders(tasks, daemonOf, "mine");
    expect(groups).toHaveLength(1);
    expect(groups[0]!.folder).toEqual({ machine: "mac", path: "/work/app" });
    expect(groups[0]!.runs.map((t) => t.id)).toEqual(["a", "bb"]);
  });

  it("is empty when the issue runs alone or not at all", () => {
    const alone = [task("a", { issue_id: "mine", work_dir: "/work/app" })];
    expect(issueSharedFolders(alone, daemonOf, "mine")).toEqual([]);
    expect(issueSharedFolders(alone, daemonOf, "")).toEqual([]);
  });
});
