import { spawn } from "child_process";
import { basename } from "path";
import { stat } from "fs/promises";
import { BrowserWindow, dialog, ipcMain } from "electron";
import {
  IDLE_TRANSFER_JOB_STATE,
  transferExportFilename,
  type TransferJobKind,
  type TransferJobState,
  type TransferPickPathResult,
  type TransferProgressEvent,
  type TransferRunRequest,
  type TransferRunResult,
} from "../shared/workspace-transfer";
import {
  parseTransferRunRequest,
  runTransferCli,
  type TransferCommandHooks,
  type TransferCommandResult,
} from "./workspace-transfer-cli";
import {
  activeDesktopProfileName,
  desktopCliSpawnEnv,
  resolveDesktopCliBinary,
} from "./daemon-manager";

/**
 * The transfer in flight, owned here rather than in the card.
 *
 * The CLI is a main-process child, so it keeps running when the renderer
 * navigates away and unmounts the card. Keeping the run's state here is what
 * lets a remounted card pick the same run back up instead of showing an idle
 * form over a running export (DENE-240).
 */
let job: TransferJobState = IDLE_TRANSFER_JOB_STATE;
let nextRunId = 1;

function jobKind(req: TransferRunRequest): TransferJobKind {
  if (req.action === "export") return "export";
  if (req.action === "bind-runtimes") return "bind-runtimes";
  return req.dryRun ? "import-preview" : "import-apply";
}

/**
 * Broadcasts to every window, not just the sender: the run belongs to the app,
 * and a second window showing settings must not sit on a stale snapshot.
 */
function broadcast(channel: string, payload: unknown): void {
  for (const win of BrowserWindow.getAllWindows()) {
    if (!win.isDestroyed() && !win.webContents.isDestroyed()) {
      win.webContents.send(channel, payload);
    }
  }
}

function setJob(next: TransferJobState): void {
  job = next;
  broadcast("transfer:state", job);
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

function spawnTransferCommand(
  bin: string,
  args: string[],
  hooks: TransferCommandHooks,
): Promise<TransferCommandResult> {
  return new Promise((resolve) => {
    const child = spawn(bin, args, { env: desktopCliSpawnEnv() });
    let stdout = "";
    let stderr = "";
    child.stdout?.on("data", (buf: Buffer) => {
      const chunk = buf.toString("utf8");
      stdout += chunk;
      hooks.onStdout(chunk);
    });
    child.stderr?.on("data", (buf: Buffer) => {
      const chunk = buf.toString("utf8");
      stderr += chunk;
      hooks.onStderr(chunk);
    });
    child.on("error", (err) => {
      resolve({
        code: 1,
        stdout,
        stderr: stderr || errorMessage(err),
      });
    });
    child.on("close", (code) => {
      resolve({ code: code ?? 1, stdout, stderr });
    });
  });
}

export function setupWorkspaceTransfer(
  windowGetter: () => BrowserWindow | null,
): void {
  ipcMain.handle(
    "transfer:pick-export-path",
    async (event, raw?: { slug?: unknown }): Promise<TransferPickPathResult> => {
      const win = BrowserWindow.fromWebContents(event.sender) ?? windowGetter();
      if (!win) return { ok: false, reason: "no_window" };
      const slug = typeof raw?.slug === "string" ? raw.slug : "workspace";
      try {
        const result = await dialog.showSaveDialog(win, {
          defaultPath: transferExportFilename(slug),
          filters: [{ name: "Zip", extensions: ["zip"] }],
        });
        if (result.canceled || !result.filePath) {
          return { ok: false, reason: "cancelled" };
        }
        const path = result.filePath.endsWith(".zip")
          ? result.filePath
          : `${result.filePath}.zip`;
        return { ok: true, path, fileName: basename(path) };
      } catch (err) {
        return { ok: false, reason: "error", error: errorMessage(err) };
      }
    },
  );

  ipcMain.handle(
    "transfer:pick-import-path",
    async (event): Promise<TransferPickPathResult> => {
      const win = BrowserWindow.fromWebContents(event.sender) ?? windowGetter();
      if (!win) return { ok: false, reason: "no_window" };
      try {
        const result = await dialog.showOpenDialog(win, {
          properties: ["openFile"],
          filters: [{ name: "Zip", extensions: ["zip"] }],
        });
        if (result.canceled || result.filePaths.length === 0) {
          return { ok: false, reason: "cancelled" };
        }
        const picked = result.filePaths[0];
        if (!picked) return { ok: false, reason: "cancelled" };
        return { ok: true, path: picked, fileName: basename(picked) };
      } catch (err) {
        return { ok: false, reason: "error", error: errorMessage(err) };
      }
    },
  );

  ipcMain.handle("transfer:state", (): TransferJobState => job);

  ipcMain.handle(
    "transfer:run",
    async (_event, raw: unknown): Promise<TransferRunResult> => {
      const req = parseTransferRunRequest(raw);
      if (!req) {
        return { ok: false, code: "unknown", message: "invalid transfer request" };
      }
      if (job.running) {
        return { ok: false, code: "busy", message: "a transfer is already running" };
      }
      const runId = nextRunId++;
      setJob({
        runId,
        kind: jobKind(req),
        running: true,
        progress: null,
        inPath: req.action === "import" ? req.inPath : null,
        result: null,
      });
      let result: TransferRunResult;
      try {
        result = await runTransferCli(req, {
          resolveCli: resolveDesktopCliBinary,
          profileName: activeDesktopProfileName,
          runCommand: spawnTransferCommand,
          statSize: async (path) => (await stat(path)).size,
          sendProgress: (progress: TransferProgressEvent) => {
            // A progress line for a run the app has moved past is noise.
            if (job.runId !== runId) return;
            job = { ...job, progress };
            broadcast("transfer:progress", progress);
          },
        });
      } catch (err) {
        result = { ok: false, code: "unknown", message: errorMessage(err) };
      }
      // A newer run already replaced this one: its state wins, and this
      // answer goes only to the caller that is still awaiting it.
      if (job.runId === runId) {
        setJob({ ...job, running: false, progress: null, result });
      }
      return result;
    },
  );
}
