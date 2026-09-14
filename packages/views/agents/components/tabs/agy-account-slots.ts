/** Preset slots shown in the UI. Append here, then add matching i18n keys. */
export const PRESET_ACCOUNT_SLOTS = ["account1", "account2", "account3"] as const;

export type AgyPresetAccountSlot = (typeof PRESET_ACCOUNT_SLOTS)[number];
export type AgyAccountSlot = AgyPresetAccountSlot | "custom";

export const ACCOUNT1_DIR = ".gemini";
export const ACCOUNT2_DIR = ".gemini-account2";
export const ACCOUNT3_DIR = ".gemini-account3";

const PRESET_SLOT_SET = new Set<string>(PRESET_ACCOUNT_SLOTS);

export function isPresetAccountSlot(slot: string): slot is AgyPresetAccountSlot {
  return PRESET_SLOT_SET.has(slot);
}

export function isIsolatedAccountSlot(slot: AgyAccountSlot): boolean {
  return slot !== "custom" && slot !== "account1";
}

export function accountSlotDirectory(slot: AgyPresetAccountSlot): string {
  if (slot === "account1") return ACCOUNT1_DIR;
  return `.gemini-${slot}`;
}

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
  return `${base}${sep}${leaf}`;
}

export function expandHomePrefix(path: string, homeDir: string): string {
  const trimmed = path.trim();
  if (trimmed === "~") return homeDir.replace(/[/\\]+$/, "");
  if (trimmed.startsWith("~/")) return joinHomeDir(homeDir, trimmed.slice(2));
  if (trimmed.startsWith("~\\")) return joinHomeDir(homeDir, trimmed.slice(2));
  return trimmed;
}

function slotFromBasename(base: string): AgyAccountSlot {
  if (base === ACCOUNT1_DIR) return "account1";
  const match = base.match(/^\.gemini-(account\d+)$/);
  const slot = match?.[1];
  return slot && isPresetAccountSlot(slot) ? slot : "custom";
}

export function detectAgyAccountSlot(profile: string): AgyAccountSlot {
  const trimmed = profile.trim();
  if (!trimmed) return "account1";
  return slotFromBasename(pathBasename(trimmed));
}

function resolvePresetDirectory(
  slot: AgyPresetAccountSlot,
  homeDir: string | null,
): string {
  const leaf = accountSlotDirectory(slot);
  return homeDir ? joinHomeDir(homeDir, leaf) : `~/${leaf}`;
}

export function resolveSlotDirectory(
  slot: AgyAccountSlot,
  customPath: string,
  homeDir: string | null,
): string {
  if (slot === "account1") return "";
  if (isPresetAccountSlot(slot)) return resolvePresetDirectory(slot, homeDir);
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
  if (isPresetAccountSlot(slot)) return resolvePresetDirectory(slot, homeDir);
  if (homeDir && (profile === "~" || profile.startsWith("~/"))) {
    return expandHomePrefix(profile, homeDir);
  }
  return profile;
}

export function formatAgyLoginCommand(directory: string): string {
  return directory ? `agy --gemini_dir=${directory}` : "agy";
}
