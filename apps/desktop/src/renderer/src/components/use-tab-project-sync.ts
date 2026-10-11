import { useEffect, useMemo } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { parseTabSubject, useCurrentWorkspace } from "@multica/core/paths";
import { chatSessionsOptions } from "@multica/core/chat/queries";
import { issueDetailOptions } from "@multica/core/issues/queries";
import { projectListOptions } from "@multica/core/projects/queries";
import type { Project } from "@multica/core/types";
import { useTabStore, type Tab } from "@/stores/tab-store";

const NO_PROJECTS: Project[] = [];

/**
 * Feed each tab's project into the tab store so the strip can group by it
 * (DENE-1775). A chat tab follows its session's project, an issue tab its
 * issue's project, a project page is its own project; every other page has
 * none. A tab whose data has not loaded stays unresolved and keeps whatever
 * project the store last recorded for it.
 *
 * Returns the workspace's projects so the strip can label and badge groups.
 */
export function useTabProjectSync(slug: string | null, tabs: Tab[]): Project[] {
  const wsId = useCurrentWorkspace()?.id ?? "";
  const syncTabProjects = useTabStore((s) => s.syncTabProjects);
  const enabled = !!wsId && !!slug;

  // Keyed on id + url only: the sync itself rewrites tab objects, and
  // re-running on that would feed back into another sync.
  const tabsKey = tabs.map((tab) => `${tab.id} ${tab.url}`).join("\n");
  const subjects = useMemo(
    () =>
      tabsKey
        .split("\n")
        .filter(Boolean)
        .map((line) => {
          const space = line.indexOf(" ");
          return {
            id: line.slice(0, space),
            subject: parseTabSubject(line.slice(space + 1)),
          };
        }),
    [tabsKey],
  );
  const hasChat = subjects.some(
    ({ subject }) => subject.kind === "chat" && !!subject.sessionId,
  );
  const issueIds = useMemo(
    () => [
      ...new Set(
        subjects.flatMap(({ subject }) =>
          subject.kind === "issue" ? [subject.id] : [],
        ),
      ),
    ],
    [subjects],
  );

  const projects =
    useQuery({ ...projectListOptions(wsId), enabled }).data ?? NO_PROJECTS;
  const sessions = useQuery({
    ...chatSessionsOptions(wsId),
    enabled: enabled && hasChat,
  }).data;
  const issues = useQueries({
    queries: issueIds.map((id) => ({
      ...issueDetailOptions(wsId, id),
      enabled,
    })),
  });

  const issueProjectKey = issues
    .map((query, index) =>
      query.data ? `${issueIds[index]}=${query.data.project_id ?? ""}` : "",
    )
    .join("|");

  useEffect(() => {
    if (!enabled || !slug) return;
    const issueProject = new Map<string, string | null>();
    issues.forEach((query, index) => {
      if (query.data) issueProject.set(issueIds[index], query.data.project_id ?? null);
    });
    const resolved: Record<string, string | null> = {};
    for (const { id, subject } of subjects) {
      switch (subject.kind) {
        case "chat": {
          if (!subject.sessionId) {
            resolved[id] = null;
            break;
          }
          const session = sessions?.find((s) => s.id === subject.sessionId);
          if (session) resolved[id] = session.project_id ?? null;
          break;
        }
        case "issue":
          if (issueProject.has(subject.id)) {
            resolved[id] = issueProject.get(subject.id) ?? null;
          }
          break;
        case "project":
          resolved[id] = subject.id;
          break;
        default:
          resolved[id] = null;
      }
    }
    const titles: Record<string, string> = {};
    for (const project of projects) titles[project.id] = project.title;
    syncTabProjects(slug, resolved, titles);
    // `issues` is a fresh array each render; issueProjectKey is its content.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, slug, subjects, sessions, issueProjectKey, projects, syncTabProjects]);

  return projects;
}

/**
 * Drop a leading "<project> · " from a tab title shown inside that project's
 * group — chat titles carry it, and the group chip already says it.
 */
export function stripProjectPrefix(title: string, projectTitle: string | undefined): string {
  const name = projectTitle?.trim();
  if (!name || !title.startsWith(name)) return title;
  const rest = title.slice(name.length);
  const match = /^\s*[·:：|—-]\s*/.exec(rest);
  if (!match) return title;
  const stripped = rest.slice(match[0].length);
  return stripped.trim() ? stripped : title;
}
