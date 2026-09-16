// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  DEFAULT_RUNTIME_CONFIG,
  SELF_HOSTED_PRESET_URL,
  deriveWsUrl,
  desktopProfileName,
  isOfficialCloudConfig,
  parseRuntimeConfig,
  runtimeConfigFromDevEnv,
  runtimeConfigFromServerUrl,
  runtimeConfigHost,
} from "./runtime-config";

describe("runtime config", () => {
  it("derives https/wss compatible URLs from apiUrl", () => {
    expect(
      parseRuntimeConfig(
        JSON.stringify({
          schemaVersion: 1,
          apiUrl: "https://congvc-x99.taila6fa8a.ts.net:18443",
        }),
      ),
    ).toEqual({
      schemaVersion: 1,
      apiUrl: "https://congvc-x99.taila6fa8a.ts.net:18443",
      wsUrl: "wss://congvc-x99.taila6fa8a.ts.net:18443/ws",
      appUrl: "https://congvc-x99.taila6fa8a.ts.net:18443",
    });
  });

  it("strips the leading api. label when deriving appUrl", () => {
    expect(
      parseRuntimeConfig(
        JSON.stringify({ schemaVersion: 1, apiUrl: "https://api.multica.ai" }),
      ),
    ).toEqual({
      schemaVersion: 1,
      apiUrl: "https://api.multica.ai",
      wsUrl: "wss://api.multica.ai/ws",
      appUrl: "https://multica.ai",
    });
  });

  it("derives ws for http api URLs", () => {
    expect(deriveWsUrl("http://localhost:8080")).toBe("ws://localhost:8080/ws");
  });

  it("accepts explicit appUrl and wsUrl", () => {
    expect(
      parseRuntimeConfig(
        JSON.stringify({
          schemaVersion: 1,
          apiUrl: "https://api.example.com/",
          wsUrl: "wss://ws.example.com/socket/",
          appUrl: "https://app.example.com/",
        }),
      ),
    ).toEqual({
      schemaVersion: 1,
      apiUrl: "https://api.example.com",
      wsUrl: "wss://ws.example.com/socket",
      appUrl: "https://app.example.com",
    });
  });

  it("rejects invalid JSON", () => {
    expect(() => parseRuntimeConfig("{")).toThrow(/Invalid desktop runtime config JSON/);
  });

  it("rejects unsupported schema versions", () => {
    expect(() =>
      parseRuntimeConfig(JSON.stringify({ schemaVersion: 2, apiUrl: "https://api.example.com" })),
    ).toThrow(/schemaVersion/);
  });

  it("rejects non-http api schemes", () => {
    expect(() =>
      parseRuntimeConfig(JSON.stringify({ schemaVersion: 1, apiUrl: "file:///tmp/multica" })),
    ).toThrow(/apiUrl must use http or https/);
  });

  it("rejects non-ws websocket schemes", () => {
    expect(() =>
      parseRuntimeConfig(
        JSON.stringify({
          schemaVersion: 1,
          apiUrl: "https://api.example.com",
          wsUrl: "https://api.example.com/ws",
        }),
      ),
    ).toThrow(/wsUrl must use ws or wss/);
  });

  it("preserves electron-vite dev env precedence", () => {
    expect(
      runtimeConfigFromDevEnv({
        apiUrl: "http://dev-api.example.test:8080/",
        wsUrl: "ws://dev-api.example.test:8080/ws/",
        appUrl: "http://dev-app.example.test:3000/",
      }),
    ).toEqual({
      schemaVersion: 1,
      apiUrl: "http://dev-api.example.test:8080",
      wsUrl: "ws://dev-api.example.test:8080/ws",
      appUrl: "http://dev-app.example.test:3000",
    });
  });

  it("falls back to local web URL when dev apiUrl is localhost", () => {
    expect(runtimeConfigFromDevEnv({ apiUrl: "http://localhost:8080" })).toEqual({
      schemaVersion: 1,
      apiUrl: "http://localhost:8080",
      wsUrl: "ws://localhost:8080/ws",
      appUrl: "http://localhost:3000",
    });
  });

  it("derives dev appUrl by stripping the leading api. label", () => {
    // When the dev renderer is pointed at a remote backend (e.g. a test
    // environment), copy-link / share URLs must reflect that environment's
    // public web host, not the api host. Multica's convention exposes the
    // api at `api.<web-host>`, so stripping the leading label gives the
    // right web origin without a separate VITE_APP_URL.
    expect(
      runtimeConfigFromDevEnv({ apiUrl: "https://api.test.multica.ai" }),
    ).toEqual({
      schemaVersion: 1,
      apiUrl: "https://api.test.multica.ai",
      wsUrl: "wss://api.test.multica.ai/ws",
      appUrl: "https://test.multica.ai",
    });
  });

  it("maps official cloud aliases to the packaged defaults", () => {
    for (const input of [
      "https://multica.ai",
      "https://api.multica.ai/",
      "https://www.multica.ai/login",
    ]) {
      expect(runtimeConfigFromServerUrl(input)).toEqual(DEFAULT_RUNTIME_CONFIG);
    }
    expect(isOfficialCloudConfig(DEFAULT_RUNTIME_CONFIG)).toBe(true);
  });

  it("derives ws and app URLs from a self-hosted origin", () => {
    expect(runtimeConfigFromServerUrl(SELF_HOSTED_PRESET_URL)).toEqual({
      schemaVersion: 1,
      apiUrl: "https://ai.ferryway.cc",
      wsUrl: "wss://ai.ferryway.cc/ws",
      appUrl: "https://ai.ferryway.cc",
    });
    expect(runtimeConfigFromServerUrl("https://user:secret@ai.ferryway.cc/path?x=1")).toEqual({
      schemaVersion: 1,
      apiUrl: "https://ai.ferryway.cc",
      wsUrl: "wss://ai.ferryway.cc/ws",
      appUrl: "https://ai.ferryway.cc",
    });
  });

  it("rejects empty, non-http, and unparseable server URLs", () => {
    expect(() => runtimeConfigFromServerUrl("")).toThrow(/required/);
    expect(() => runtimeConfigFromServerUrl("   ")).toThrow(/required/);
    expect(() => runtimeConfigFromServerUrl("not a url")).toThrow(/valid http or https URL/);
    expect(() => runtimeConfigFromServerUrl("ftp://ai.ferryway.cc")).toThrow(/http or https/);
    expect(() => runtimeConfigFromServerUrl("file:///tmp/multica")).toThrow(/http or https/);
  });

  it("names the desktop profile after the API host", () => {
    expect(desktopProfileName("https://api.multica.ai")).toBe("desktop-api.multica.ai");
    expect(desktopProfileName("https://ai.ferryway.cc")).toBe("desktop-ai.ferryway.cc");
    expect(desktopProfileName("http://localhost:8080")).toBe("desktop-localhost-8080");
    expect(runtimeConfigHost(DEFAULT_RUNTIME_CONFIG)).toBe("multica.ai");
  });

  it("dev VITE_APP_URL still wins over apiUrl-derived value", () => {
    expect(
      runtimeConfigFromDevEnv({
        apiUrl: "https://api.test.multica.ai",
        appUrl: "https://staging.multica.ai",
      }),
    ).toEqual({
      schemaVersion: 1,
      apiUrl: "https://api.test.multica.ai",
      wsUrl: "wss://api.test.multica.ai/ws",
      appUrl: "https://staging.multica.ai",
    });
  });
});
