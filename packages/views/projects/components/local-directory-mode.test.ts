// @vitest-environment node

import { describe, expect, it } from "vitest";
import {
  apiExecutionMode,
  coerceLocalDirectoryMode,
  displayedExecutionMode,
  executionModeOf,
  isSharedModeRejectedByServer,
  needsLocalSharedOverride,
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
  it("stays available on desktop even when the server rejects shared", () => {
    expect(
      sharedModeUnavailable({
        serverAcceptsShared: false,
        canSetLocalOverride: true,
      }),
    ).toBe(false);
  });

  it("blocks only when neither the server nor a local override can honour it", () => {
    expect(
      sharedModeUnavailable({
        serverAcceptsShared: false,
        canSetLocalOverride: false,
      }),
    ).toBe(true);
    expect(
      sharedModeUnavailable({
        serverAcceptsShared: true,
        canSetLocalOverride: false,
      }),
    ).toBe(false);
  });
});

describe("apiExecutionMode", () => {
  it("sends shared only when the server declares it", () => {
    expect(apiExecutionMode("shared", true)).toBe("shared");
    expect(apiExecutionMode("shared", false)).toBe("in_place");
    expect(apiExecutionMode("worktree", false)).toBe("worktree");
    expect(apiExecutionMode("in_place", false)).toBe("in_place");
  });
});

describe("needsLocalSharedOverride", () => {
  it("is the official-cloud shared path: UI says shared, API cannot store it", () => {
    expect(needsLocalSharedOverride("shared", false)).toBe(true);
    expect(needsLocalSharedOverride("shared", true)).toBe(false);
    expect(needsLocalSharedOverride("in_place", false)).toBe(false);
    expect(needsLocalSharedOverride("worktree", false)).toBe(false);
  });
});

describe("displayedExecutionMode", () => {
  it("upgrades stored in_place to shared when a local override exists", () => {
    expect(displayedExecutionMode({ execution_mode: "in_place" }, true)).toBe(
      "shared",
    );
    expect(displayedExecutionMode({}, true)).toBe("shared");
  });

  it("does not hide a stored worktree behind a stale override", () => {
    expect(displayedExecutionMode({ execution_mode: "worktree" }, true)).toBe(
      "worktree",
    );
  });

  it("keeps a server-persisted shared mode", () => {
    expect(displayedExecutionMode({ execution_mode: "shared" }, false)).toBe(
      "shared",
    );
  });
});

describe("isSharedModeRejectedByServer", () => {
  it("recognises the official-cloud enum rejection", () => {
    expect(
      isSharedModeRejectedByServer(
        'local_directory: execution_mode must be "in_place" or "worktree", got "shared"',
      ),
    ).toBe(true);
  });

  it("ignores unrelated failures", () => {
    expect(isSharedModeRejectedByServer("daemon is offline")).toBe(false);
    expect(isSharedModeRejectedByServer("")).toBe(false);
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
