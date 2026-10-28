"use client";

import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, ChevronRight, Copy, FolderOpen, X } from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  projectResourcesOptions,
  runsInProjectFolder,
  useCreateProjectResource,
  useDeleteProjectResource,
  useUpdateProjectResource,
} from "@multica/core/projects";
import type { LocalDirectoryResourceRef, ProjectResource } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { copyText } from "@multica/ui/lib/clipboard";
import { useT } from "../../i18n";
import { isDesktopShell, pickDirectory, validateLocalDirectory } from "../../platform/local-directory";
import { useLocalDaemonStatus } from "../../platform/use-local-daemon-status";
import { localDirectoryResourceRef } from "../new-project-directory";
import { FolderRunsNotice, useFolderRunsSnapshot } from "./folder-runs-notice";

type FolderResource = ProjectResource & { resource_ref: LocalDirectoryResourceRef };

function isFolder(resource: ProjectResource): resource is FolderResource {
  return resource.resource_type === "local_directory";
}

/** The row's name, or the path's last segment when it has none. */
function folderName(folder: FolderResource): string {
  const named = (folder.label || folder.resource_ref.label || "").trim();
  if (named) return named;
  const path = folder.resource_ref.local_path.trim().replace(/[\\/]+$/, "");
  return path.split(/[\\/]/).pop() || path;
}

const HOME_PREFIX = /^(?:\/Users\/(?!Shared(?:\/|$))[^/]+|\/home\/[^/]+|\/root|[A-Za-z]:\\Users\\[^\\]+)(?=$|[\\/])/;

/**
 * The path as shown: the home folder becomes `~`, so a screenshot does not
 * carry the account name. Copying still takes the absolute path.
 */
export function displayFolderPath(path: string): string {
  return path.replace(HOME_PREFIX, "~");
}

function FolderPathLine({ path }: { path: string }) {
  const { t } = useT("projects");
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current);
  }, []);

  const copy = async () => {
    if (!(await copyText(path))) {
      toast.error(t(($) => $.folder.copy_failed));
      return;
    }
    setCopied(true);
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopied(false), 1500);
  };

  return (
    <div className="flex items-start gap-1 pl-5">
      <span className="min-w-0 flex-1 break-all font-mono text-micro text-muted-foreground">
        {displayFolderPath(path)}
      </span>
      <button
        type="button"
        aria-label={copied ? t(($) => $.folder.copied) : t(($) => $.folder.copy_path_aria)}
        onClick={() => void copy()}
        className={`-mt-0.5 inline-flex h-5 shrink-0 items-center gap-0.5 rounded-sm px-1 text-micro transition-colors hover:bg-accent [@media(pointer:coarse)]:h-8 [@media(pointer:coarse)]:px-2 ${copied ? "text-success" : "text-muted-foreground hover:text-foreground"}`}
      >
        {copied ? <Check className="size-3" /> : <Copy className="size-3" />}
        <span aria-live="polite">{copied ? t(($) => $.folder.copied) : t(($) => $.folder.copy)}</span>
      </button>
    </div>
  );
}

/**
 * The folder a project's agents start in, one per machine. Choosing a folder
 * on a machine that already has one replaces it, keeping the row (and every
 * run decision that names it). Whether the folder is a Git repository is the
 * CLI's business; this only records what the machine saw.
 */
