// @vitest-environment node
import { describe, expect, it } from "vitest";
import { docsLocalePrefix } from "./docs-locale";

describe("docsLocalePrefix", () => {
  it.each([
    ["en", ""],
    [undefined, ""],
    ["zh-Hans", "/zh"],
    ["ja", ""],
    ["ko", ""],
    ["fr", ""],
  ])("maps %s to %j", (language, expected) => {
    expect(docsLocalePrefix(language)).toBe(expected);
  });
});
