import type {
  GithubRepoResourceRef,
  LocalDirectoryExecutionMode,
  LocalDirectoryResourceRef,
  ProjectResource,
} from "../types";

/**
 * One rule, stated once, for both source surfaces (DENE-592 / DENE-595):
 *
 *   **A project that pins a local directory uses that directory.**
 *
 * It is a rule, not a ranking. The remote `github_repo` type stays — CI, cloud
 * runners and teammates without a local checkout depend on it — but on a
 * machine that holds the directory, a `github_repo` naming the same repository
 * is a second pointer to code already on disk, not a second repository.
 *
 * This module is deliberately pure and free of React/query so the project
 * picker and the task badge cannot drift into two different answers. The
 * canonical behavioural matrix lives in `source-rule.test.ts`.
 *
 * Its counterpart on the daemon is `server/internal/daemon/local_source.go`,
 * which can additionally read the directory's git remotes. Here we only have a
 * URL and a path, so every verdict this file produces is a SUSPICION that asks
 * the user a question — never a silent removal.
 */

/** Normalized repository identity: `host/owner/name`, or "" when unidentifiable. */
export function normalizeRepoUrl(raw: string): string {
  const input = (raw ?? "").trim();
  if (!input) return "";
  // A filesystem path is a location on one machine, not a repository identity.
  if (/^([/.~]|\\\\|[a-zA-Z]:[\\/]|file:\/\/)/.test(input)) return "";

  let rest = input;
  const scheme = /^(https?|ssh|git|git\+ssh):\/\//i.exec(rest);
  if (scheme) {
    rest = rest.slice(scheme[0].length);
  } else {
    // scp-like `user@host:owner/repo` — the single colon separates host from
    // path, so turn it into a slash and use one parser for both forms.
    const colon = rest.indexOf(":");
    if (colon >= 0 && !rest.slice(0, colon).includes("/")) {
      rest = `${rest.slice(0, colon)}/${rest.slice(colon + 1).replace(/^\/+/, "")}`;
    }
  }
  // Credentials are not identity: one repo cloned by two users is one repo.
  const at = rest.lastIndexOf("@");
  const firstSlash = rest.indexOf("/");
  if (at >= 0 && (firstSlash < 0 || at < firstSlash)) rest = rest.slice(at + 1);

  rest = rest.replace(/^\/+/, "").replace(/\/+$/, "");
  const slash = rest.indexOf("/");
  if (slash < 0) return "";
  let host = rest.slice(0, slash);
  let path = rest.slice(slash + 1);
  host = host.split(":")[0]!.trim().toLowerCase();
  path = path.replace(/\/+$/, "").replace(/\.git$/i, "").replace(/^\/+|\/+$/g, "");
  if (!host || !path) return "";
  return `${host}/${path.toLowerCase()}`;
}

/** Repository short name from a remote URL, lowercased; "" when unreadable. */
export function repoNameFromUrl(raw: string): string {
  const key = normalizeRepoUrl(raw);
  if (key) return key.slice(key.lastIndexOf("/") + 1);
  const s = (raw ?? "").trim().replace(/\/+$/, "").replace(/\.git$/i, "");
  const cut = Math.max(s.lastIndexOf("/"), s.lastIndexOf(":"));
  return s.slice(cut + 1).trim().toLowerCase();
}

/** Repository name a local directory most likely holds: its basename. */
export function repoNameFromLocalPath(path: string): string {
  const s = (path ?? "").trim().replace(/\\/g, "/").replace(/\/+$/, "");
  if (!s) return "";
  const base = s.slice(s.lastIndexOf("/") + 1);
  return base === "." ? "" : base.toLowerCase();
}

export function githubRef(r: ProjectResource): GithubRepoResourceRef | null {
  return r.resource_type === "github_repo"
    ? ((r.resource_ref ?? {}) as GithubRepoResourceRef)
    : null;
}

export function localDirectoryRef(r: ProjectResource): LocalDirectoryResourceRef | null {
  return r.resource_type === "local_directory"
    ? ((r.resource_ref ?? {}) as LocalDirectoryResourceRef)
    : null;
}

/** Absent `execution_mode` means `in_place`; unknown values are NOT invented. */
export function executionModeOfResource(r: ProjectResource): LocalDirectoryExecutionMode {
  const mode = localDirectoryRef(r)?.execution_mode;
  return mode === "worktree" || mode === "shared" || mode === "in_place" ? mode : "in_place";
}

