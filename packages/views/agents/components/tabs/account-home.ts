// Host home-directory helpers for the account surface: the daemon reports
// absolute account directories, and these resolve `~` lever values and
// synthesized slot directories against the same host home.

export function isAbsoluteFsPath(path: string): boolean {
  return path.startsWith("/") || /^[A-Za-z]:[\\/]/.test(path);
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

/** The runtime's reported home when it is absolute, else this process's own. */
export function resolveHomeDir(runtimeHome?: string | null): string | null {
  const hostHome =
    runtimeHome && isAbsoluteFsPath(runtimeHome.trim()) ? runtimeHome.trim() : null;
  return hostHome ?? readProcessHomeDir();
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