export function ProjectFolderSection({ projectId }: { projectId: string }) {
  const { t } = useT("projects");
  const wsId = useWorkspaceId();
  const daemon = useLocalDaemonStatus();
  const desktop = isDesktopShell();
  const [open, setOpen] = useState(true);
  const [picking, setPicking] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const resources = useQuery(projectResourcesOptions(wsId, projectId));
  const create = useCreateProjectResource(wsId, projectId);
  const update = useUpdateProjectResource(wsId, projectId);
  const remove = useDeleteProjectResource(wsId, projectId);
  const { tasks, daemonOf } = useFolderRunsSnapshot();

  const folders = (resources.data ?? []).filter(isFolder);
  const mine = daemon.daemonId
    ? folders.find((folder) => folder.resource_ref.daemon_id === daemon.daemonId)
    : undefined;
  const canPick = desktop && !!daemon.daemonId && daemon.running && !picking;

  const choose = async () => {
    if (!canPick || !daemon.daemonId) return;
    setError(null);
    setPicking(true);
    try {
      const picked = await pickDirectory(mine?.resource_ref.local_path);
      if (!picked.ok || !picked.path) {
        if (picked.reason && picked.reason !== "cancelled") setError(picked.error ?? t(($) => $.folder.invalid));
        return;
      }
      const checked = await validateLocalDirectory(picked.path);
      if (!checked.ok) {
        setError(checked.error ?? t(($) => $.folder.invalid));
        return;
      }
      const label = picked.basename || null;
      if (mine) {
        // The server keeps identity fields an edit leaves out, so a new folder
        // states all of them — null where this machine could not tell.
        await update.mutateAsync({
          resourceId: mine.id,
          data: {
            ...(label ? { label } : {}),
            resource_ref: {
              local_path: picked.path,
              daemon_id: daemon.daemonId,
              ...(label ? { label } : {}),
              real_path: checked.real_path ?? null,
              repo_key: checked.repo_key ?? null,
              is_git_repo: checked.is_git_repo ?? null,
            },
          },
        });
      } else {
        await create.mutateAsync({
          resource_type: "local_directory",
          resource_ref: localDirectoryResourceRef({
            localPath: picked.path,
            daemonId: daemon.daemonId,
            label,
            realPath: checked.real_path,
            repoKey: checked.repo_key,
            isGitRepo: checked.is_git_repo,
          }),
        });
      }
      toast.success(t(($) => $.folder.toast_saved));
    } catch (err) {
      toast.error(err instanceof Error && err.message ? err.message : t(($) => $.folder.toast_save_failed));
    } finally {
      setPicking(false);
    }
  };

  const handleRemove = async (folder: FolderResource) => {
    try {
      await remove.mutateAsync(folder.id);
    } catch (err) {
      toast.error(err instanceof Error && err.message ? err.message : t(($) => $.folder.toast_remove_failed));
    }
  };

  return (
    <div>
      <button
        type="button"
        className={`mb-2 flex w-full items-center gap-1 rounded-md px-2 py-1 text-caption font-medium transition-colors hover:bg-accent/70 ${open ? "" : "text-muted-foreground hover:text-foreground"}`}
        onClick={() => setOpen(!open)}
      >
        {t(($) => $.folder.title)}
        <ChevronRight
          className={`!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${open ? "rotate-90" : ""}`}
        />
      </button>
      {open && (
        <div className="space-y-1.5 pl-2">
          <p className="text-caption text-muted-foreground">{t(($) => $.folder.intro)}</p>
          {resources.isError ? (
            <p className="text-caption text-muted-foreground">{t(($) => $.folder.load_failed)}</p>
          ) : null}
          {!resources.isError && !resources.isPending && folders.length === 0 ? (
            <p className="text-caption text-muted-foreground">{t(($) => $.folder.empty)}</p>
          ) : null}
          {folders.map((folder) => {
            const ref = folder.resource_ref;
            const here = !!daemon.daemonId && ref.daemon_id === daemon.daemonId;
            return (
              <div key={folder.id} className="space-y-1">
                <div className="group flex items-center justify-between gap-2 py-1">
                  <div className="flex min-w-0 items-center gap-1.5">
                    <FolderOpen className="size-3.5 shrink-0 text-muted-foreground" />
                    <span className="truncate text-caption font-medium">{folderName(folder)}</span>
                    {daemon.daemonId ? (
                      <span className="shrink-0 text-micro text-muted-foreground">
                        {here ? t(($) => $.folder.this_machine) : t(($) => $.folder.other_machine)}
                      </span>
                    ) : null}
                    {ref.is_git_repo ? (
                      <span className="shrink-0 rounded-sm bg-muted px-1 text-micro text-muted-foreground">
                        {t(($) => $.folder.git)}
                      </span>
                    ) : null}
                  </div>
                  <div className="flex shrink-0 items-center gap-1">
                    {here ? (
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        className="h-6 px-1.5 text-micro text-muted-foreground hover:text-foreground"
                        disabled={!canPick}
                        onClick={() => void choose()}
                      >
                        {picking ? t(($) => $.folder.picking) : t(($) => $.folder.change)}
                      </Button>
                    ) : null}
                    <button
                      type="button"
                      aria-label={t(($) => $.folder.remove)}
                      title={t(($) => $.folder.remove)}
                      disabled={remove.isPending}
                      onClick={() => void handleRemove(folder)}
                      className="rounded-sm p-0.5 text-muted-foreground transition-opacity hover:bg-accent focus-visible:opacity-100 group-hover:opacity-100 [@media(hover:hover)]:opacity-0 [@media(pointer:coarse)]:p-2"
                    >
                      <X className="size-3" />
                    </button>
                  </div>
                </div>
                <FolderPathLine path={ref.local_path.trim()} />
                <FolderRunsNotice runs={runsInProjectFolder(tasks, daemonOf, folder)} />
              </div>
            );
          })}
          {desktop && !mine ? (
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-7 px-2 text-caption"
              disabled={!canPick}
              onClick={() => void choose()}
            >
              <FolderOpen className="size-3" />
              {picking ? t(($) => $.folder.picking) : t(($) => $.folder.choose)}
            </Button>
          ) : null}
          {desktop && !daemon.running ? (
            <p className="text-micro text-amber-600 dark:text-amber-400">{t(($) => $.folder.offline)}</p>
          ) : null}
          {!desktop ? (
            <p className="text-micro text-muted-foreground">{t(($) => $.folder.web_hint)}</p>
          ) : null}
          {error ? (
            <p role="alert" className="text-micro text-destructive">
              {error}
            </p>
          ) : null}
        </div>
      )}
    </div>
  );
}
