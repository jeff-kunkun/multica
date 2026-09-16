// @vitest-environment node
//
// Canonical test layer for the agy directory helpers the accounts surface still
// uses (DENE-309): the numbered pool in `runtime_config.agy_slots`, the
// `--gemini_dir` lever, and the ~ → host-home arithmetic. The per-slot sign-in
// and quota decoration that used to be covered here is gone with the block that
// consumed it — the daemon now reports both on `agent_accounts`.

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  expandHomePrefix,
  formatAgyLoginCommand,
  getGeminiDir,
  isAbsoluteFsPath,
  isGeminiDirToken,
  loginDirectory,
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
    expect(isGeminiDirToken("--gemini_dir")).toBe(true);
    expect(isGeminiDirToken("--gemini_dir=/tmp/.gemini")).toBe(true);
    expect(isGeminiDirToken("--profile")).toBe(false);
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

  it("reads a slot number off account ids and back", () => {
    expect(parseAccountNumber("account1")).toBe(1);
    expect(parseAccountNumber("account4")).toBe(4);
    expect(parseAccountNumber("account33")).toBeNull();
    expect(parseAccountNumber("custom")).toBeNull();
    expect(parseAccountNumber("default")).toBeNull();
    expect(numberedSlotId(4)).toBe("account4");
  });

  it("expands ~ against a home directory", () => {
    expect(expandHomePrefix("~/.gemini-account2", "/Users/you")).toBe(
      "/Users/you/.gemini-account2",
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
    expect(loginDirectory("account2", "~/.gemini", home)).toBe(
      "/Users/you/.gemini-account2",
    );
  });

  it("expands ~\\.gemini into an absolute Account 2 path using USERPROFILE", () => {
    vi.stubEnv("HOME", "");
    vi.stubEnv("USERPROFILE", "C:\\Users\\you");
    const home = resolveHomeDir("~\\.gemini");
    expect(home).toBe("C:\\Users\\you");
    expect(loginDirectory("account2", "~\\.gemini", home)).toBe(
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

  it("returns null when no profile, runtime home, or env is available", () => {
    vi.stubEnv("HOME", "");
    vi.stubEnv("USERPROFILE", "");
    delete (globalThis as { desktopAPI?: unknown }).desktopAPI;
    expect(resolveHomeDir("")).toBeNull();
    expect(runtimeHomeDir({ metadata: { home_dir: "~" } })).toBeNull();
    expect(loginDirectory("account2", "", null)).toBe("~/.gemini-account2");
  });

  it("builds a one-line agy login command per numbered directory", () => {
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

  it("keeps a custom directory as given, and the default when nothing is bound", () => {
    expect(loginDirectory("custom", "/Users/you/.gemini-work", "/Users/you")).toBe(
      "/Users/you/.gemini-work",
    );
    expect(loginDirectory("custom", "~/.gemini-work", "/Users/you")).toBe(
      "/Users/you/.gemini-work",
    );
    // An absolute profile wins over the slot's conventional directory.
    expect(loginDirectory("account2", "/mnt/gemini", "/Users/you")).toBe(
      "/mnt/gemini",
    );
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
});
