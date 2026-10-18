import { app, ipcMain, shell, type WebContents } from "electron";
import { execFile } from "child_process";
import { existsSync, readFileSync } from "fs";
import { join } from "path";
import type {
  OfficePagePreview,
  OfficeResumeResult,
  OfficeScreenFrame,
  OfficeScreenState,
  OfficeViewState,
} from "../../shared/office-screen";
import { fetchOwnedDaemonHealth } from "../daemon-manager";
import { CaptureProcess } from "./capture";
import { officeVdHome, readLatch, resumeLatch, stopLatch, writeDurable } from "./latch";
import { computeReceipt, type BrokerHealth } from "./receipt";

// Office screen (DENE-2261): the red stop button, its receipts, and the live
// picture of the virtual display the office agents browse on.
//
// Stop is a file write (latch.ts), so it works with the network, the daemon
// or the broker down. The broker re-reads it on every request, so input is
// sealed the moment the file lands; the daemon notices within a quarter
// second and cancels its runs. Neither ever resumes on its own.
//
// The picture only reads pixels of the virtual display (screen-capture
// helper) and nothing here moves, raises or focuses a window, so watching
// never takes Kun's screen, pointer, keyboard or focus.

const POLL_MS = 1_000;
const PROBE_TIMEOUT_MS = 800;
// Fixed System Settings deep link, not renderer-controlled.
const SCREEN_RECORDING_SETTINGS_URL =
  "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture";

function toolsDir(): string {
  // Packaged: resources/** is unpacked next to app.asar (see electron-builder asarUnpack).
  return join(app.getAppPath(), "resources", "office-vd").replace("app.asar", "app.asar.unpacked");
}

function brokerPort(): number {
  return Number(process.env.OFFICE_VD_PORT || 9339);
}

async function fetchJSON(url: string, init?: RequestInit): Promise<Record<string, unknown> | null> {
  try {
    const res = await fetch(url, { ...init, signal: AbortSignal.timeout(PROBE_TIMEOUT_MS * 4) });
    if (!res.ok) return null;
    return (await res.json()) as Record<string, unknown>;
  } catch {
    return null;
  }
}

async function brokerHealth(): Promise<BrokerHealth> {
  try {
    const res = await fetch(`http://127.0.0.1:${brokerPort()}/health`, {
      signal: AbortSignal.timeout(PROBE_TIMEOUT_MS),
    });
    return res.ok ? ((await res.json()) as Record<string, unknown>) : {};
  } catch {
    return null;
  }
}

function runHelper(helper: string, args: string[]): Promise<Record<string, unknown> | null> {
  return new Promise((resolve) => {
    execFile(helper, args, { timeout: 60_000 }, (err, stdout) => {
      if (err) return resolve(null);
      try {
        resolve(JSON.parse(stdout.trim().split("\n").pop() ?? "") as Record<string, unknown>);
      } catch {
        resolve(null);
      }
    });
  });
}

