// AGY-specific pieces of the account surface: the `--gemini_dir` flag, the
// host-home resolution its paths need, and the login command.
//
// The numbered slot registry itself is no longer AGY's own — it is one entry of
// the per-CLI family table, and its parsing lives in `account-slots.ts`. AGY's
// list stays under `runtime_config.agy_slots`, the key the backend rotation
// (`server/pkg/agent/agy_quota.go`) reads.

export function getGeminiDir(args: string[]): string {
  const flagIndex = args.findIndex((value) => value === "--gemini_dir");
  if (flagIndex >= 0) return args[flagIndex + 1] ?? "";
  const inline = args.find((value) => value.startsWith("--gemini_dir="));
  return inline?.slice("--gemini_dir=".length) ?? "";
}

export function setGeminiDir(args: string[], profile: string): string[] {
  const next = [...args];
  for (let index = next.length - 1; index >= 0; index -= 1) {
    if (next[index] === "--gemini_dir") next.splice(index, 2);
    else if (next[index]?.startsWith("--gemini_dir=")) next.splice(index, 1);
  }
  const trimmed = profile.trim();
  return trimmed ? [...next, "--gemini_dir", trimmed] : next;
}

export function isGeminiDirToken(value: string): boolean {
  return value === "--gemini_dir" || value.startsWith("--gemini_dir=");
}

export function isAbsoluteFsPath(path: string): boolean {
  return path.startsWith("/") || /^[A-Za-z]:[\\/]/.test(path);
}

export function pathBasename(path: string): string {
  const normalized = path.replace(/[/\\]+$/, "");
  const parts = normalized.split(/[/\\]/);
  return parts[parts.length - 1] ?? "";
}

export function inferHomeDirFromGeminiPath(path: string): string | null {
  const trimmed = path.trim();
  if (!trimmed || trimmed.startsWith("~")) return null;
  const normalized = trimmed.replace(/[/\\]+$/, "");
  const match = normalized.match(/^(.*)[/\\]\.gemini(?:-account\d+)?$/);
  return match?.[1] || null;
}

export function runtimeHomeDir(
  runtime?: { metadata?: Record<string, unknown> } | null,
): string | null {
  const value = runtime?.metadata?.home_dir;
  if (typeof value !== "string") return null;
  const trimmed = value.trim();
  return isAbsoluteFsPath(trimmed) ? trimmed : null;
}

export function readProcessHomeDir(): string | null {
  const desktopHome = (globalThis as { desktopAPI?: { homeDir?: unknown } }).desktopAPI?.homeDir;
  if (typeof desktopHome === "string" && isAbsoluteFsPath(desktopHome.trim())) {
    return desktopHome.trim();
  }
  const env = (globalThis as { process?: { env?: Record<string, string | undefined> } })
    .process?.env;
  const home = env?.HOME || env?.USERPROFILE;
  return home && isAbsoluteFsPath(home) ? home : null;
}

export function resolveHomeDir(
  profile: string,
  runtimeHome?: string | null,
): string | null {
  const hostHome =
    runtimeHome && isAbsoluteFsPath(runtimeHome.trim()) ? runtimeHome.trim() : null;
  return inferHomeDirFromGeminiPath(profile) ?? hostHome ?? readProcessHomeDir();
}

export function joinHomeDir(home: string, leaf: string): string {
  const base = home.replace(/[/\\]+$/, "");
  const sep = base.includes("\\") && !base.includes("/") ? "\\" : "/";
  const normalizedLeaf = sep === "\\" ? leaf.replace(/\//g, "\\") : leaf;
  return `${base}${sep}${normalizedLeaf}`;
}

export function expandHomePrefix(path: string, homeDir: string): string {
  const trimmed = path.trim();
  if (trimmed === "~") return homeDir.replace(/[/\\]+$/, "");
  if (trimmed.startsWith("~/")) return joinHomeDir(homeDir, trimmed.slice(2));
  if (trimmed.startsWith("~\\")) return joinHomeDir(homeDir, trimmed.slice(2));
  return trimmed;
}

export function formatAgyLoginCommand(directory: string): string {
  return directory ? `agy --gemini_dir=${directory}` : "agy";
}
