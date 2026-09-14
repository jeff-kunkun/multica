// @vitest-environment node

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  ACCOUNT3_DIR,
  accountDirectoryLeaf,
  accountSlotDirectory,
  agyCredentialPaths,
  detectAgyAccountSlot,
  directoryHasAgyCredentials,
  expandHomePrefix,
  formatAgyLoginCommand,
  getGeminiDir,
  inferHomeDirFromGeminiPath,
  isAbsoluteFsPath,
  isIsolatedAccountSlot,
  loginDirectory,
  nextAccountNumber,
  normalizeAccountNumbers,
  parseAgySlotsConfig,
  resolveHomeDir,
  resolveSlotDirectory,
  runtimeHomeDir,
  runtimeLoggedInDirs,
  setGeminiDir,
  slotIsSignedIn,
  writeAgySlotsConfig,
} from "./agy-account-slots";

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
    expect(detectAgyAccountSlot("~/.gemini-account3")).toBe("account3");
    expect(detectAgyAccountSlot("/Users/you/.gemini-account4")).toBe("account4");
    expect(detectAgyAccountSlot("~/.gemini-account4")).toBe("account4");
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
    expect(inferHomeDirFromGeminiPath("/Users/you/.gemini-account3")).toBe(
      "/Users/you",
    );
    expect(inferHomeDirFromGeminiPath("/Users/you/.gemini-account4")).toBe(
      "/Users/you",
    );
    expect(isAbsoluteFsPath("/Users/you/.gemini")).toBe(true);
    expect(isAbsoluteFsPath("~/.gemini")).toBe(false);
    expect(isAbsoluteFsPath("C:\\Users\\you\\.gemini")).toBe(true);
  });

  it("fills the isolated account-2 path and leaves account 1 as the default flag", () => {
    expect(resolveSlotDirectory("account1", "/ignored", "/Users/you")).toBe("");
    expect(resolveSlotDirectory("account2", "", "/Users/you")).toBe(
      "/Users/you/.gemini-account2",
    );
    expect(resolveSlotDirectory("account3", "", "/Users/you")).toBe(
      "/Users/you/.gemini-account3",
    );
    expect(resolveSlotDirectory("account4", "", "/Users/you")).toBe(
      "/Users/you/.gemini-account4",
    );
    expect(
      resolveSlotDirectory("custom", "~/.gemini-work", "/Users/you"),
    ).toBe("/Users/you/.gemini-work");
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
    expect(resolveSlotDirectory("account2", "~/.gemini", home)).toBe(
      "/Users/you/.gemini-account2",
    );
  });

  it("expands ~\\.gemini into an absolute Account 2 path using USERPROFILE", () => {
    vi.stubEnv("HOME", "");
    vi.stubEnv("USERPROFILE", "C:\\Users\\you");
    const home = resolveHomeDir("~\\.gemini");
    expect(home).toBe("C:\\Users\\you");
    expect(resolveSlotDirectory("account2", "~\\.gemini", home)).toBe(
      "C:\\Users\\you\\.gemini-account2",
    );
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
    expect(resolveHomeDir("", "/Users/agy-host")).toBe("/Users/agy-host");
    expect(
      resolveSlotDirectory("account2", "", resolveHomeDir("", "/Users/agy-host")),
    ).toBe("/Users/agy-host/.gemini-account2");
  });

  it("returns null when no profile, runtime home, or env is available", () => {
    vi.stubEnv("HOME", "");
    vi.stubEnv("USERPROFILE", "");
    delete (globalThis as { desktopAPI?: unknown }).desktopAPI;
    expect(resolveHomeDir("")).toBeNull();
    expect(runtimeHomeDir({ metadata: { home_dir: "~" } })).toBeNull();
    expect(resolveSlotDirectory("account2", "", null)).toBe("~/.gemini-account2");
  });

  it("builds a one-line agy login command", () => {
    expect(formatAgyLoginCommand("/Users/you/.gemini-account2")).toBe(
      "agy --gemini_dir=/Users/you/.gemini-account2",
    );
    expect(loginDirectory("account1", "", "/Users/you")).toBe(
      "/Users/you/.gemini",
    );
    expect(loginDirectory("account2", "", "/Users/you")).toBe(
      "/Users/you/.gemini-account2",
    );
    expect(loginDirectory("account3", "", "/Users/you")).toBe(
      "/Users/you/.gemini-account3",
    );
    expect(loginDirectory("account4", "", "/Users/you")).toBe(
      "/Users/you/.gemini-account4",
    );
    expect(formatAgyLoginCommand("/Users/you/.gemini-account4")).toBe(
      "agy --gemini_dir=/Users/you/.gemini-account4",
    );
  });

  it("maps numbered slots onto isolated directories", () => {
    expect(accountSlotDirectory("account1")).toBe(".gemini");
    expect(accountSlotDirectory("account2")).toBe(".gemini-account2");
    expect(accountSlotDirectory("account3")).toBe(ACCOUNT3_DIR);
    expect(accountSlotDirectory("account4")).toBe(".gemini-account4");
    expect(accountDirectoryLeaf(1)).toBe(".gemini");
    expect(accountDirectoryLeaf(4)).toBe(".gemini-account4");
    expect(isIsolatedAccountSlot("account1")).toBe(false);
    expect(isIsolatedAccountSlot("account2")).toBe(true);
    expect(isIsolatedAccountSlot("account3")).toBe(true);
    expect(isIsolatedAccountSlot("account4")).toBe(true);
    expect(isIsolatedAccountSlot("custom")).toBe(false);
    expect(resolveSlotDirectory("account4", "", null)).toBe("~/.gemini-account4");
  });

  it("adds the next unused account number so plus yields account 4", () => {
    expect(nextAccountNumber([1, 2, 3])).toBe(4);
    expect(nextAccountNumber([1, 2, 4])).toBe(3);
    expect(nextAccountNumber([1])).toBe(2);
    expect(normalizeAccountNumbers([4, 1, 4, 0, 99])).toEqual([1, 4]);
  });

  it("persists the numbered slot list in runtime_config.agy_slots", () => {
    expect(parseAgySlotsConfig({})).toEqual([1, 2, 3]);
    expect(
      parseAgySlotsConfig({ agy_slots: { accounts: [1, 4, 5] } }),
    ).toEqual([1, 4, 5]);
    expect(
      parseAgySlotsConfig({}, "/Users/you/.gemini-account4"),
    ).toEqual([1, 2, 3, 4]);
    expect(
      writeAgySlotsConfig({ mode: "local" }, [1, 4]),
    ).toEqual({
      mode: "local",
      agy_slots: { accounts: [1, 4] },
    });
  });

  it("treats oauth_creds.json as a signed-in credential and ignores missing files", () => {
    const dir = "/Users/you/.gemini-account4";
    expect(agyCredentialPaths(dir)).toEqual([
      "/Users/you/.gemini-account4/oauth_creds.json",
      "/Users/you/.gemini-account4/antigravity-cli/antigravity-oauth-token",
    ]);
    expect(
      directoryHasAgyCredentials(dir, (path) => path.endsWith("oauth_creds.json")),
    ).toBe(true);
    expect(directoryHasAgyCredentials(dir, () => false)).toBe(false);
    expect(
      slotIsSignedIn(dir, ["/Users/you/.gemini-account4"]),
    ).toBe(true);
    expect(slotIsSignedIn(dir, ["/Users/you/.gemini"])).toBe(false);
    expect(
      runtimeLoggedInDirs({
        metadata: { agy_logged_in_dirs: ["/Users/you/.gemini", "relative"] },
      }),
    ).toEqual(["/Users/you/.gemini"]);
  });
});
