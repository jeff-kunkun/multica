// @vitest-environment node

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  detectAgyAccountSlot,
  expandHomePrefix,
  formatAgyLoginCommand,
  getGeminiDir,
  inferHomeDirFromGeminiPath,
  isAbsoluteFsPath,
  loginDirectory,
  resolveHomeDir,
  resolveSlotDirectory,
  runtimeHomeDir,
  setGeminiDir,
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

  it("detects the default, isolated, and custom slots", () => {
    expect(detectAgyAccountSlot("")).toBe("account1");
    expect(detectAgyAccountSlot("/Users/you/.gemini")).toBe("account1");
    expect(detectAgyAccountSlot("~/.gemini")).toBe("account1");
    expect(detectAgyAccountSlot("/Users/you/.gemini-account2")).toBe("account2");
    expect(detectAgyAccountSlot("~/.gemini-account2")).toBe("account2");
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
    expect(isAbsoluteFsPath("/Users/you/.gemini")).toBe(true);
    expect(isAbsoluteFsPath("~/.gemini")).toBe(false);
    expect(isAbsoluteFsPath("C:\\Users\\you\\.gemini")).toBe(true);
  });

  it("fills the isolated account-2 path and leaves account 1 as the default flag", () => {
    expect(resolveSlotDirectory("account1", "/ignored", "/Users/you")).toBe("");
    expect(resolveSlotDirectory("account2", "", "/Users/you")).toBe(
      "/Users/you/.gemini-account2",
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
  });
});
