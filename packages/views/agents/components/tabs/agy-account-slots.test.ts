// @vitest-environment node
//
// Canonical suite for the AGY-specific vocabulary (DENE-175, narrowed by
// DENE-309 and again by DENE-678).
//
// What is still AGY's own: the `--gemini_dir` lever, the host-home resolution
// its slot directories derive from, and the login command. The numbered slot
// registry became one entry of the per-CLI family table; its parse/write
// matrix — including the frozen `agy_slots` key — is `account-slots.test.ts`.

import { afterEach, describe, expect, it, vi } from "vitest";
import { accountSlotFamily } from "@multica/core/agents/account-slot-families";
import { slotDirectoryLeaf } from "./account-slots";
import {
  expandHomePrefix,
  formatAgyLoginCommand,
  getGeminiDir,
  inferHomeDirFromGeminiPath,
  isAbsoluteFsPath,
  joinHomeDir,
  resolveHomeDir,
  runtimeHomeDir,
  setGeminiDir,
} from "./agy-account-slots";

const AGY = accountSlotFamily("agy")!;

/** How the accounts model builds a numbered slot's directory on this host. */
function slotDirectory(home: string | null, slot: number): string {
  const leaf = slotDirectoryLeaf(AGY, slot);
  return home ? joinHomeDir(home, leaf) : leaf;
}

describe("agy account slots", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("reads --gemini_dir from a flag pair or an inline value", () => {
    expect(getGeminiDir(["--profile", "research"])).toBe("");
    expect(getGeminiDir(["--gemini_dir", "/Users/you/.gemini-account2"])).toBe(
      "/Users/you/.gemini-account2",
    );
    expect(getGeminiDir(["--gemini_dir=/tmp/.gemini"])).toBe("/tmp/.gemini");
  });

  it("replaces existing gemini_dir tokens and drops them when the path is empty", () => {
    expect(
      setGeminiDir(["--profile", "x", "--gemini_dir", "/old", "--keep"], "/new"),
    ).toEqual(["--profile", "x", "--keep", "--gemini_dir", "/new"]);
    expect(setGeminiDir(["--gemini_dir=/old", "--model", "flash"], "")).toEqual([
      "--model",
      "flash",
    ]);
  });

  it("expands ~ against a home directory and infers home from a Gemini path", () => {
    expect(expandHomePrefix("~/.gemini-account2", "/Users/you")).toBe(
      "/Users/you/.gemini-account2",
    );
    expect(inferHomeDirFromGeminiPath("/Users/you/.gemini")).toBe("/Users/you");
    expect(inferHomeDirFromGeminiPath("/Users/you/.gemini-account2")).toBe(
      "/Users/you",
    );
    expect(inferHomeDirFromGeminiPath("/Users/you/.gemini-account4")).toBe(
      "/Users/you",
    );
    expect(isAbsoluteFsPath("/Users/you/.gemini")).toBe(true);
    expect(isAbsoluteFsPath("~/.gemini")).toBe(false);
    expect(isAbsoluteFsPath("C:\\Users\\you\\.gemini")).toBe(true);
  });

  it("prefers the current profile when inferring home, then process.env.HOME", () => {
    vi.stubEnv("HOME", "/Users/env");
    expect(resolveHomeDir("/Users/you/.gemini")).toBe("/Users/you");
    expect(resolveHomeDir("")).toBe("/Users/env");
  });

  it("expands ~/.gemini into an absolute Account 2 path using process home", () => {
    vi.stubEnv("HOME", "/Users/you");
    vi.stubEnv("USERPROFILE", "");
    const home = resolveHomeDir("~/.gemini");
    expect(home).toBe("/Users/you");
    expect(slotDirectory(home, 2)).toBe("/Users/you/.gemini-account2");
  });

  it("expands ~\\.gemini into an absolute Account 2 path using USERPROFILE", () => {
    vi.stubEnv("HOME", "");
    vi.stubEnv("USERPROFILE", "C:\\Users\\you");
    const home = resolveHomeDir("~\\.gemini");
    expect(home).toBe("C:\\Users\\you");
    expect(slotDirectory(home, 2)).toBe("C:\\Users\\you\\.gemini-account2");
  });

  it("prefers runtime metadata home over process.env when the profile is empty", () => {
    vi.stubEnv("HOME", "/Users/env");
    expect(resolveHomeDir("", "/Users/runtime")).toBe("/Users/runtime");
    expect(runtimeHomeDir({ metadata: { home_dir: "/Users/agy-host" } })).toBe(
      "/Users/agy-host",
    );
  });

  it("resolves home from runtime metadata when HOME and desktopAPI are absent", () => {
    vi.stubEnv("HOME", "");
    vi.stubEnv("USERPROFILE", "");
    delete (globalThis as { desktopAPI?: unknown }).desktopAPI;
    const home = resolveHomeDir("", "/Users/agy-host");
    expect(home).toBe("/Users/agy-host");
    expect(slotDirectory(home, 2)).toBe("/Users/agy-host/.gemini-account2");
  });

  it("returns null when no profile, runtime home, or env is available", () => {
    vi.stubEnv("HOME", "");
    vi.stubEnv("USERPROFILE", "");
    delete (globalThis as { desktopAPI?: unknown }).desktopAPI;
    expect(resolveHomeDir("")).toBeNull();
    expect(runtimeHomeDir({ metadata: { home_dir: "~" } })).toBeNull();
    // With no host home the leaf stays relative, and the switch plan refuses it
    // as an unusable directory instead of binding a guessed path.
    expect(slotDirectory(null, 2)).toBe(".gemini-account2");
  });

  it("builds a one-line agy login command", () => {
    expect(formatAgyLoginCommand("/Users/you/.gemini-account2")).toBe(
      "agy --gemini_dir=/Users/you/.gemini-account2",
    );
    expect(formatAgyLoginCommand("/Users/you/.gemini-account4")).toBe(
      "agy --gemini_dir=/Users/you/.gemini-account4",
    );
  });
});
