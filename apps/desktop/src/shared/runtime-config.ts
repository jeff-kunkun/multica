export interface RuntimeConfig {
  schemaVersion: 1;
  apiUrl: string;
  wsUrl: string;
  appUrl: string;
}

export interface RuntimeConfigError {
  message: string;
}

export type RuntimeConfigResult =
  | { ok: true; config: RuntimeConfig }
  | { ok: false; error: RuntimeConfigError };

export type RuntimeConfigSwitchResult =
  | { ok: true; config: RuntimeConfig }
  | { ok: false; error: string };

export const DEFAULT_RUNTIME_CONFIG: RuntimeConfig = Object.freeze({
  schemaVersion: 1,
  apiUrl: "https://api.multica.ai",
  wsUrl: "wss://api.multica.ai/ws",
  appUrl: "https://multica.ai",
});

/** Built-in self-hosted preset shown in the desktop server switcher. */
export const SELF_HOSTED_PRESET_URL = "https://ai.ferryway.cc";

/**
 * Server this fork's desktop build connects to on entry, used whenever
 * `~/.multica/desktop.json` is absent. Upstream falls back to official cloud;
 * this fork's product line is self-hosted first (KUN-FORK.md), so a fresh
 * install lands on the self-hosted instance and the settings switcher is only
 * needed to move to official cloud or another instance.
 *
 * Official cloud stays reachable: switching to it writes an explicit
 * `desktop.json` rather than deleting the file, so "no file" means exactly one
 * thing.
 */
export const DEFAULT_ENTRY_RUNTIME_CONFIG: RuntimeConfig = Object.freeze({
  schemaVersion: 1,
  apiUrl: SELF_HOSTED_PRESET_URL,
  wsUrl: deriveWsUrl(SELF_HOSTED_PRESET_URL),
  appUrl: deriveAppUrl(SELF_HOSTED_PRESET_URL),
});

const OFFICIAL_CLOUD_HOSTS = new Set([
  "api.multica.ai",
  "multica.ai",
  "www.multica.ai",
]);

const LOCAL_DEV_RUNTIME_CONFIG: RuntimeConfig = Object.freeze({
  schemaVersion: 1,
  apiUrl: "http://localhost:8080",
  wsUrl: "ws://localhost:8080/ws",
  appUrl: "http://localhost:3000",
});

export interface RuntimeConfigEnv {
  apiUrl?: string;
  wsUrl?: string;
  appUrl?: string;
}

export function runtimeConfigFromDevEnv(env: RuntimeConfigEnv): RuntimeConfig {
  const apiUrl = normalizeHttpUrl(
    env.apiUrl || LOCAL_DEV_RUNTIME_CONFIG.apiUrl,
    "VITE_API_URL",
  );
  return {
    schemaVersion: 1,
    apiUrl,
    wsUrl: env.wsUrl
      ? normalizeWsUrl(env.wsUrl, "VITE_WS_URL")
      : deriveWsUrl(apiUrl),
    appUrl: env.appUrl
      ? normalizeHttpUrl(env.appUrl, "VITE_APP_URL")
      : deriveDevAppUrl(apiUrl),
  };
}

export function parseRuntimeConfig(raw: string): RuntimeConfig {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch (err) {
    throw new Error(
      `Invalid desktop runtime config JSON: ${err instanceof Error ? err.message : "parse failed"}`,
    );
  }

  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("Invalid desktop runtime config: expected a JSON object");
  }

  const obj = parsed as Record<string, unknown>;
  if (obj.schemaVersion !== 1) {
    throw new Error("Unsupported desktop runtime config schemaVersion: expected 1");
  }

  const apiUrl = requiredString(obj.apiUrl, "apiUrl");
  const appUrl = optionalString(obj.appUrl, "appUrl");
  const wsUrl = optionalString(obj.wsUrl, "wsUrl");

  const normalizedApiUrl = normalizeHttpUrl(apiUrl, "apiUrl");
  return {
    schemaVersion: 1,
    apiUrl: normalizedApiUrl,
    wsUrl: wsUrl ? normalizeWsUrl(wsUrl, "wsUrl") : deriveWsUrl(normalizedApiUrl),
    appUrl: appUrl ? normalizeHttpUrl(appUrl, "appUrl") : deriveAppUrl(normalizedApiUrl),
  };
}

export function deriveWsUrl(apiUrl: string): string {
  const url = new URL(apiUrl);
  if (url.protocol === "https:") url.protocol = "wss:";
  else if (url.protocol === "http:") url.protocol = "ws:";
  else throw new Error("apiUrl must use http or https");
  url.pathname = joinPath(url.pathname, "/ws");
  url.search = "";
  url.hash = "";
  return trimTrailingSlash(url.toString());
}

export function isOfficialCloudConfig(config: RuntimeConfig): boolean {
  return (
    config.apiUrl === DEFAULT_RUNTIME_CONFIG.apiUrl &&
    config.appUrl === DEFAULT_RUNTIME_CONFIG.appUrl
  );
}