export function setupOfficeScreen(): void {
  const supported = process.platform === "darwin";
  const home = officeVdHome();
  const helper = join(toolsDir(), "screen-capture");
  const prefsFile = join(app.getPath("userData"), "office-screen.json");

  const subscribers = new Map<number, WebContents>();
  let permission: OfficeScreenState["permission"] = "unknown";
  let lastSent = "";
  let poll: ReturnType<typeof setInterval> | null = null;
  let latestBroker: BrokerHealth = null;
  let latestDaemon: Record<string, unknown> | null = null;

  const readPrefs = (): { viewEnabled: boolean } => {
    try {
      const v = JSON.parse(readFileSync(prefsFile, "utf8")) as { viewEnabled?: unknown };
      return { viewEnabled: v.viewEnabled !== false };
    } catch {
      return { viewEnabled: true };
    }
  };

  const view: OfficeViewState = {
    enabled: readPrefs().viewEnabled,
    status: "off",
    reason: null,
    windows: 0,
    lastFrameAt: null,
  };

  const state = (): OfficeScreenState => {
    const latch = readLatch(home);
    return {
      supported,
      latch,
      receipt: computeReceipt(latch, latestBroker, latestDaemon),
      view: { ...view },
      permission,
    };
  };

  const broadcast = (force = false) => {
    const s = state();
    const key = JSON.stringify({ ...s, view: { ...s.view, lastFrameAt: null } });
    if (!force && key === lastSent) return s;
    lastSent = key;
    for (const wc of subscribers.values()) if (!wc.isDestroyed()) wc.send("office-screen:state", s);
    return s;
  };

  const capture = new CaptureProcess(helper, ["--home", home, "--fps", "2", "--width", "1280"], {
    onFrame: (frame: OfficeScreenFrame) => {
      view.lastFrameAt = frame.at;
      for (const wc of subscribers.values()) if (!wc.isDestroyed()) wc.send("office-screen:frame", frame);
    },
    onStatus: (s) => {
      view.status = s.status;
      view.reason = s.reason;
      view.windows = s.windows;
      if (s.reason === "permission") permission = "missing";
      broadcast();
    },
  });

  const refreshPermission = async () => {
    if (!supported || !existsSync(helper)) return;
    const r = await runHelper(helper, ["--check-permission"]);
    permission = r?.granted === true ? "granted" : r ? "missing" : "unknown";
  };

  // The picture runs only while someone is looking and Kun wants it.
  const reconcileCapture = () => {
    const helperPresent = supported && existsSync(helper);
    const want = view.enabled && subscribers.size > 0 && helperPresent && permission === "granted";
    if (want) {
      if (!capture.running) capture.start();
      return;
    }
    if (capture.running) capture.stop();
    if (!view.enabled) Object.assign(view, { status: "off", reason: null, windows: 0 });
    else if (!helperPresent) Object.assign(view, { status: "unavailable", reason: "helper_missing", windows: 0 });
    else if (permission !== "granted") Object.assign(view, { status: "blocked", reason: "permission", windows: 0 });
  };

  const tick = async () => {
    [latestBroker, latestDaemon] = await Promise.all([brokerHealth(), fetchOwnedDaemonHealth().catch(() => null)]);
    reconcileCapture();
    broadcast();
  };

  const ensurePolling = () => {
    if (subscribers.size > 0 && !poll) {
      poll = setInterval(() => void tick(), POLL_MS);
      void tick();
    } else if (subscribers.size === 0 && poll) {
      clearInterval(poll);
      poll = null;
      reconcileCapture();
    }
  };

  ipcMain.handle("office-screen:get-state", () => state());

  ipcMain.handle("office-screen:subscribe", async (event) => {
    const wc = event.sender;
    if (!subscribers.has(wc.id)) {
      subscribers.set(wc.id, wc);
      wc.once("destroyed", () => {
        subscribers.delete(wc.id);
        ensurePolling();
      });
    }
    await refreshPermission();
    ensurePolling();
    reconcileCapture();
    return broadcast(true);
  });

  ipcMain.handle("office-screen:unsubscribe", (event) => {
    subscribers.delete(event.sender.id);
    ensurePolling();
  });

  ipcMain.handle("office-screen:stop", async () => {
    if (supported) stopLatch(home, "desktop", "stop");
    await tick();
    return broadcast(true);
  });

  ipcMain.handle("office-screen:resume", async (_e, expectGeneration: unknown): Promise<OfficeResumeResult> => {
    if (!supported) return { ok: false, error: "unsupported", state: state() };
    const gen = typeof expectGeneration === "number" && Number.isSafeInteger(expectGeneration) ? expectGeneration : null;
    const r = resumeLatch(home, gen, "desktop", "resume");
    await tick();
    const s = broadcast(true);
    return r.ok ? { ok: true, state: s } : { ok: false, error: "generation_mismatch", state: s };
  });

  ipcMain.handle("office-screen:set-view-enabled", (_e, enabled: unknown) => {
    view.enabled = enabled === true;
    try {
      writeDurable(prefsFile, JSON.stringify({ viewEnabled: view.enabled }) + "\n");
    } catch {
      // The choice still holds for this session.
    }
    reconcileCapture();
    return broadcast(true);
  });

  // Only on Kun's click: this is the one call that may show the macOS prompt.
  ipcMain.handle("office-screen:request-permission", async () => {
    if (supported && existsSync(helper)) await runHelper(helper, ["--request-permission"]);
    await refreshPermission();
    reconcileCapture();
    return broadcast(true);
  });

  ipcMain.handle("office-screen:open-permission-settings", async () => {
    if (!supported) return;
    // eslint-disable-next-line no-restricted-syntax -- constant URL, see above
    await shell.openExternal(SCREEN_RECORDING_SETTINGS_URL);
  });

  // Degraded picture without screen recording: screenshots of the pages the
  // broker registered as the agents' own, through read-only CDP calls that
  // the broker allows even while stopped. Never other tabs.
  ipcMain.handle("office-screen:page-previews", async (): Promise<OfficePagePreview[]> => {
    if (!supported) return [];
    const base = `http://127.0.0.1:${brokerPort()}`;
    const control = await fetchJSON(`${base}/control/state`);
    const owned = Array.isArray(control?.ownedTargets) ? (control.ownedTargets as Array<{ targetId?: unknown }>) : [];
    const call = (method: string, params: Record<string, unknown>, sessionId?: string) =>
      fetchJSON(base, { method: "POST", body: JSON.stringify({ method, params, sessionId }) });
    const out: OfficePagePreview[] = [];
    for (const t of owned.slice(0, 6)) {
      if (typeof t.targetId !== "string") continue;
      const attached = await call("Target.attachToTarget", { targetId: t.targetId, flatten: true });
      const sessionId = typeof attached?.sessionId === "string" ? attached.sessionId : null;
      if (!sessionId) continue;
      const shot = await call("Page.captureScreenshot", { format: "jpeg", quality: 50 }, sessionId);
      await call("Target.detachFromTarget", { sessionId });
      if (typeof shot?.data === "string") out.push({ targetId: t.targetId, src: `data:image/jpeg;base64,${shot.data}` });
    }
    return out;
  });

  app.on("will-quit", () => capture.stop());
}
