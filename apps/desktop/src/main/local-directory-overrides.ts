import { mkdir, readFile, rename, writeFile } from "fs/promises";
import { dirname, join } from "path";

// Keep in sync with server/internal/daemon/local_directory_overrides.go.
export const LOCAL_DIRECTORY_OVERRIDES_FILE = "local-directory-overrides.json";

export type LocalDirectorySharedOverride = {
  daemonId: string;
  localPath: string;
};

type OverrideFile = {
  overrides?: Array<{
    daemon_id?: string;
    local_path?: string;
    skip_path_mutex?: boolean;
  }>;
};

export function localDirectoryOverridesPath(profileDir: string): string {
  return join(profileDir, LOCAL_DIRECTORY_OVERRIDES_FILE);
}

export function normalizeOverridePath(path: string): string {
  return path.replace(/[\\/]+$/, "") || path;
}

export function parseLocalDirectoryOverrides(raw: string): LocalDirectorySharedOverride[] {
  let parsed: OverrideFile;
  try {
    parsed = JSON.parse(raw) as OverrideFile;
  } catch {
    return [];
  }
  if (!parsed || !Array.isArray(parsed.overrides)) return [];
  const out: LocalDirectorySharedOverride[] = [];
  const seen = new Set<string>();
  for (const row of parsed.overrides) {
    const daemonId = typeof row.daemon_id === "string" ? row.daemon_id.trim() : "";
    const localPath =
      typeof row.local_path === "string" ? normalizeOverridePath(row.local_path.trim()) : "";
    if (!daemonId || !localPath) continue;
    if (row.skip_path_mutex === false) continue;
    const key = `${daemonId}\n${localPath}`;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push({ daemonId, localPath });
  }
  return out;
}

export function serializeLocalDirectoryOverrides(
  rows: LocalDirectorySharedOverride[],
): string {
  return `${JSON.stringify(
    {
      overrides: rows.map((row) => ({
        daemon_id: row.daemonId,
        local_path: row.localPath,
        skip_path_mutex: true,
      })),
    },
    null,
    2,
  )}\n`;
}

export async function readLocalDirectoryOverrides(
  filePath: string,
): Promise<LocalDirectorySharedOverride[]> {
  try {
    const raw = await readFile(filePath, "utf-8");
    return parseLocalDirectoryOverrides(raw);
  } catch (err) {
    const code = (err as NodeJS.ErrnoException).code;
    if (code === "ENOENT") return [];
    throw err;
  }
}

export async function writeLocalDirectorySharedOverride(
  filePath: string,
  input: { daemonId: string; localPath: string; enabled: boolean },
): Promise<LocalDirectorySharedOverride[]> {
  const daemonId = input.daemonId.trim();
  const localPath = normalizeOverridePath(input.localPath.trim());
  if (!daemonId || !localPath) {
    throw new Error("daemon_id and local_path are required");
  }
  const existing = await readLocalDirectoryOverrides(filePath);
  const next = existing.filter(
    (row) =>
      !(row.daemonId === daemonId && normalizeOverridePath(row.localPath) === localPath),
  );
  if (input.enabled) {
    next.push({ daemonId, localPath });
  }
  await mkdir(dirname(filePath), { recursive: true });
  const tmp = `${filePath}.${process.pid}.tmp`;
  await writeFile(tmp, serializeLocalDirectoryOverrides(next), "utf-8");
  await rename(tmp, filePath);
  return next;
}
