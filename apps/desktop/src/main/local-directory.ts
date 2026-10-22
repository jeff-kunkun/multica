import { ipcMain, dialog, BrowserWindow } from "electron";
import { execFile } from "child_process";
import { access, realpath, stat } from "fs/promises";
import { constants as fsConstants } from "fs";
import { basename, isAbsolute } from "path";
import { promisify } from "util";
import { normalizeRepoUrl } from "@multica/core/projects/source-rule";
import { initLocalGit, type InitLocalGitResult } from "./local-git";
import {
  provisionProjectDirectory,
  removeProvisionedDirectory,
  type ProvisionProjectDirectoryResult,
} from "./provision-project-dir";

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
   * The repository root containing the directory, when there is one. The
   * create flow uses it to tell a plain folder from a subdirectory of a repo.
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

/** One TypeScript copy: packages/core/projects/source-rule.ts. */
export const normalizeRepoKey = normalizeRepoUrl;

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
  const gitRoot = await gitTopLevel(path);
  const hasCommit = gitRoot ? await gitHasCommit(gitRoot) : false;
  // An empty repository (no commit yet) is reported as not-a-repo.
  const result: ValidateLocalDirectoryResult = {
    ok: true,
    is_git_repo: hasCommit,
  };
  try {
    result.real_path = await realpath(path);
  } catch {
    // Unresolvable means "no better identity than what the user typed"; the
    // server falls back to local_path, which is what it compared before this
    // field existed.
  }
  if (gitRoot) {
    result.git_root = gitRoot;
    const key = await repoKeyOf(gitRoot);
    if (key) result.repo_key = key;
  }
  return result;
}

async function gitHasCommit(gitRoot: string): Promise<boolean> {
  try {
    await run("git", ["-C", gitRoot, "rev-parse", "--verify", "HEAD"], {
      timeout: 5000,
    });
    return true;
  } catch {
    return false;
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

  ipcMain.handle(
    "local-directory:init-git",
    (_event, path: string): Promise<InitLocalGitResult> => initLocalGit(path),
  );

  ipcMain.handle(
    "local-directory:provision-project",
    (_event, input: { root?: string; dirName?: string; gitInit?: boolean }): Promise<ProvisionProjectDirectoryResult> =>
      provisionProjectDirectory({
        root: input?.root ?? "",
        dirName: input?.dirName ?? "",
        gitInit: input?.gitInit !== false,
      }),
  );

  ipcMain.handle(
    "local-directory:remove-provisioned",
    (_event, input: { root?: string; path?: string }) =>
      removeProvisionedDirectory({ root: input?.root ?? "", path: input?.path ?? "" }),
  );
}
