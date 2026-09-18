"use client";

import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { useChatStore } from "@multica/core/chat";
import { planFollowedProjectIds } from "@multica/core/chat/project-follow";
import { issueDetailOptions } from "@multica/core/issues/queries";
import { projectDetailOptions } from "@multica/core/projects/queries";
import { useNavigation } from "../../navigation";
import { parseCurrentContextRoute } from "./use-chat-context-items";

/**
 * The project the main UI is currently showing: the project page itself, or
 * the project an open issue belongs to. Null anywhere else.
 *
 * Resolved through the same detail queries the chat's @-context list uses, so
 * the two agree on what "current" means and share one cache entry.
 */
export function useCurrentRouteProjectId(wsId: string): string | null {
  const { pathname, searchParams } = useNavigation();
  const currentRoute = parseCurrentContextRoute(pathname, searchParams);

  const { data: currentIssue } = useQuery({
    ...issueDetailOptions(wsId, currentRoute?.type === "issue" ? currentRoute.id : ""),
    enabled: currentRoute?.type === "issue",
  });
  const { data: currentProject } = useQuery({
    ...projectDetailOptions(wsId, currentRoute?.type === "project" ? currentRoute.id : ""),
    enabled: currentRoute?.type === "project",
  });

  if (currentRoute?.type === "project") return currentProject?.id ?? null;
  // The route id may be an identifier ("MUL-12"), so the project comes from the
  // fetched issue rather than from the URL.
  if (currentRoute?.type === "issue") return currentIssue?.project_id ?? null;
  return null;
}

/**
 * Keep the floating chat's draft project set bound to the project the user is
 * looking at (DENE-603 §1), until they pick one themselves (§4).
 *
 * Only runs while the window is open: a closed window is holding a persisted
 * draft set that nobody is looking at, and rewriting it from the route would
 * change a chat the user never opened.
 */
export function useChatProjectFollow(params: {
  routeProjectId: string | null;
  isOpen: boolean;
  hasSession: boolean;
  projectsLoaded: boolean;
}) {
  const { routeProjectId, isOpen, hasSession, projectsLoaded } = params;
  const selectedProjectIds = useChatStore((s) => s.selectedProjectIds);
  const locked = useChatStore((s) => s.projectContextLocked);
  const setSelectedProjectIds = useChatStore((s) => s.setSelectedProjectIds);

  useEffect(() => {
    if (!isOpen) return;
    const next = planFollowedProjectIds({
      routeProjectId,
      selectedProjectIds,
      locked,
      hasSession,
      projectsLoaded,
    });
    if (next) setSelectedProjectIds(next);
  }, [
    isOpen,
    routeProjectId,
    selectedProjectIds,
    locked,
    hasSession,
    projectsLoaded,
    setSelectedProjectIds,
  ]);
}
