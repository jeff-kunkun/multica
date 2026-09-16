import { spawn } from "child_process";
import { basename } from "path";
import { stat } from "fs/promises";
import { BrowserWindow, dialog, ipcMain } from "electron";
import {
  transferExportFilename,
  type TransferPickPathResult,
  type TransferProgressEvent,
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

let transferBusy = false;

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

  ipcMain.handle(
    "transfer:run",
    async (event, raw: unknown): Promise<TransferRunResult> => {
      const req = parseTransferRunRequest(raw);
      if (!req) {
        return { ok: false, code: "unknown", message: "invalid transfer request" };
      }
      if (transferBusy) {
        return { ok: false, code: "busy", message: "a transfer is already running" };
      }
      const sender = event.sender;
      transferBusy = true;
      try {
        return await runTransferCli(req, {
          resolveCli: resolveDesktopCliBinary,
          profileName: activeDesktopProfileName,
          runCommand: spawnTransferCommand,
          statSize: async (path) => (await stat(path)).size,
          sendProgress: (progress: TransferProgressEvent) => {
            if (!sender.isDestroyed()) {
              sender.send("transfer:progress", progress);
            }
          },
        });
      } finally {
        transferBusy = false;
      }
    },
  );
}
