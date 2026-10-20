import { describe, expect, it } from "vitest";
import { isPlainFolder } from "./code-decision-view";

describe("isPlainFolder", () => {
  it("is only a folder the machine measured as having no git root", () => {
    expect(isPlainFolder(false, undefined)).toBe(true);
    expect(isPlainFolder(false, "")).toBe(true);
    expect(isPlainFolder(true, undefined)).toBe(false);
    expect(isPlainFolder(undefined, undefined)).toBe(false);
    expect(isPlainFolder(false, "/Users/me/repo")).toBe(false);
  });
});
