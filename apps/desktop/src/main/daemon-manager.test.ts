// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  alive: false,
  autoStart: true,
  token: "test-saved-token",
  exec: vi.fn(),
  send: vi.fn(),
  handlers: new Map<string, (...args: any[]) => any>(),
  appEvents: new Map<string, (...args: any[]) => any>(),
}));
vi.mock("electron", () => ({
  app: {
    getAppPath: () => "/test/Multica.app",
    on: (name: string, fn: (...args: any[]) => any) => state.appEvents.set(name, fn),
    quit: vi.fn(),
  },
  ipcMain: {
    handle: (name: string, fn: (...args: any[]) => any) => state.handlers.set(name, fn),
    on: vi.fn(),
  },
  BrowserWindow: vi.fn(),
  shell: {},
}));
vi.mock("child_process", () => ({ execFile: (...args: unknown[]) => state.exec(...args) }));
vi.mock("fs", () => ({ existsSync: () => true, watchFile: vi.fn(), unwatchFile: vi.fn() }));
vi.mock("fs/promises", () => ({
  readFile: vi.fn(async (path: string) => {
    if (path.endsWith("/desktop_prefs.json")) {
      return JSON.stringify({ autoStart: state.autoStart, autoStop: false });
    }
    if (path.endsWith("config.json")) {
      return JSON.stringify({ server_url: "https://office.example.com", token: state.token });
    }
    throw Object.assign(new Error("missing"), { code: "ENOENT" });
  }),
  writeFile: vi.fn(), mkdir: vi.fn(), rm: vi.fn(), open: vi.fn(), stat: vi.fn(),
}));
vi.mock("./daemon-port", () => ({
  probeProfileHealth: async () => state.alive ? {
    status: "running", daemon_id: "test-daemon", profile: "desktop-office.example.com",
    server_url: "https://office.example.com", cli_version: "test",
  } : null,
}));
vi.mock("./cli-bootstrap", () => ({ ensureManagedCli: vi.fn(), managedCliPath: () => "/test/cli" }));
vi.mock("./updater", () => ({ isQuittingToInstallUpdate: () => false }));

beforeEach(() => {
  vi.resetModules();
  vi.useFakeTimers();
  vi.clearAllMocks();
  state.alive = false;
  state.autoStart = true;
  state.token = "test-saved-token";
  state.handlers.clear();
  state.appEvents.clear();
  state.exec.mockImplementation((_bin, args, _options, callback) => {
    if (args[0] === "version") callback(null, '{"version":"test"}');
    else if (args[1] === "start") {
      state.alive = true;
      callback(null);
    } else callback(null);
  });
});
afterEach(() => {
  vi.clearAllTimers();
  vi.useRealTimers();
});

async function boot() {
  const { setupDaemonManager } = await import("./daemon-manager");
  setupDaemonManager(() => ({ webContents: { send: state.send } }) as never, "https://office.example.com");
  await vi.advanceTimersByTimeAsync(0);
}
function starts() {
  return state.exec.mock.calls.filter((call) => call[1][0] === "daemon" && call[1][1] === "start");
}

describe("Desktop daemon supervision", () => {
  it("restores a saved session before renderer login and recovers an unexpected exit", async () => {
    await boot();
    await vi.advanceTimersByTimeAsync(15_000);
    expect(starts()).toHaveLength(1);
    expect(starts()[0][1]).toEqual(["daemon", "start", "--profile", "desktop-office.example.com"]);
    await vi.advanceTimersByTimeAsync(5_000);
    state.alive = false;
    await vi.advanceTimersByTimeAsync(5_000);
    expect(state.send).toHaveBeenCalledWith("daemon:status", expect.objectContaining({
      state: "stopped", daemonId: "test-daemon",
    }));
    await vi.advanceTimersByTimeAsync(15_000);
    expect(starts()).toHaveLength(2);
  });

  it.each(["disabled", "signed-out"])("does not restore when %s", async (reason) => {
    state.autoStart = reason !== "disabled";
    state.token = reason === "signed-out" ? "" : "test-saved-token";
    await boot();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(starts()).toHaveLength(0);
  });

  it("respects an explicit Stop while the app remains open", async () => {
    await boot();
    await state.handlers.get("daemon:stop")!();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(starts()).toHaveLength(0);
  });

  it("recovers when a successful start command never produces a health listener", async () => {
    state.exec.mockImplementation((_bin, args, _options, callback) =>
      callback(null, args[0] === "version" ? '{"version":"test"}' : undefined));
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ status: 200 }));
    await boot();
    await vi.advanceTimersByTimeAsync(95_000);
    expect(starts()).toHaveLength(2);
    vi.unstubAllGlobals();
  });
});
