// @vitest-environment node

import { describe, expect, it } from "vitest";
import {
  coerceLocalDirectoryMode,
  executionModeOf,
  sharedModeUnavailable,
  worktreeUnavailableReason,
} from "./local-directory-mode";

describe("executionModeOf", () => {
  it("recognises the three known modes", () => {
    expect(executionModeOf({ execution_mode: "in_place" })).toBe("in_place");
    expect(executionModeOf({ execution_mode: "worktree" })).toBe("worktree");
    expect(executionModeOf({ execution_mode: "shared" })).toBe("shared");
  });

  // Absent (pre-mode resources) and anything a newer server might send both
  // mean "assume the working copy is at stake" — claiming isolation or a
  // lock-free share we cannot verify is the one wrong answer here.
  it("treats an absent or unknown mode as in_place", () => {
    expect(executionModeOf({})).toBe("in_place");
    expect(executionModeOf({ execution_mode: null })).toBe("in_place");
    expect(executionModeOf({ execution_mode: "snapshot" })).toBe("in_place");
    expect(executionModeOf({ execution_mode: "SHARED" })).toBe("in_place");
  });
});

describe("worktreeUnavailableReason", () => {
  it("blocks a confirmed non-git folder even when the server validates", () => {
    expect(worktreeUnavailableReason(false, true)).toBe("not_git");
  });

  it("blocks when the server would silently drop the field", () => {
    expect(worktreeUnavailableReason(true, false)).toBe("server_outdated");
    expect(worktreeUnavailableReason(undefined, false)).toBe("server_outdated");
  });

  it("stays permissive when git-ness is unknown and the server validates", () => {
    expect(worktreeUnavailableReason(undefined, true)).toBeUndefined();
    expect(worktreeUnavailableReason(true, true)).toBeUndefined();
  });
});

describe("sharedModeUnavailable", () => {
  it("is only the server-outdated gate — a non-git folder is the typical case", () => {
    expect(sharedModeUnavailable(true)).toBe(false);
    expect(sharedModeUnavailable(false)).toBe(true);
  });
});

describe("coerceLocalDirectoryMode", () => {
  it("keeps a selected mode when nothing blocks it", () => {
    expect(coerceLocalDirectoryMode("shared")).toBe("shared");
    expect(coerceLocalDirectoryMode("worktree")).toBe("worktree");
    expect(coerceLocalDirectoryMode("in_place")).toBe("in_place");
  });

  it("falls worktree back to in_place when the option is blocked", () => {
    expect(
      coerceLocalDirectoryMode("worktree", { worktreeUnavailable: "not_git" }),
    ).toBe("in_place");
  });

  // The whole point of shared: an umbrella directory is usually not itself a
  // git repo. Blocking worktree must not also wipe a shared choice.
  it("keeps shared selected on a non-git folder", () => {
    expect(
      coerceLocalDirectoryMode("shared", { worktreeUnavailable: "not_git" }),
    ).toBe("shared");
  });

  it("falls shared back to in_place when the server would drop the field", () => {
    expect(
      coerceLocalDirectoryMode("shared", { sharedUnavailable: true }),
    ).toBe("in_place");
  });

  it("maps an unrecognised mode to in_place", () => {
    expect(
      coerceLocalDirectoryMode("snapshot" as "in_place"),
    ).toBe("in_place");
  });
});
