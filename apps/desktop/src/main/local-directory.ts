import { ipcMain, dialog, BrowserWindow } from "electron";
import { execFile } from "child_process";
import { access, realpath, stat } from "fs/promises";
import { constants as fsConstants } from "fs";
import { basename, dirname, isAbsolute, join } from "path";
import { promisify } from "util";
import { activeDaemonProfileDir } from "./daemon-manager";
import {
  localDirectoryOverridesPath,
  readLocalDirectoryOverrides,
  writeLocalDirectorySharedOverride,
} from "./local-directory-overrides";

export interface PickDirectoryResult {
  ok: boolean;
  path?: string;
  basename?: string;
  /** Set when ok=false. "cancelled" = user dismissed; otherwise an error blurb. */
  reason?: "cancelled" | "no_window" | "error";
  error?: string;
}

export interface ValidateLocalDirectoryResult {
  ok: boolean;
  /** When ok=false, identifies which check failed so the renderer can render a
   *  specific message without parsing free-form text. */
  reason?:
    | "not_absolute"
    | "not_found"
    | "not_a_directory"
    | "not_readable"
    | "not_writable"
    | "error";
  error?: string;
  /**
   * Whether the directory sits inside a git working tree. Only set when ok=true.
   *
   * Worktree execution mode requires a git repo, and only the desktop app can
   * see the filesystem — the server cannot. Reporting it here lets the picker
   * disable that mode with a reason at selection time, instead of letting the
   * user save a resource whose very first task fails.
   */
  is_git_repo?: boolean;
  /**
   * The symlink-resolved absolute path. This is the directory's IDENTITY:
   * the server's "one row per directory" rule keys on it, so /tmp/x and
   * /private/tmp/x cannot be bound twice under two spellings (DENE-617).
   *
   * Only this machine can compute it — the server holds a string, not a
   * filesystem — which is why it is reported here and stored on the resource.
   */
  real_path?: string;
  /**
   * The normalized identity of the repository this directory holds, taken
   * from its `origin` remote. Absent for a plain folder, a repository with no
   * remote, or a git that could not be run: all of those are "unidentifiable",
   * and an unidentifiable directory never collides with another.
   */
  repo_key?: string;
  /**
   * Where parallel mode would put this directory's working copies by default:
   * the repository's sibling. Shown in the picker BEFORE the user commits to
   * parallel mode, because the cost of that mode is a copy per task on their
   * own disk and they are entitled to see where it lands.
   */
  default_worktree_root?: string;
  /**
   * The repository root containing the directory, when there is one. The
   * picker needs it to tell the user that a worktree root they typed sits
   * inside their own repository — the rule execenv enforces at task time.
   */
  git_root?: string;
}

const run = promisify(execFile);

/**
 * The repository root containing `path`, or "" when it is not in one.
 * `--show-toplevel` is what git itself uses, so a subdirectory of a repo
 * answers with the repo — matching what the daemon resolves at task time.
 */
async function gitTopLevel(path: string): Promise<string> {
  try {
    const { stdout } = await run("git", ["-C", path, "rev-parse", "--show-toplevel"], {
      timeout: 5000,
    });
    return stdout.trim();
  } catch {
    return "";
  }
}

/**
 * The normalized identity of the repository at `gitRoot`, from its `origin`
 * remote. Normalization mirrors `server/internal/repoident`: strip transport,
 * credentials, port and a `.git` suffix, lowercase the rest. The two must
 * agree or a duplicate the server rejects would look fine here.
 */
async function repoKeyOf(gitRoot: string): Promise<string> {
  if (!gitRoot) return "";
  try {
    const { stdout } = await run("git", ["-C", gitRoot, "remote", "get-url", "origin"], {
      timeout: 5000,
    });
    return normalizeRepoKey(stdout.trim());
  } catch {
    return "";
  }
}

export function normalizeRepoKey(raw: string): string {
  let s = (raw ?? "").trim();
  if (!s) return "";
  // A filesystem path is a location on one machine, not a repository identity.
  if (/^([/.~]|\\\\|[a-zA-Z]:[\\/]|file:\/\/)/.test(s)) return "";
  const scheme = /^(https?|ssh|git|git\+ssh):\/\//i.exec(s);
  if (scheme) {
    s = s.slice(scheme[0].length);
  } else {
    // scp-like `user@host:owner/repo` — one colon separates host from path.
    const colon = s.indexOf(":");
    if (colon >= 0 && !s.slice(0, colon).includes("/")) {
      s = `${s.slice(0, colon)}/${s.slice(colon + 1).replace(/^\/+/, "")}`;
    }
  }
  // Credentials are not identity: one repo cloned by two users is one repo.
  const at = s.lastIndexOf("@");
  const firstSlash = s.indexOf("/");
  if (at >= 0 && (firstSlash < 0 || at < firstSlash)) s = s.slice(at + 1);
  s = s.replace(/^\/+/, "").replace(/\/+$/, "");
  const slash = s.indexOf("/");
  if (slash < 0) return "";
  const host = s.slice(0, slash).split(":")[0]!.trim().toLowerCase();
  const rest = s
    .slice(slash + 1)
    .replace(/\/+$/, "")
    .replace(/\.git$/i, "")
    .replace(/^\/+|\/+$/g, "")
    .toLowerCase();
  if (!host || !rest) return "";
  return `${host}/${rest}`;
}

