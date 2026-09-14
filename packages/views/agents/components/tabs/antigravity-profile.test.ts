// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  applyAntigravitySlot,
  classifyAntigravitySlot,
  displayAntigravityDirectory,
  expandLeadingTilde,
  formatAntigravityLoginCommand,
  getAntigravityProfile,
  inferHomeDir,
  isAbsolutePath,
  resolveHomeDir,
  resolveSecondaryPath,
  resolveSlotPath,
  setAntigravityProfile,
} from "./antigravity-profile";

function stubNoHome() {
  vi.stubEnv("HOME", "");
  vi.stubEnv("USERPROFILE", "");
}

describe("getAntigravityProfile / setAntigravityProfile", () => {
  it("reads a split --gemini_dir flag and its value", () => {
    expect(getAntigravityProfile(["--profile", "x", "--gemini_dir", "/Users/you/.gemini"])).toBe(
      "/Users/you/.gemini",
    );
  });

  it("reads an inline --gemini_dir= value", () => {
    expect(getAntigravityProfile(["--gemini_dir=/tmp/agy"])).toBe("/tmp/agy");
  });

  it("replaces every previous --gemini_dir spelling with one split flag", () => {
    expect(
      setAntigravityProfile(
        ["--keep", "--gemini_dir", "/old", "--gemini_dir=/also-old"],
        "/Users/you/.gemini-account2",
      ),
    ).toEqual(["--keep", "--gemini_dir", "/Users/you/.gemini-account2"]);
  });

  it("drops --gemini_dir when the profile is empty", () => {
    expect(setAntigravityProfile(["--keep", "--gemini_dir", "/old"], "  ")).toEqual(["--keep"]);
  });
});

describe("classifyAntigravitySlot", () => {
  it("treats an empty path as the default primary account", () => {
    expect(classifyAntigravitySlot("")).toBe("primary");
    expect(classifyAntigravitySlot("   ")).toBe("primary");
  });

  it("matches primary and secondary directory names, including a leading ~", () => {
    expect(classifyAntigravitySlot("/Users/you/.gemini")).toBe("primary");
    expect(classifyAntigravitySlot("~/.gemini")).toBe("primary");
    expect(classifyAntigravitySlot("/Users/you/.gemini-account2")).toBe("secondary");
    expect(classifyAntigravitySlot("C:\\Users\\you\\.gemini-account2")).toBe("secondary");
  });

  it("classifies any other directory as custom", () => {
    expect(classifyAntigravitySlot("/opt/agy-data")).toBe("custom");
  });
});

