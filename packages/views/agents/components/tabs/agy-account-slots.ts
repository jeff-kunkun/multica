// AGY's numbered account slots.
//
// `runtime_config.agy_slots` is the agent-level list of numbered Gemini
// directories this agent may rotate to when a quota runs out; the backend
// (`server/pkg/agent/agy_quota.go`) reads the same key, so both sides must
// agree on its shape — hence one parse/write pair here.
//
// The slots themselves are host-only paths: this module derives directories
// from the host home. It never reads a credential, and the per-directory
// sign-in / quota state comes from the daemon's `agent_accounts` report rather
// than from the `agy_logged_in_dirs` / `agy_quota_exhausted` metadata keys,
// which stay on the API surface for older desktop builds.

export const ACCOUNT1_DIR = ".gemini";

export const AGY_SLOTS_RUNTIME_KEY = "agy_slots";
export const DEFAULT_NUMBERED_ACCOUNTS = [1, 2, 3] as const;
export const MAX_AGY_ACCOUNT_NUMBER = 32;

export type AgyNumberedSlot = `account${number}`;
export type AgyAccountSlot = AgyNumberedSlot | "custom";

const ACCOUNT_SLOT_RE = /^account(\d+)$/;
const GEMINI_ACCOUNT_DIR_RE = /^\.gemini-(account\d+)$/;

export function parseAccountNumber(slot: string): number | null {
  const match = slot.match(ACCOUNT_SLOT_RE);
  if (!match) return null;
  const n = Number(match[1]);
  return Number.isInteger(n) && n >= 1 && n <= MAX_AGY_ACCOUNT_NUMBER ? n : null;
}

export function numberedSlotId(account: number): AgyNumberedSlot {
  return `account${account}`;
}

export function isNumberedAccountSlot(slot: string): slot is AgyNumberedSlot {
  return parseAccountNumber(slot) !== null;
}

export function accountDirectoryLeaf(account: number): string {
  return account <= 1 ? ACCOUNT1_DIR : `.gemini-account${account}`;
}

export function nextAccountNumber(accounts: readonly number[]): number {
  const used = new Set(accounts);
  for (let n = 2; n <= MAX_AGY_ACCOUNT_NUMBER; n += 1) {
    if (!used.has(n)) return n;
  }
  return MAX_AGY_ACCOUNT_NUMBER + 1;
}

export function normalizeAccountNumbers(accounts: readonly number[]): number[] {
  const unique = new Set<number>([1]);
  for (const n of accounts) {
    if (Number.isInteger(n) && n >= 1 && n <= MAX_AGY_ACCOUNT_NUMBER) unique.add(n);
  }
  return [...unique].sort((a, b) => a - b);
}

export function parseAgySlotsConfig(
  runtimeConfig: Record<string, unknown> | null | undefined,
  geminiDir = "",
): number[] {
  const raw = runtimeConfig?.[AGY_SLOTS_RUNTIME_KEY];
  const persisted =
    raw && typeof raw === "object" && !Array.isArray(raw)
      ? (raw as { accounts?: unknown }).accounts
      : undefined;
  if (Array.isArray(persisted)) {
    return normalizeAccountNumbers(
      persisted.filter((value): value is number => typeof value === "number"),
    );
  }
  const seeded: number[] = [...DEFAULT_NUMBERED_ACCOUNTS];
  const detected = parseAccountNumber(detectAgyAccountSlot(geminiDir));
  if (detected) seeded.push(detected);
  return normalizeAccountNumbers(seeded);
}

export function writeAgySlotsConfig(
  runtimeConfig: Record<string, unknown> | null | undefined,
  accounts: readonly number[],
): Record<string, unknown> {
  return {
    ...(runtimeConfig ?? {}),
    [AGY_SLOTS_RUNTIME_KEY]: { accounts: normalizeAccountNumbers(accounts) },
  };
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

function slotFromBasename(base: string): AgyAccountSlot {
  if (base === ACCOUNT1_DIR) return "account1";
  const match = base.match(GEMINI_ACCOUNT_DIR_RE);
  const slot = match?.[1];
  return slot && isNumberedAccountSlot(slot) ? slot : "custom";
}

export function detectAgyAccountSlot(profile: string): AgyAccountSlot {
  const trimmed = profile.trim();
  if (!trimmed) return "account1";
  return slotFromBasename(pathBasename(trimmed));
}

export function formatAgyLoginCommand(directory: string): string {
  return directory ? `agy --gemini_dir=${directory}` : "agy";
}