/**
 * The default landing place for parallel-mode working copies: the
 * repository's sibling `<repo>.multica-worktrees`. Mirrors
 * execenv.DefaultWorktreeRoot — the picker only PREVIEWS it, the daemon
 * decides, and the two must name the same directory or the preview lies.
 */
export function defaultWorktreeRoot(gitRoot: string): string {
  if (!gitRoot) return "";
  return join(dirname(gitRoot), `${basename(gitRoot)}.multica-worktrees`);
}

async function validateLocalDirectory(
  path: string,
): Promise<ValidateLocalDirectoryResult> {
  if (!path || !isAbsolute(path)) {
    return { ok: false, reason: "not_absolute" };
  }
  try {
    const st = await stat(path);
    if (!st.isDirectory()) return { ok: false, reason: "not_a_directory" };
  } catch (err) {
    const code = (err as NodeJS.ErrnoException).code;
    if (code === "ENOENT") return { ok: false, reason: "not_found" };
    return { ok: false, reason: "error", error: errorMessage(err) };
  }
  try {
    await access(path, fsConstants.R_OK);
  } catch {
    return { ok: false, reason: "not_readable" };
  }
  try {
    await access(path, fsConstants.W_OK);
  } catch {
    return { ok: false, reason: "not_writable" };
  }
  const isGitRepo = await isInsideGitWorkTree(path);
  const result: ValidateLocalDirectoryResult = { ok: true, is_git_repo: isGitRepo };
  try {
    result.real_path = await realpath(path);
  } catch {
    // Unresolvable means "no better identity than what the user typed"; the
    // server falls back to local_path, which is what it compared before this
    // field existed.
  }
  if (isGitRepo) {
    const gitRoot = await gitTopLevel(path);
    if (gitRoot) {
      result.git_root = gitRoot;
      result.default_worktree_root = defaultWorktreeRoot(gitRoot);
      const key = await repoKeyOf(gitRoot);
      if (key) result.repo_key = key;
    }
  }
  return result;
}

/**
 * Walks up from `path` looking for a `.git` entry, mirroring how git itself
 * resolves a working tree — so a subdirectory of a repo reports true, matching
 * what the daemon does with `rev-parse --show-toplevel` at task time.
 *
 * `.git` is accepted as either a directory (ordinary clone) or a file (a linked
 * worktree, where it holds a gitdir pointer). Any error means "can't tell",
 * which is reported as not-a-repo: this only drives a UI hint, and the daemon
 * re-checks authoritatively before running anything.
 */
async function isInsideGitWorkTree(path: string): Promise<boolean> {
  let current = path;
  for (;;) {
    try {
      await stat(join(current, ".git"));
      return true;
    } catch {
      // Not here — keep walking up.
    }
    const parent = dirname(current);
    if (parent === current) return false;
    current = parent;
  }
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export function setupLocalDirectory(
  windowGetter: () => BrowserWindow | null,
): void {
  ipcMain.handle(
    "local-directory:pick",
    async (event, defaultPath?: string): Promise<PickDirectoryResult> => {
      const win =
        BrowserWindow.fromWebContents(event.sender) ?? windowGetter();
      if (!win) return { ok: false, reason: "no_window" };
      try {
        const result = await dialog.showOpenDialog(win, {
          // Multiple-selection is intentionally disabled — a project_resource
          // points at a single directory, and the create flow expects one
          // path per click. Multi-add would have to be a separate UX.
          properties: ["openDirectory", "createDirectory"],
          ...(defaultPath ? { defaultPath } : {}),
        });
        if (result.canceled || result.filePaths.length === 0) {
          return { ok: false, reason: "cancelled" };
        }
        const picked = result.filePaths[0];
        if (!picked) return { ok: false, reason: "cancelled" };
        return { ok: true, path: picked, basename: basename(picked) };
      } catch (err) {
        return { ok: false, reason: "error", error: errorMessage(err) };
      }
    },
  );

  ipcMain.handle(
    "local-directory:validate",
    (_event, path: string): Promise<ValidateLocalDirectoryResult> =>
      validateLocalDirectory(path),
  );

  ipcMain.handle("local-directory:list-shared-overrides", async () => {
    const dir = await activeDaemonProfileDir();
    if (!dir) return [];
    try {
      return await readLocalDirectoryOverrides(localDirectoryOverridesPath(dir));
    } catch {
      return [];
    }
  });

  ipcMain.handle(
    "local-directory:set-shared-override",
    async (
      _event,
      input: { daemonId?: string; localPath?: string; enabled?: boolean },
    ): Promise<{ ok: boolean; error?: string }> => {
      const dir = await activeDaemonProfileDir();
      if (!dir) return { ok: false, error: "daemon profile is unresolved" };
      try {
        await writeLocalDirectorySharedOverride(localDirectoryOverridesPath(dir), {
          daemonId: input.daemonId ?? "",
          localPath: input.localPath ?? "",
          enabled: input.enabled === true,
        });
        return { ok: true };
      } catch (err) {
        return { ok: false, error: errorMessage(err) };
      }
    },
  );
}
