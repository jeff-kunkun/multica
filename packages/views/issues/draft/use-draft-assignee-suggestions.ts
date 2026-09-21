"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  buildIssueDraftAssigneeRoster,
  suggestIssueDraftAssignees,
  type IssueDraftAssigneeSuggestions,
} from "@multica/core/issue-drafts";
import { agentListOptions, squadListOptions } from "@multica/core/workspace/queries";
import type { IssueDraftPayload } from "@multica/core/types";

const EMPTY_SUGGESTIONS: IssueDraftAssigneeSuggestions = {
  parent: null,
  children: {},
};

/**
 * Suggested assignees for the preview panel, derived from the workspace
 * roster's existing tags. Loading is not a gate: the confirm button stays
 * available and treats empty slots as unassigned until this resolves.
 */
export function useDraftAssigneeSuggestions(
  payload: IssueDraftPayload | null,
  options?: { enabled?: boolean },
): {
  suggestions: IssueDraftAssigneeSuggestions;
  loading: boolean;
} {
  const wsId = useWorkspaceId();
  const enabled = options?.enabled !== false && Boolean(wsId);
  const agentsQuery = useQuery({
    ...agentListOptions(wsId),
    enabled,
  });
  const squadsQuery = useQuery({
    ...squadListOptions(wsId),
    enabled,
  });

  const loading =
    enabled && (agentsQuery.isLoading || squadsQuery.isLoading);

  const suggestions = useMemo(() => {
    if (!payload || !agentsQuery.data) return EMPTY_SUGGESTIONS;
    // Official cloud omits `members`; that is still a roster — seats carry
    // routing_tier and names. A failed squad list is the unavailable case.
    if (squadsQuery.isError && !squadsQuery.data) return EMPTY_SUGGESTIONS;
    const roster = buildIssueDraftAssigneeRoster({
      agents: agentsQuery.data,
      squads: squadsQuery.data ?? [],
    });
    return suggestIssueDraftAssignees(payload, roster);
  }, [agentsQuery.data, payload, squadsQuery.data, squadsQuery.isError]);

  return { suggestions, loading };
}
