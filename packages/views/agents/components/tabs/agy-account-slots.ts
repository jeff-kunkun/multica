export type AgyAccountSlot = "account1" | "account2" | "custom";

export const ACCOUNT1_DIR = ".gemini";
export const ACCOUNT2_DIR = ".gemini-account2";

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
  const match = normalized.match(/^(.*)[/\\]\.gemini(?:-account2)?$/);
  return match?.[1] || null;
}

export function readProcessHomeDir(): string | null {
  const env = (globalThis as { process?: { env?: Record<string, string | undefined> } })
    .process?.env;
  const home = env?.HOME || env?.USERPROFILE;
  return home && home.length > 0 ? home : null;
}

export function resolveHomeDir(profile: string): string | null {
  return inferHomeDirFromGeminiPath(profile) ?? readProcessHomeDir();
}

export function joinHomeDir(home: string, leaf: string): string {
  const base = home.replace(/[/\\]+$/, "");
  const sep = base.includes("\\") && !base.includes("/") ? "\\" : "/";
  return `${base}${sep}${leaf}`;
}

export function expandHomePrefix(path: string, homeDir: string): string {
  const trimmed = path.trim();
  if (trimmed === "~") return homeDir.replace(/[/\\]+$/, "");
  if (trimmed.startsWith("~/")) return joinHomeDir(homeDir, trimmed.slice(2));
  if (trimmed.startsWith("~\\")) return joinHomeDir(homeDir, trimmed.slice(2));
  return trimmed;
}

export function detectAgyAccountSlot(profile: string): AgyAccountSlot {
  const trimmed = profile.trim();
  if (!trimmed) return "account1";
  const base = pathBasename(trimmed);
  if (trimmed === `~/${ACCOUNT1_DIR}` || base === ACCOUNT1_DIR) return "account1";
  if (trimmed === `~/${ACCOUNT2_DIR}` || base === ACCOUNT2_DIR) return "account2";
  return "custom";
}

export function resolveSlotDirectory(
  slot: AgyAccountSlot,
  customPath: string,
  homeDir: string | null,
): string {
  if (slot === "account1") return "";
  if (slot === "account2") {
    return homeDir ? joinHomeDir(homeDir, ACCOUNT2_DIR) : `~/${ACCOUNT2_DIR}`;
  }
  const trimmed = customPath.trim();
  if (homeDir && (trimmed === "~" || trimmed.startsWith("~/") || trimmed.startsWith("~\\"))) {
    return expandHomePrefix(trimmed, homeDir);
  }
  return trimmed;
}

export function loginDirectory(
  slot: AgyAccountSlot,
  profile: string,
  homeDir: string | null,
): string {
  if (profile && isAbsoluteFsPath(profile)) return profile;
  if (slot === "account1") {
    return homeDir ? joinHomeDir(homeDir, ACCOUNT1_DIR) : `~/${ACCOUNT1_DIR}`;
  }
  if (slot === "account2") {
    return homeDir ? joinHomeDir(homeDir, ACCOUNT2_DIR) : `~/${ACCOUNT2_DIR}`;
  }
  if (homeDir && (profile === "~" || profile.startsWith("~/"))) {
    return expandHomePrefix(profile, homeDir);
  }
  return profile;
}

export function formatAgyLoginCommand(directory: string): string {
  return directory ? `agy --gemini_dir=${directory}` : "agy";
}
