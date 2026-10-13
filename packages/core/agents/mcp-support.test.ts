import { describe, expect, it } from "vitest";

import { providerSupportsMcpConfig } from "./mcp-support";

describe("providerSupportsMcpConfig", () => {
  it("accepts a provider whose runtime consumes mcp_config", () => {
    expect(providerSupportsMcpConfig("claude")).toBe(true);
    expect(providerSupportsMcpConfig("codex")).toBe(true);
    expect(providerSupportsMcpConfig("opencode")).toBe(true);
  });
  it("rejects providers whose runtime ignores mcp_config", () => {
    expect(providerSupportsMcpConfig("antigravity")).toBe(false);
    // Providers removed from this line are unknown, so they are rejected.
    expect(providerSupportsMcpConfig("copilot")).toBe(false);
    expect(providerSupportsMcpConfig("pi")).toBe(false);
    expect(providerSupportsMcpConfig(undefined)).toBe(false);
    expect(providerSupportsMcpConfig(null)).toBe(false);
  });
});