/**
 * A repository configured twice: once as a remote URL, once as a directory on
 * some machine. `local` is what tasks on that machine will actually use;
 * `remotes` are the now-redundant `github_repo` rows.
 */
export interface DuplicateSourceGroup {
  /** Lowercased repository name the two sources agree on. */
  repoName: string;
  local: ProjectResource;
  remotes: ProjectResource[];
}

/**
 * Find repositories configured both ways.
 *
 * Matching is by repository NAME, because a name is all the server can compare:
 * it stores a URL and an absolute path, and only the machine holding that path
 * can read its git remotes. A name match is therefore strong enough to ASK
 * ("these look like the same repository — merge them?") and never strong enough
 * to remove a resource on the user's behalf. Two `github_repo` rows for one URL
 * are a different problem and are reported by {@link findRedundantRemotes}.
 */
export function findDuplicateSources(resources: ProjectResource[]): DuplicateSourceGroup[] {
  const locals = resources.filter((r) => r.resource_type === "local_directory");
  const remotes = resources.filter((r) => r.resource_type === "github_repo");
  const groups: DuplicateSourceGroup[] = [];
  for (const local of locals) {
    const path = localDirectoryRef(local)?.local_path ?? "";
    const name = repoNameFromLocalPath(path);
    if (!name) continue;
    const matched = remotes.filter((r) => {
      const url = githubRef(r)?.url ?? "";
      const remoteName = repoNameFromUrl(url);
      return remoteName !== "" && remoteName === name;
    });
    if (matched.length > 0) groups.push({ repoName: name, local, remotes: matched });
  }
  return groups;
}

/**
 * `github_repo` rows that point at the SAME repository as an earlier row. This
 * is an unambiguous duplicate — identical identity, no filesystem guess — so
 * unlike {@link findDuplicateSources} it needs no local directory to be present.
 * Returned in the order they would be removed: the first row of each identity
 * is kept.
 */
export function findRedundantRemotes(resources: ProjectResource[]): ProjectResource[] {
  const seen = new Set<string>();
  const redundant: ProjectResource[] = [];
  for (const r of resources) {
    const url = githubRef(r)?.url ?? "";
    const key = normalizeRepoUrl(url);
    if (!key) continue;
    if (seen.has(key)) redundant.push(r);
    else seen.add(key);
  }
  return redundant;
}

/** Where a task's code comes from, and which resource decided it. */
export interface TaskCodeSource {
  kind: "local_directory" | "remote_checkout";
  /** The resource that decided it; null when no local directory applies. */
  resource: ProjectResource | null;
  /** Absolute path of the pinned directory, when kind is local_directory. */
  localPath: string;
  executionMode: LocalDirectoryExecutionMode;
  /** True while the task has not reported a work dir yet — a prediction. */
  predicted: boolean;
}

/**
 * Resolve a task's code source from data already on the wire.
 *
 * `daemonId` is the machine the task runs on. It matters: a project may pin one
 * directory PER machine, and another machine's directory says nothing about
 * this task. When it is unknown, a single local directory is still reported —
 * with `predicted` true — because "probably this one" is more useful than
 * nothing, and the badge labels it as a prediction rather than a fact.
 */
export function resolveTaskCodeSource(params: {
  resources: ProjectResource[];
  daemonId?: string | null;
  /** Whether the daemon has reported a working directory for this task yet. */
  hasReportedWorkDir?: boolean;
}): TaskCodeSource {
  const { resources, daemonId, hasReportedWorkDir } = params;
  const locals = resources.filter((r) => r.resource_type === "local_directory");
  const forDaemon = daemonId
    ? locals.filter((r) => localDirectoryRef(r)?.daemon_id === daemonId)
    : locals;
  // More than one candidate and no daemon to disambiguate: refuse to guess.
  // Naming the wrong machine's path is worse than naming none.
  const resource = forDaemon.length === 1 ? forDaemon[0]! : null;
  if (!resource) {
    return {
      kind: "remote_checkout",
      resource: null,
      localPath: "",
      executionMode: "in_place",
      predicted: hasReportedWorkDir !== true,
    };
  }
  return {
    kind: "local_directory",
    resource,
    localPath: localDirectoryRef(resource)?.local_path ?? "",
    executionMode: executionModeOfResource(resource),
    predicted: hasReportedWorkDir !== true,
  };
}
