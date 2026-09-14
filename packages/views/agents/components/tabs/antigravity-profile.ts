export const ANTIGRAVITY_PRIMARY_DIR = ".gemini";
export const ANTIGRAVITY_SECONDARY_DIR = ".gemini-account2";

export type AntigravitySlot = "primary" | "secondary" | "custom";

function trimTrailingSlashes(path: string): string {
  return path.replace(/[\\/]+$/, "");
}

function pathSeparator(path: string): "/" | "\\" {
  return path.includes("\\") && !path.includes("/") ? "\\" : "/";
}

function toUnix(path: string): string {
  return trimTrailingSlashes(path.replace(/\\/g, "/"));
}

function fromUnix(unixPath: string, sep: "/" | "\\"): string {
  return sep === "\\" ? unixPath.replace(/\//g, "\\") : unixPath;
}

export function joinHomeDir(home: string, name: string): string {
  if (home === "~") return `~/${name}`;
  const sep = pathSeparator(home);
  return `${trimTrailingSlashes(home)}${sep}${name}`;
}

export function expandLeadingTilde(path: string, homeDir: string | null): string {
  const trimmed = path.trim();
  if (!homeDir || !trimmed.startsWith("~")) return trimmed;
  if (trimmed === "~") return homeDir;
  if (trimmed.startsWith("~/") || trimmed.startsWith("~\\")) {
    const rest = trimmed.slice(2);
    return rest ? joinHomeDir(homeDir, rest) : homeDir;
  }
  return trimmed;
}

function basename(path: string): string {
  const unix = toUnix(path);
  const index = unix.lastIndexOf("/");
  return index >= 0 ? unix.slice(index + 1) : unix;
}

function dirname(path: string): string {
  const sep = pathSeparator(path);
  const unix = toUnix(path);
  const index = unix.lastIndexOf("/");
  if (index <= 0) return unix.startsWith("/") ? "/" : "";
  return fromUnix(unix.slice(0, index), sep);
}

export function inferHomeDir(profilePath: string): string | null {
  const trimmed = profilePath.trim();
  if (!trimmed || trimmed.startsWith("~")) return null;
  const name = basename(trimmed);
  if (name !== ANTIGRAVITY_PRIMARY_DIR && name !== ANTIGRAVITY_SECONDARY_DIR) {
    return null;
  }
  const parent = dirname(trimmed);
  return parent || null;
}

export function isAbsolutePath(path: string): boolean {
  const trimmed = path.trim();
  if (!trimmed || trimmed.startsWith("~")) return false;
  if (trimmed.startsWith("/")) return true;
  return /^[A-Za-z]:[\\/]/.test(trimmed);
}

export function classifyAntigravitySlot(profilePath: string): AntigravitySlot {
  const trimmed = profilePath.trim();
  if (!trimmed) return "primary";
  const name = basename(trimmed);
  if (name === ANTIGRAVITY_PRIMARY_DIR) return "primary";
  if (name === ANTIGRAVITY_SECONDARY_DIR) return "secondary";
  return "custom";
}

export function resolveSecondaryPath(currentPath: string): string {
  const home = inferHomeDir(currentPath);
  if (home) return joinHomeDir(home, ANTIGRAVITY_SECONDARY_DIR);
  return joinHomeDir("~", ANTIGRAVITY_SECONDARY_DIR);
}

export function resolveSlotPath(
  slot: AntigravitySlot,
  currentPath: string,
  customPath = currentPath,
): string {
  if (slot === "primary") return "";
  if (slot === "secondary") {
    return expandLeadingTilde(resolveSecondaryPath(currentPath), inferHomeDir(currentPath));
  }
  return expandLeadingTilde(customPath, inferHomeDir(currentPath) ?? inferHomeDir(customPath));
}

export function getAntigravityProfile(args: string[]): string {
  const flagIndex = args.findIndex((value) => value === "--gemini_dir");
  if (flagIndex >= 0) return args[flagIndex + 1] ?? "";
  const inline = args.find((value) => value.startsWith("--gemini_dir="));
  return inline?.slice("--gemini_dir=".length) ?? "";
}

export function setAntigravityProfile(args: string[], profile: string): string[] {
  const next = [...args];
  for (let index = next.length - 1; index >= 0; index -= 1) {
    if (next[index] === "--gemini_dir") next.splice(index, 2);
    else if (next[index]?.startsWith("--gemini_dir=")) next.splice(index, 1);
  }
  const normalized = profile.trim();
  return normalized ? [...next, "--gemini_dir", normalized] : next;
}

export function applyAntigravitySlot(
  args: string[],
  slot: AntigravitySlot,
  customPath?: string,
): string[] {
  const current = getAntigravityProfile(args);
  return setAntigravityProfile(args, resolveSlotPath(slot, current, customPath));
}

export function formatAntigravityLoginCommand(profilePath: string): string {
  const trimmed = profilePath.trim();
  if (!trimmed) return "agy";
  const forShell =
    trimmed === "~"
      ? "$HOME"
      : trimmed.startsWith("~/")
        ? `$HOME/${trimmed.slice(2)}`
        : trimmed;
  return `agy --gemini_dir=${forShell}`;
}

export function displayAntigravityDirectory(profilePath: string): string {
  const trimmed = profilePath.trim();
  return trimmed || `~/${ANTIGRAVITY_PRIMARY_DIR}`;
}
