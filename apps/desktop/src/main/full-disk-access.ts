import { app, ipcMain, shell } from "electron";
import { open } from "fs/promises";
import { homedir } from "os";
import { join } from "path";
import type { FullDiskAccessState } from "../shared/full-disk-access";

// Full Disk Access is the one macOS privacy grant that never prompts: an app
// only shows up in that System Settings list after someone adds it with "+".
// Agents the daemon starts inherit this app's grants, so without it they hang
// on the first protected folder. We can't query TCC directly; instead we open
// files only readable with the grant. EPERM means missing, success means
// granted, and a probe that doesn't exist on this Mac tells us nothing.
const PROBES = [
  "Library/Application Support/com.apple.TCC/TCC.db",
  "Library/Safari/Bookmarks.plist",
  "Library/Containers/com.apple.stocks",
];

type OpenFn = (path: string) => Promise<{ close: () => Promise<void> }>;

export async function probeFullDiskAccess(
  platform: NodeJS.Platform = process.platform,
  home: string = homedir(),
  openFile: OpenFn = (path) => open(path, "r"),
): Promise<FullDiskAccessState["status"]> {
  if (platform !== "darwin") return "unsupported";
  for (const probe of PROBES) {
    try {
      const handle = await openFile(join(home, probe));
      await handle.close();
      return "granted";
    } catch (err) {
      const code = (err as NodeJS.ErrnoException).code;
      if (code === "EPERM" || code === "EACCES") return "missing";
      // ENOENT or anything else: try the next probe.
    }
  }
  return "unsupported";
}

// Fixed System Settings deep link, not renderer-controlled, so it bypasses
// the http/https allowlist in external-url.ts on purpose.
export const FULL_DISK_ACCESS_SETTINGS_URL =
  "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles";

export function setupFullDiskAccess(): void {
  ipcMain.handle(
    "system:full-disk-access",
    async (): Promise<FullDiskAccessState> => ({
      status: await probeFullDiskAccess(),
      appName: app.getName(),
    }),
  );
  ipcMain.handle("system:open-full-disk-access-settings", async () => {
    if (process.platform !== "darwin") return;
    // eslint-disable-next-line no-restricted-syntax -- constant URL, see above
    await shell.openExternal(FULL_DISK_ACCESS_SETTINGS_URL);
  });
}
