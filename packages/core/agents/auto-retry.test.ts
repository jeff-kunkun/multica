// @vitest-environment node
import { describe, expect, it } from "vitest";
import { isAgentAutoRetryEnabled } from "./auto-retry";

describe("isAgentAutoRetryEnabled", () => {
  it("treats a missing field as on", () => {
    expect(isAgentAutoRetryEnabled({})).toBe(true);
    expect(isAgentAutoRetryEnabled({ auto_retry_enabled: undefined })).toBe(true);
  });

  it("treats explicit true as on and explicit false as off", () => {
    expect(isAgentAutoRetryEnabled({ auto_retry_enabled: true })).toBe(true);
    expect(isAgentAutoRetryEnabled({ auto_retry_enabled: false })).toBe(false);
  });
});
