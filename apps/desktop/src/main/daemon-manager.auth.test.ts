// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { BrowserWindow } from "electron";

type Handler = (...args: unknown[]) => unknown;

const fixture = vi.hoisted(() => ({
  handlers: new Map<string, Handler>(),
  files: new Map<string, string>(),
  sent: vi.fn(),
  exec: vi.fn(),
  healthy: false,
}));
vi.mock("electron", () => ({
  app: { getAppPath: () => "/test/app.asar", on: vi.fn() },
  ipcMain: { handle: (name: string, handler: Handler) => fixture.handlers.set(name, handler), on: vi.fn() },
  BrowserWindow: {}, shell: {},
}));
vi.mock("child_process", () => ({ execFile: fixture.exec }));
vi.mock("fs", () => ({ existsSync: () => true, watchFile: vi.fn(), unwatchFile: vi.fn() }));
vi.mock("fs/promises", () => ({
  readFile: async (path: string) => {
    const value = fixture.files.get(path);
    if (value === undefined) throw Object.assign(new Error("missing"), { code: "ENOENT" });
    return value;
  },
  writeFile: async (path: string, value: string) => { fixture.files.set(path, value); },
  mkdir: vi.fn(), rm: vi.fn(), open: vi.fn(), stat: vi.fn(),
}));
vi.mock("./cli-bootstrap", () => ({ ensureManagedCli: vi.fn(), managedCliPath: () => "/test/multica" }));
vi.mock("./updater", () => ({ isQuittingToInstallUpdate: () => false }));

import { profileConfigPath, profileUserIdPath } from "./daemon-profile";
const profile = "desktop-api.example.com";
const configPath = profileConfigPath(profile);
const invoke = (name: string, ...args: unknown[]) => fixture.handlers.get(name)!(null, ...args);

async function setup(status: number | "network") {
  const fetchMock = vi.fn(async (url: string, options?: RequestInit) => {
    if (url.startsWith("http://127.0.0.1:")) {
      if (fixture.healthy) return Response.json({ status: "running", os: "darwin" });
      throw new Error("connection refused");
    }
    if (url.endsWith("/api/me")) {
      if (status === "network") throw new Error("offline");
      return new Response(null, { status });
    }
    if (url.endsWith("/api/tokens") && options?.method === "POST") {
      return Response.json({ token: "mul_new" });
    }
    throw new Error(`Unexpected request: ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  const { setupDaemonManager } = await import("./daemon-manager");
  setupDaemonManager(() => ({ webContents: { send: fixture.sent } }) as unknown as BrowserWindow);
  // Bootstrap and the one-shot IPC both use the lifecycle gate.
  await invoke("daemon:retry-install");
  await invoke("daemon:set-target-api-url", "https://api.example.com");
  return fetchMock;
}

beforeEach(() => {
  vi.resetModules();
  vi.useFakeTimers();
  fixture.handlers.clear(); fixture.files.clear(); fixture.sent.mockClear();
  fixture.exec.mockReset();
  fixture.healthy = false;
  fixture.files.set(configPath, JSON.stringify({ server_url: "https://api.example.com", token: "mul_revoked" }));
  fixture.files.set(profileUserIdPath(profile), "user-1");
  fixture.exec.mockImplementation((_bin, args, _options, callback) => {
    if (args[0] === "version") callback(null, JSON.stringify({ version: "1.0.0" }));
    else if (args[1] === "stop") { fixture.healthy = false; callback(null); }
    else callback(new Error("daemon exited before becoming ready"));
  });
});
afterEach(() => { vi.clearAllTimers(); vi.useRealTimers(); vi.unstubAllGlobals(); });

describe("daemon authentication recovery through IPC", () => {
  it("shows reauthentication after a startup exits before the 10-second probe", async () => {
    await setup(401);
    const result = await invoke("daemon:start") as { success: boolean };
    expect(result.success).toBe(false);
    expect(await invoke("daemon:get-status")).toMatchObject({ state: "auth_expired", profile });
    expect(fixture.sent).toHaveBeenCalledWith("daemon:status", expect.objectContaining({ state: "auth_expired" }));
  });

  it.each([200, 403, 429, 503, "network"] as const)("keeps non-auth start failure %s distinct from expired login", async (status) => {
    await setup(status);
    await invoke("daemon:start");
    expect(await invoke("daemon:get-status")).toMatchObject({ state: "stopped" });
  });

  it("replaces a rejected cached PAT using the signed-in session on login", async () => {
    const fetchMock = await setup(401);
    await invoke("daemon:sync-token", "session-jwt", "user-1");
    expect(JSON.parse(fixture.files.get(configPath)!).token).toBe("mul_new");
    expect(fetchMock).toHaveBeenCalledWith("https://api.example.com/api/tokens", expect.objectContaining({ method: "POST" }));
  });

  it.each([200, 403, 429, 503, "network"] as const)("does not rotate the cached PAT on status %s", async (status) => {
    const fetchMock = await setup(status);
    await invoke("daemon:sync-token", "session-jwt", "user-1");
    expect(JSON.parse(fixture.files.get(configPath)!).token).toBe("mul_revoked");
    expect(fetchMock.mock.calls.some(([url]) => url.endsWith("/api/tokens"))).toBe(false);
  });

  it("restarts a running daemon after replacing the same user's rejected PAT", async () => {
    await setup(401);
    fixture.healthy = true;
    await invoke("daemon:sync-token", "session-jwt", "user-1");
    const commands = fixture.exec.mock.calls.map(([, args]) => args.slice(0, 2));
    expect(commands).toContainEqual(["daemon", "stop"]);
    expect(commands).toContainEqual(["daemon", "start"]);
  });
});