describe("inferHomeDir / expandLeadingTilde / resolveSecondaryPath", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("infers $HOME from a known Gemini directory", () => {
    expect(inferHomeDir("/Users/you/.gemini")).toBe("/Users/you");
    expect(inferHomeDir("/Users/you/.gemini-account2/")).toBe("/Users/you");
    expect(inferHomeDir("C:\\Users\\you\\.gemini")).toBe("C:\\Users\\you");
    expect(inferHomeDir("~/.gemini")).toBeNull();
    expect(inferHomeDir("~\\.gemini")).toBeNull();
    expect(inferHomeDir("/opt/agy-data")).toBeNull();
  });

  it("expands a leading ~ only when a home directory is known", () => {
    expect(expandLeadingTilde("~/.gemini-account2", "/Users/you")).toBe(
      "/Users/you/.gemini-account2",
    );
    expect(expandLeadingTilde("~\\.gemini-account2", "C:\\Users\\you")).toBe(
      "C:\\Users\\you\\.gemini-account2",
    );
    expect(expandLeadingTilde("~/.gemini-account2", null)).toBe("~/.gemini-account2");
    expect(expandLeadingTilde("/abs/keep", "/Users/you")).toBe("/abs/keep");
  });

  it("fills the isolated secondary directory from the current path's home", () => {
    stubNoHome();
    expect(resolveSecondaryPath("/Users/you/.gemini")).toBe("/Users/you/.gemini-account2");
    expect(resolveSecondaryPath("")).toBe("~/.gemini-account2");
  });

  it("prefers a Gemini path's home over process.env when both exist", () => {
    vi.stubEnv("HOME", "/Users/env");
    expect(resolveHomeDir("/Users/you/.gemini")).toBe("/Users/you");
    expect(resolveHomeDir("")).toBe("/Users/env");
  });

  it("expands ~/.gemini into an absolute Account 2 path using process home", () => {
    vi.stubEnv("HOME", "/Users/you");
    vi.stubEnv("USERPROFILE", "");
    expect(resolveSecondaryPath("~/.gemini")).toBe("/Users/you/.gemini-account2");
    expect(resolveSlotPath("secondary", "~/.gemini")).toBe("/Users/you/.gemini-account2");
    expect(applyAntigravitySlot(["--gemini_dir", "~/.gemini"], "secondary")).toEqual([
      "--gemini_dir",
      "/Users/you/.gemini-account2",
    ]);
    expect(resolveSecondaryPath("")).toBe("/Users/you/.gemini-account2");
  });

  it("expands ~\\.gemini into an absolute Account 2 path using USERPROFILE", () => {
    vi.stubEnv("HOME", "");
    vi.stubEnv("USERPROFILE", "C:\\Users\\you");
    expect(resolveSecondaryPath("~\\.gemini")).toBe("C:\\Users\\you\\.gemini-account2");
    expect(resolveSlotPath("secondary", "~\\.gemini")).toBe(
      "C:\\Users\\you\\.gemini-account2",
    );
    expect(applyAntigravitySlot(["--gemini_dir", "~\\.gemini"], "secondary")).toEqual([
      "--gemini_dir",
      "C:\\Users\\you\\.gemini-account2",
    ]);
  });
});

describe("resolveSlotPath / applyAntigravitySlot", () => {
  it("clears --gemini_dir for the primary slot", () => {
    expect(resolveSlotPath("primary", "/Users/you/.gemini-account2")).toBe("");
    expect(
      applyAntigravitySlot(["--keep", "--gemini_dir", "/Users/you/.gemini-account2"], "primary"),
    ).toEqual(["--keep"]);
  });

  it("fills an absolute isolated directory for the secondary slot", () => {
    expect(resolveSlotPath("secondary", "/Users/you/.gemini")).toBe(
      "/Users/you/.gemini-account2",
    );
    expect(
      applyAntigravitySlot(["--keep", "--gemini_dir", "/Users/you/.gemini"], "secondary"),
    ).toEqual(["--keep", "--gemini_dir", "/Users/you/.gemini-account2"]);
  });

  it("keeps a custom path and expands a leading ~ when home is known", () => {
    expect(resolveSlotPath("custom", "/Users/you/.gemini", "~/.agy-extra")).toBe(
      "/Users/you/.agy-extra",
    );
  });
});

describe("login command and path display", () => {
  it("formats a one-line agy login command", () => {
    expect(formatAntigravityLoginCommand("")).toBe("agy");
    expect(formatAntigravityLoginCommand("/Users/you/.gemini-account2")).toBe(
      "agy --gemini_dir=/Users/you/.gemini-account2",
    );
    expect(formatAntigravityLoginCommand("~/.gemini-account2")).toBe(
      "agy --gemini_dir=$HOME/.gemini-account2",
    );
  });

  it("shows the default primary directory when no flag is set", () => {
    expect(displayAntigravityDirectory("")).toBe("~/.gemini");
    expect(displayAntigravityDirectory("/tmp/agy")).toBe("/tmp/agy");
  });

  it("treats ~ as not absolute", () => {
    expect(isAbsolutePath("/Users/you/.gemini")).toBe(true);
    expect(isAbsolutePath("C:\\Users\\you\\.gemini")).toBe(true);
    expect(isAbsolutePath("~/.gemini")).toBe(false);
    expect(isAbsolutePath("relative")).toBe(false);
  });
});
