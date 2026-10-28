import type { AgentTask, LocalDirectoryResourceRef, ProjectResource } from "../types";

/**
 * Which runs are working in the same folder right now (DENE-2667 PR 12).
 *
 * The daemon never makes a run wait for a folder: several runs share it the
 * way people share a checkout, and each agent's brief names the others
 * (`folderRunRegistry` in server/internal/daemon/local_directory.go). This is
 * the same fact for the people watching: runs whose machine and folder match
 * are editing the same files on the same checked-out branch.
 *
 * A run's folder is the work_dir its daemon reported — a bound folder or the
 * machine's default directory is the run's cwd — or, before it has reported
 * one, the folder its code decision names. A run in its own scratch directory
 * never matches another one.
 */

const RUNNING_STATUSES: ReadonlySet<AgentTask["status"]> = new Set([
  "dispatched",
  "running",
]);

/** Maps a runtime id to the daemon (machine) it belongs to, when known. */
export type DaemonOfRuntime = (runtimeId: string) => string | null | undefined;

/** One folder on one machine. */
export interface RunFolder {
  /** The daemon id, or the runtime id when the runtime is not known. */
  machine: string;
  path: string;
}

/** Same folder whatever the separators or trailing slashes. */
export function normalizeFolderPath(path: string | null | undefined): string {
  const s = (path ?? "").trim().replace(/\\/g, "/");
  if (!s) return "";
  return s.replace(/\/+$/, "") || "/";
}

/** Where a dispatched or running task works; null when it is not running or has no folder yet. */
export function runFolder(task: AgentTask, daemonOf: DaemonOfRuntime): RunFolder | null {
  if (!RUNNING_STATUSES.has(task.status)) return null;
  const decision = task.code_decision;
  const path = normalizeFolderPath(
    task.work_dir || (decision?.kind === "local_in_place" ? decision.path : ""),
  );
  if (!path) return null;
  const machine = daemonOf(task.runtime_id) || task.runtime_id;
  if (!machine) return null;
  return { machine, path };
}

function sameFolder(a: RunFolder, b: RunFolder): boolean {
  return a.machine === b.machine && a.path === b.path;
}

/** Every dispatched or running task in `folder`, oldest first. */
export function runsInFolder(
  tasks: readonly AgentTask[],
  daemonOf: DaemonOfRuntime,
  folder: RunFolder,
): AgentTask[] {
  return tasks
    .filter((task) => {
      const at = runFolder(task, daemonOf);
      return at !== null && sameFolder(at, folder);
    })
    .sort((a, b) => a.created_at.localeCompare(b.created_at));
}

/**
 * The runs working in a project's folder: those whose code decision names the
 * resource, plus any run on the same machine whose folder is the same path
 * (another project bound to it, or a run that already reported its work_dir).
 */
export function runsInProjectFolder(
  tasks: readonly AgentTask[],
  daemonOf: DaemonOfRuntime,
  resource: ProjectResource,
): AgentTask[] {
  if (resource.resource_type !== "local_directory") return [];
  const ref = (resource.resource_ref ?? {}) as LocalDirectoryResourceRef;
  const paths = new Set(
    [ref.local_path, ref.real_path].map(normalizeFolderPath).filter(Boolean),
  );
  return tasks
    .filter((task) => {
      if (!RUNNING_STATUSES.has(task.status)) return false;
      if (task.code_decision?.resource_id === resource.id) return true;
      const at = runFolder(task, daemonOf);
      return at !== null && at.machine === ref.daemon_id && paths.has(at.path);
    })
    .sort((a, b) => a.created_at.localeCompare(b.created_at));
}

/**
 * For each folder this issue's runs are in, every run in that folder — only
 * where it holds more than one run, which is when a person needs telling.
 */
export function issueSharedFolders(
  tasks: readonly AgentTask[],
  daemonOf: DaemonOfRuntime,
  issueId: string,
): Array<{ folder: RunFolder; runs: AgentTask[] }> {
  const folders: RunFolder[] = [];
  for (const task of tasks) {
    if (!issueId || task.issue_id !== issueId) continue;
    const at = runFolder(task, daemonOf);
    if (at && !folders.some((seen) => sameFolder(seen, at))) folders.push(at);
  }
  return folders
    .map((folder) => ({ folder, runs: runsInFolder(tasks, daemonOf, folder) }))
    .filter((group) => group.runs.length > 1);
}