/** Desktop CLI profile name for a target API URL (`desktop-<host>`). */
export function desktopProfileName(apiUrl: string): string {
  try {
    const url = new URL(apiUrl);
    const host = url.host.replace(/:/g, "-").toLowerCase();
    return `desktop-${host}`;
  } catch {
    return "desktop";
  }
}

export function runtimeConfigHost(config: RuntimeConfig): string {
  try {
    return new URL(config.appUrl).host;
  } catch {
    try {
      return new URL(config.apiUrl).host;
    } catch {
      return config.appUrl || config.apiUrl;
    }
  }
}

/**
 * Build a runtime config from a user-entered server URL.
 *
 * Official cloud hosts always resolve to `DEFAULT_RUNTIME_CONFIG`. Any other
 * http(s) origin becomes apiUrl; wsUrl/appUrl are derived. Credentials, path,
 * query, and hash are stripped so `desktop.json` never stores secrets.
 */
export function runtimeConfigFromServerUrl(raw: string): RuntimeConfig {
  const trimmed = raw.trim();
  if (!trimmed) {
    throw new Error("Server URL is required");
  }

  let url: URL;
  try {
    url = new URL(trimmed);
  } catch {
    throw new Error("Server URL must be a valid http or https URL");
  }

  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new Error("Server URL must use http or https");
  }

  const host = url.hostname.replace(/\.$/, "").toLowerCase();
  if (OFFICIAL_CLOUD_HOSTS.has(host)) {
    return { ...DEFAULT_RUNTIME_CONFIG };
  }

  const apiUrl = `${url.protocol}//${url.host}`;
  return {
    schemaVersion: 1,
    apiUrl,
    wsUrl: deriveWsUrl(apiUrl),
    appUrl: deriveAppUrl(apiUrl),
  };
}

// Convention: api hosts are exposed at `api.<web-host>` (api.multica.ai →
// multica.ai, api.test.multica.ai → test.multica.ai). Strip the leading
// `api.` label so a single `apiUrl` configuration produces the right
// shareable web URL. Hosts that don't match the convention (no leading
// `api.` label, or short two-label hosts like `api.local`) fall through
// untouched — those deployments must set `appUrl` explicitly.
export function deriveAppUrl(apiUrl: string): string {
  const url = new URL(apiUrl);
  url.pathname = "";
  url.search = "";
  url.hash = "";
  if (url.hostname.startsWith("api.") && url.hostname.split(".").length >= 3) {
    url.hostname = url.hostname.slice("api.".length);
  }
  return trimTrailingSlash(url.toString());
}

// Dev variant: when the api host is the local backend (`localhost:8080` /
// `127.0.0.1:8080`), the renderer is served from a different port (3000),
// so deriving by host alone is wrong. Fall back to the local dev web URL
// in that case; for any non-local host (e.g. a remote test environment),
// trust the production-style derivation so `apiUrl=https://api.test.x`
// yields `appUrl=https://test.x` without a separate VITE_APP_URL.
export function deriveDevAppUrl(apiUrl: string): string {
  const url = new URL(apiUrl);
  if (url.hostname === "localhost" || url.hostname === "127.0.0.1") {
    return LOCAL_DEV_RUNTIME_CONFIG.appUrl;
  }
  return deriveAppUrl(apiUrl);
}

function requiredString(value: unknown, field: string): string {
  if (typeof value !== "string" || value.trim().length === 0) {
    throw new Error(`Invalid desktop runtime config: ${field} must be a non-empty string`);
  }
  return value;
}

function optionalString(value: unknown, field: string): string | undefined {
  if (value === undefined) return undefined;
  if (typeof value !== "string" || value.trim().length === 0) {
    throw new Error(`Invalid desktop runtime config: ${field} must be a non-empty string when set`);
  }
  return value;
}

function normalizeHttpUrl(value: string, field: string): string {
  let url: URL;
  try {
    url = new URL(value.trim());
  } catch {
    throw new Error(`Invalid desktop runtime config: ${field} must be a valid URL`);
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new Error(`Invalid desktop runtime config: ${field} must use http or https`);
  }
  url.search = "";
  url.hash = "";
  return trimTrailingSlash(url.toString());
}

function normalizeWsUrl(value: string, field: string): string {
  let url: URL;
  try {
    url = new URL(value.trim());
  } catch {
    throw new Error(`Invalid desktop runtime config: ${field} must be a valid URL`);
  }
  if (url.protocol !== "ws:" && url.protocol !== "wss:") {
    throw new Error(`Invalid desktop runtime config: ${field} must use ws or wss`);
  }
  url.search = "";
  url.hash = "";
  return trimTrailingSlash(url.toString());
}

function joinPath(base: string, suffix: string): string {
  const normalizedBase = base.endsWith("/") ? base.slice(0, -1) : base;
  return `${normalizedBase}${suffix}`;
}

function trimTrailingSlash(value: string): string {
  return value.replace(/\/+$/, "");
}
