import { describe, it, expect } from "vitest";
import {
  chatProjectContextSupported,
  checkQuickCreateCliVersion,
  checkQuickCreateFieldsCliVersion,
  MIN_CHAT_PROJECT_CONTEXT_CLI_VERSION,
} from "./cli-version";

describe("checkQuickCreateCliVersion", () => {
  it("returns ok for a tagged release at or above the minimum", () => {
    expect(checkQuickCreateCliVersion("v0.2.21").state).toBe("ok");
    expect(checkQuickCreateCliVersion("0.3.1").state).toBe("ok");
  });

  it("returns too_old for a tagged release below the minimum", () => {
    expect(checkQuickCreateCliVersion("v0.2.20").state).toBe("too_old");
    expect(checkQuickCreateCliVersion("v0.2.15").state).toBe("too_old");
  });

  it("returns missing for empty or unparsable input", () => {
    expect(checkQuickCreateCliVersion("").state).toBe("missing");
    expect(checkQuickCreateCliVersion(undefined).state).toBe("missing");
    expect(checkQuickCreateCliVersion("not-a-version").state).toBe("missing");
  });

  it("treats git-describe dev builds as ok regardless of base tag", () => {
    expect(checkQuickCreateCliVersion("v0.2.15-235-gdaf0e935").state).toBe("ok");
    expect(checkQuickCreateCliVersion("v0.2.15-235-gdaf0e935-dirty").state).toBe("ok");
    expect(checkQuickCreateCliVersion("0.1.0-1-gabc1234").state).toBe("ok");
  });
});

describe("checkQuickCreateFieldsCliVersion", () => {
  it("requires the first daemon release that transports explicit fields", () => {
    expect(checkQuickCreateFieldsCliVersion("0.4.2").state).toBe("too_old");
    expect(checkQuickCreateFieldsCliVersion("0.4.3").state).toBe("ok");
    expect(checkQuickCreateFieldsCliVersion("v0.4.3-1-gabc1234").state).toBe("ok");
  });
});

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
