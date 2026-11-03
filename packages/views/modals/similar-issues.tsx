"use client";

import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, type SimilarIssueCandidate } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { AppLink, resolveClickIntent } from "../navigation";
import { useT } from "../i18n";

export function SimilarIssueList({
  candidates, onNavigate,
}: {
  candidates: SimilarIssueCandidate[];
  onNavigate?: () => void;
}) {
  const p = useWorkspacePaths();
  const { t } = useT("modals");
  return (
    <div className="space-y-1">
      {candidates.map((candidate) => (
        <AppLink
          key={candidate.id}
          href={p.issueDetail(candidate.id)}
          className="flex min-h-11 items-center gap-2 rounded px-2 py-1 text-body hover:bg-accent"
          onClick={(event) => {
            if (resolveClickIntent(event) === "push" && !event.shiftKey) onNavigate?.();
          }}
        >
          <span className="shrink-0 text-muted-foreground">{candidate.identifier}</span>
          <span className="min-w-0 flex-1 break-words">{candidate.title}</span>
          {candidate.recent_done && (
            <span className="shrink-0 text-caption text-muted-foreground">
              {t(($) => $.create_issue.similar_done)}
            </span>
          )}
        </AppLink>
      ))}
    </div>
  );
}

export function SimilarIssuesNotice({
  title, description, projectId, parentId, onNavigate,
}: {
  title: string;
  description: string;
  projectId?: string;
  parentId?: string;
  onNavigate?: () => void;
}) {
  const wsId = useWorkspaceId();
  const { t } = useT("modals");
  const [debounced, setDebounced] = useState({ title, description, projectId, parentId });
  useEffect(() => {
    const timer = setTimeout(() => setDebounced({ title, description, projectId, parentId }), 450);
    return () => clearTimeout(timer);
  }, [title, description, projectId, parentId]);
  const { data } = useQuery({
    queryKey: ["issues", wsId, "similar", debounced],
    queryFn: ({ signal }) => api.similarIssues({
      project_id: debounced.projectId!,
      title: debounced.title,
      description: debounced.description,
      parent_issue_id: debounced.parentId,
      signal,
    }),
    enabled: !!debounced.projectId && debounced.title.trim().length >= 3,
    retry: false,
    staleTime: 10_000,
  });
  // Never show a previous project's results while the next request debounces.
  if (debounced.projectId !== projectId || debounced.title !== title || !data?.candidates.length) return null;
  return (
    <aside className="mx-5 my-2 max-h-40 shrink-0 overflow-y-auto rounded-md border border-amber-500/30 bg-amber-500/5 p-2" aria-label={t(($) => $.create_issue.similar_title)}>
      <p className="px-2 text-body font-medium">{t(($) => $.create_issue.similar_title)}</p>
      <SimilarIssueList candidates={data.candidates} onNavigate={onNavigate} />
      <p className="px-2 pt-1 text-caption text-muted-foreground">
        {t(($) => $.create_issue.similar_hint)}
      </p>
    </aside>
  );
}
