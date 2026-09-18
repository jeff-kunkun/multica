import { delimiter, join } from "path";

/**
 * PATH padding for macOS/Linux GUI launches.
 *
 * A GUI-launched app inherits launchd's minimal PATH, which omits everything
 * only a login shell would add (Homebrew, nvm/fnm shims, ~/.local/bin). The
 * app runs `fix-path` first to recover the login shell's PATH; this module is
 * the fallback it prepends afterwards, for when that recovery comes up short
 * (broken rc file, non-interactive $SHELL, an entry the user never added).
 *
 * The daemon is spawned with this environment and resolves agent CLIs with
 * exec.LookPath, so this list *is* the daemon's PATH until the user's own
 * shell config can be recovered.
 *
 * Order is the contract, not a detail: PATH lookups short-circuit on the first
 * match, so an earlier directory shadows every later one. User-local installs
 * must come first, because the official standalone installers (Codex, Claude,
 * …) write to ~/.local/bin while a Homebrew/npm global install puts a
 * *separate, older* copy of the same CLI in /opt/homebrew/bin. Listing
 * Homebrew first made the daemon pin the stale copy — /opt/homebrew/bin/codex
 * 0.148.0 shadowed ~/.local/bin/codex 0.154.0, so the model picker never
 * offered gpt-6-astra even after `codex debug models` was re-run (#DENE-507).
 *
 * Kept free of electron imports so the ordering invariant stays unit-tested.
 */

/**
 * Directories prepended to PATH on macOS/Linux, most specific first: the
 * user's own install directory, then the machine-wide package managers
 * (Apple-silicon Homebrew ahead of the Intel/manual /usr/local).
 */
export function fallbackPathDirs(home: string): string[] {
  return [join(home, ".local/bin"), "/opt/homebrew/bin", "/usr/local/bin"];
}

/**
 * Prepend the fallback directories to a PATH value, so they pad the inherited
 * PATH instead of replacing it. Duplicates are harmless — PATH lookups
 * short-circuit — but an unset/empty PATH yields only the fallback entries: a
 * trailing separator would leave an empty segment, which some tools read as
 * the current directory.
 */
export function applyFallbackPathDirs(
  path: string | undefined,
  home: string,
): string {
  const dirs = fallbackPathDirs(home).join(delimiter);
  const current = path ?? "";
  return current === "" ? dirs : `${dirs}${delimiter}${current}`;
}
