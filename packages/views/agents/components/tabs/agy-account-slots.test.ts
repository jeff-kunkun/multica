// @vitest-environment node
//
// Canonical suite for the AGY slot vocabulary (DENE-175, narrowed by DENE-309).
//
// DENE-309 retired the slot block that used to live in the custom-args tab, so
// this file covers what is still on the code path: the `--gemini_dir` lever,
// the host-home resolution the slot directories derive from, and the
// parse/write pair for `runtime_config.agy_slots` — the key the backend reads
// to decide which numbered accounts a quota-exhausted agent may rotate to.
// The `agy_logged_in_dirs` / `agy_quota_exhausted` metadata keys stay on the
// daemon's API surface, but this client reads per-account state from the
// `agent_accounts` report instead, so their parsers are gone.

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  AGY_SLOTS_RUNTIME_KEY,
  accountDirectoryLeaf,
  detectAgyAccountSlot,
  expandHomePrefix,
  formatAgyLoginCommand,
  getGeminiDir,
  inferHomeDirFromGeminiPath,
  isAbsoluteFsPath,
  joinHomeDir,
  nextAccountNumber,
  normalizeAccountNumbers,
  numberedSlotId,
  parseAccountNumber,
  parseAgySlotsConfig,
  resolveHomeDir,
  runtimeHomeDir,
  setGeminiDir,
  writeAgySlotsConfig,
} from "./agy-account-slots";

/** How the accounts model builds a numbered slot's directory on this host. */
function slotDirectory(home: string | null, slot: number): string {
  const leaf = accountDirectoryLeaf(slot);
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

  it("detects the default, isolated, and custom slots including account 4+", () => {
    expect(detectAgyAccountSlot("")).toBe("account1");
    expect(detectAgyAccountSlot("/Users/you/.gemini")).toBe("account1");
    expect(detectAgyAccountSlot("~/.gemini")).toBe("account1");
    expect(detectAgyAccountSlot("/Users/you/.gemini-account2")).toBe("account2");
    expect(detectAgyAccountSlot("~/.gemini-account2")).toBe("account2");
    expect(detectAgyAccountSlot("/Users/you/.gemini-account3")).toBe("account3");
    expect(detectAgyAccountSlot("/Users/you/.gemini-account4")).toBe("account4");
    expect(detectAgyAccountSlot("/Users/you/.gemini-work")).toBe("custom");
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

  it("maps numbered slots onto isolated directories", () => {
    expect(accountDirectoryLeaf(1)).toBe(".gemini");
    expect(accountDirectoryLeaf(2)).toBe(".gemini-account2");
    expect(accountDirectoryLeaf(4)).toBe(".gemini-account4");
    expect(numberedSlotId(4)).toBe("account4");
    expect(parseAccountNumber("account4")).toBe(4);
    expect(parseAccountNumber("custom")).toBeNull();
  });

  it("adds the next unused account number so plus yields account 4", () => {
    expect(nextAccountNumber([1, 2, 3])).toBe(4);
    expect(nextAccountNumber([1, 2, 4])).toBe(3);
    expect(nextAccountNumber([1])).toBe(2);
    expect(normalizeAccountNumbers([4, 1, 4, 0, 99])).toEqual([1, 4]);
  });

  it("persists the numbered slot list under the key the backend reads", () => {
    expect(AGY_SLOTS_RUNTIME_KEY).toBe("agy_slots");
    expect(parseAgySlotsConfig({})).toEqual([1, 2, 3]);
    expect(parseAgySlotsConfig({ agy_slots: { accounts: [1, 4, 5] } })).toEqual([
      1, 4, 5,
    ]);
    expect(parseAgySlotsConfig({}, "/Users/you/.gemini-account4")).toEqual([
      1, 2, 3, 4,
    ]);
    expect(writeAgySlotsConfig({ mode: "local" }, [1, 4])).toEqual({
      mode: "local",
      agy_slots: { accounts: [1, 4] },
    });
    // A slot set that arrives unsorted or out of range is stored normalised, so
    // the backend's own normalisation cannot disagree with what the UI saved.
    expect(writeAgySlotsConfig({}, [4, 4, 0, 40])).toEqual({
      agy_slots: { accounts: [1, 4] },
    });
  });
});
