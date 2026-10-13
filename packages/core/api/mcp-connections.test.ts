import { describe, expect, it } from "vitest";
import {
  parseMcpConnectionGrant,
  parseMcpConnections,
  parseOfficeBotRoute,
  parseOfficeBotRoutes,
} from "./mcp-connections";

describe("MCP response boundaries", () => {
  it("maps metadata and handles omitted last use", () => {
    expect(
      parseMcpConnections([
        {
          id: "c",
          name: "App",
          bot_id: "b",
          expires_at: "later",
          created_at: "now",
          revoked: false,
        },
      ])[0],
    ).toMatchObject({ botId: "b", lastUsedAt: null, revoked: false });
  });
  it("rejects malformed connection lists instead of showing a false empty state", () => {
    expect(() => parseMcpConnections({ connections: [] })).toThrow();
    expect(() => parseMcpConnections([{ id: "c" }])).toThrow();
  });
  it("rejects missing codes and malformed addresses", () => {
    const grant = {
      id: "c",
      code: "single-use",
      expires_at: "later",
      endpoint: "https://test.example/mcp",
      exchange_url: "https://test.example/exchange",
    };
    expect(parseMcpConnectionGrant(grant).exchangeUrl).toBe(grant.exchange_url);
    expect(() => parseMcpConnectionGrant({ ...grant, code: "" })).toThrow();
    expect(() =>
      parseMcpConnectionGrant({ ...grant, endpoint: "javascript:alert(1)" }),
    ).toThrow();
  });
});

describe("office notification route responses", () => {
  it("maps default and project destinations", () => {
    expect(
      parseOfficeBotRoutes([
        { project_id: null, bot_id: "b", channel_ref: "g" },
      ]),
    ).toEqual([{ projectId: null, botId: "b", channelRef: "g" }]);
    expect(
      parseOfficeBotRoute({ project_id: "p", bot_id: "b", channel_ref: "g" })
        .projectId,
    ).toBe("p");
  });
  it("rejects incomplete destinations", () => {
    expect(() =>
      parseOfficeBotRoute({ project_id: null, bot_id: "b" }),
    ).toThrow();
    expect(() => parseOfficeBotRoutes({ routes: [] })).toThrow();
  });
});
