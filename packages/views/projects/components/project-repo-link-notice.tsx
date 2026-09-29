"use client";

import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { projectResourcesOptions } from "@multica/core/projects";
import {
  findRepoBinding,
  parseRepoLocator,
  resolveRepoLink,
} from "@multica/core/repo-links";
import type { RepoBinding, RepoLink } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { useRepoCatalog } from "../../settings/components/use-repo-catalog";

interface NoticeResource {
  resource_type: string;
  resource_ref: { url?: string; repo_key?: string };
}

/**
 * Scopes (host/owner) on this project that no connection can answer.
 * A miss does not block the project; the page shows one sentence per scope.
 */
export function unmatchedConnectionScopes(
  links: RepoLink[],
  bindings: RepoBinding[],
  resources: readonly NoticeResource[],
  projectId: string,
): string[] {
  const scopes: string[] = [];
  const seen = new Set<string>();

  const add = (raw: string) => {
    const locator = parseRepoLocator(raw);
    if (!locator || locator.owner === "*") return;
    const scope = `${locator.host}/${locator.owner}`;
    if (seen.has(scope)) return;
    const binding = findRepoBinding(bindings, raw);
    const resolved = resolveRepoLink(links, raw, binding?.pinned_link_id ?? null);
    const state = binding?.state ?? resolved.state;
    if (state !== "disconnected") return;
    seen.add(scope);
    scopes.push(scope);
  };

  for (const resource of resources) {
    if (resource.resource_type === "github_repo" && resource.resource_ref.url) {
      add(resource.resource_ref.url);
    } else if (
      resource.resource_type === "local_directory" &&
      resource.resource_ref.repo_key
    ) {
      add(resource.resource_ref.repo_key);
    }
  }

  for (const binding of bindings) {
    if (binding.state !== "disconnected") continue;
    if (!binding.source_projects?.some((project) => project.id === projectId)) continue;
    add(binding.repo_url);
  }

  return scopes;
}

export function ProjectRepoLinkNotice({ projectId }: { projectId: string }) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const catalog = useRepoCatalog(wsId);
  const ready = catalog.source === "catalog" || catalog.source === "legacy";
  const resources = useQuery({
    ...projectResourcesOptions(wsId, projectId),
    enabled: ready && !!wsId && !!projectId,
  });

  if (!ready || !Array.isArray(resources.data)) return null;
  const scopes = unmatchedConnectionScopes(
    catalog.links,
    catalog.bindings,
    resources.data,
    projectId,
  );
  if (scopes.length === 0) return null;

  return (
    <div className="border-b border-surface-border px-4 py-2">
      {scopes.map((scope) => (
        <div key={scope} className="flex items-center justify-between gap-3 py-1">
          <p className="min-w-0 truncate text-caption text-muted-foreground">
            {t(($) => $.repo_links.unmatched, { scope })}
          </p>
          <Button
            variant="ghost"
            size="sm"
            className="h-7 shrink-0 px-2 text-caption text-muted-foreground"
            aria-label={t(($) => $.repo_links.connect_named, { repo: scope })}
            onClick={() => {
              navigation.push(
                `${paths.settings()}?tab=git-connections&connect_scope=${encodeURIComponent(scope)}`,
              );
            }}
          >
            {t(($) => $.repo_links.add_connection)}
          </Button>
        </div>
      ))}
    </div>
  );
}
