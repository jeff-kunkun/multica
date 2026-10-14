import { describe, it, expect } from "vitest";
import {
  chatProjectContextSupported,
  MIN_CHAT_PROJECT_CONTEXT_CLI_VERSION,
} from "./cli-version";

describe("chatProjectContextSupported", () => {
  it("supports a tagged release at or above the minimum", () => {
    expect(chatProjectContextSupported(MIN_CHAT_PROJECT_CONTEXT_CLI_VERSION)).toBe(true);
    expect(chatProjectContextSupported("v0.4.10")).toBe(true);
    expect(chatProjectContextSupported("0.5.0")).toBe(true);
  });

  it("does not support a tagged release below the minimum", () => {
    expect(chatProjectContextSupported("0.4.9")).toBe(false);
    expect(chatProjectContextSupported("0.3.28")).toBe(false);
  });

  it("fails closed on empty or unparsable input", () => {
    expect(chatProjectContextSupported("")).toBe(false);
    expect(chatProjectContextSupported(undefined)).toBe(false);
    expect(chatProjectContextSupported(null)).toBe(false);
    expect(chatProjectContextSupported("garbage")).toBe(false);
  });

  it("treats git-describe dev builds as supported regardless of base tag", () => {
    expect(chatProjectContextSupported("v0.4.8-37-g5d0275d68")).toBe(true);
    expect(chatProjectContextSupported("v0.1.0-235-gdaf0e935-dirty")).toBe(true);
  });
});
