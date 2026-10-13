import { describe, expect, it } from "vitest";
import { matchLocale, pickLocale } from "./pick-locale";
import type { LocaleAdapter } from "./types";

function makeAdapter(
  overrides: Partial<LocaleAdapter> = {},
): LocaleAdapter {
  return {
    getUserChoice: () => null,
    getSystemPreferences: () => [],
    persist: () => {},
    ...overrides,
  };
}

describe("matchLocale", () => {
  it("returns DEFAULT_LOCALE when given an empty list", () => {
    expect(matchLocale([])).toBe("zh-Hans");
  });

  it("matches a clean supported tag", () => {
    expect(matchLocale(["zh-Hans"])).toBe("zh-Hans");
    expect(matchLocale(["en"])).toBe("en");
  });

  it("collapses region-tagged BCP-47 to the supported base", () => {
    expect(matchLocale(["en-US"])).toBe("en");
    expect(matchLocale(["zh-Hans-CN"])).toBe("zh-Hans");
    expect(matchLocale(["ko-KR"])).toBe("zh-Hans");
    expect(matchLocale(["ja-JP"])).toBe("zh-Hans");
    expect(matchLocale(["fr-FR"])).toBe("zh-Hans");
  });

  it("falls back to DEFAULT_LOCALE when no candidate matches", () => {
    expect(matchLocale(["es", "de"])).toBe("zh-Hans");
  });

  it("zh-Hant (traditional) collapses to zh-Hans — same base subtag, better UX than English fallback", () => {
    expect(matchLocale(["zh-Hant"])).toBe("zh-Hans");
  });

  it("uses the first supported candidate when multiple appear", () => {
    expect(matchLocale(["es", "zh-Hans", "en"])).toBe("zh-Hans");
    expect(matchLocale(["es", "ko-KR", "en"])).toBe("en");
  });

  it("returns DEFAULT_LOCALE for malformed BCP-47 tags rather than throwing", () => {
    expect(matchLocale(["----"])).toBe("zh-Hans");
    expect(matchLocale(["x-private-only"])).toBe("zh-Hans");
  });
});

describe("pickLocale", () => {
  it("prefers explicit user choice over system signal", () => {
    const adapter = makeAdapter({
      getUserChoice: () => "zh-Hans",
      getSystemPreferences: () => ["en-US"],
    });
    expect(pickLocale(adapter)).toBe("zh-Hans");
  });

  it("defaults to Chinese and ignores system preferences when no user choice", () => {
    const adapter = makeAdapter({
      getSystemPreferences: () => ["en-US"],
    });
    expect(pickLocale(adapter)).toBe("zh-Hans");
  });

  it("honours an explicit English choice", () => {
    const adapter = makeAdapter({
      getUserChoice: () => "en",
      getSystemPreferences: () => ["zh-Hans-CN"],
    });
    expect(pickLocale(adapter)).toBe("en");
  });

  it("ignores empty-string user choice and falls back to Chinese", () => {
    const adapter = makeAdapter({
      getUserChoice: () => "",
      getSystemPreferences: () => ["en-US"],
    });
    expect(pickLocale(adapter)).toBe("zh-Hans");
  });
});
