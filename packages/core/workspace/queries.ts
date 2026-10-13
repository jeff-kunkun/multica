import { queryOptions, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { Agent, Workspace } from "../types";

export const workspaceKeys = {
  all: (wsId: string) => ["workspaces", wsId] as const,
  list: () => ["workspaces", "list"] as const,
  members: (wsId: string) => ["workspaces", wsId, "members"] as const,
  invitations: (wsId: string) => ["workspaces", wsId, "invitations"] as const,
  shareLinks: (wsId: string) => ["workspaces", wsId, "share-links"] as const,
  myInvitations: () => ["invitations", "mine"] as const,
  agents: (wsId: string) => ["workspaces", wsId, "agents"] as const,
  agent: (wsId: string, agentId: string) =>
    ["workspaces", wsId, "agents", "detail", agentId] as const,
  assigneeFrequency: (wsId: string) => ["workspaces", wsId, "assignee-frequency"] as const,
  mcpServers: (wsId: string) => ["workspaces", wsId, "mcp-servers"] as const,
  bots: (wsId: string) => ["workspaces", wsId, "bots"] as const,
};

export function workspaceListOptions() {
  return queryOptions({
    queryKey: workspaceKeys.list(),
    queryFn: () => api.listWorkspaces(),
  });
}

/** Resolves the workspace whose slug matches, from the cached workspace list. */
export function workspaceBySlugOptions(slug: string) {
  return queryOptions({
    ...workspaceListOptions(),
    select: (list: Workspace[]) => list.find((w) => w.slug === slug) ?? null,
  });
}

export function memberListOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceKeys.members(wsId),
    queryFn: () => api.listMembers(wsId),
  });
}

export function agentListOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceKeys.agents(wsId),
    queryFn: () =>
      api.listAgents({ workspace_id: wsId, include_archived: true }),
    // Projected unstable can age offline without an event; polling online also
    // covers a missed demotion. Offline recovery is event/reconnect-driven.
    refetchInterval: (query) =>
      query.state.data?.some(
        (agent) =>
          !agent.archived_at &&
          (agent.runtime_availability === "online" ||
            agent.runtime_availability === "unstable"),
      )
        ? 30_000
        : false,
  });
}

export function agentDetailOptions(wsId: string, agentId: string) {
  return queryOptions({
    queryKey: workspaceKeys.agent(wsId, agentId),
    queryFn: () => api.getAgent(agentId),
    enabled: !!wsId && !!agentId,
    retry: false,
  });
}

/**
 * Makes an authoritative agent response immediately visible to both agent
 * detail and list consumers. A cold list stays cold so one agent is never
 * mistaken for the complete workspace list.
 */
export function cacheAgentResponse(
  queryClient: QueryClient,
  wsId: string,
  agent: Agent,
  options: { insertIntoList?: boolean } = {},
) {
  queryClient.setQueryData(workspaceKeys.agent(wsId, agent.id), agent);
  queryClient.setQueryData<Agent[]>(workspaceKeys.agents(wsId), (current) => {
    if (!current) return current;
    const existingIndex = current.findIndex((item) => item.id === agent.id);
    if (existingIndex === -1) {
      return options.insertIntoList === false ? current : [...current, agent];
    }
    return current.map((item, index) =>
      index === existingIndex ? agent : item,
    );
  });
}

export function invitationListOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceKeys.invitations(wsId),
    queryFn: () => api.listWorkspaceInvitations(wsId),
  });
}

export function shareLinkListOptions(wsId: string, enabled = true) {
  return queryOptions({
    queryKey: workspaceKeys.shareLinks(wsId),
    queryFn: () => api.listShareLinks(wsId),
    enabled: enabled && !!wsId,
  });
}

export function myInvitationListOptions() {
  return queryOptions({
    queryKey: workspaceKeys.myInvitations(),
    queryFn: () => api.listMyInvitations(),
  });
}

export function assigneeFrequencyOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceKeys.assigneeFrequency(wsId),
    queryFn: () => api.getAssigneeFrequency(),
  });
}

/**
 * The workspace's MCP server library. Fetched for plain members too — the
 * payload is names and transports only, and an agent owner needs to see what
 * is available to assign.
 */
export function workspaceMcpServersOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceKeys.mcpServers(wsId),
    queryFn: () => api.listWorkspaceMcpServers(wsId),
    enabled: wsId !== "",
  });
}

/** The workspace MCP servers assigned to one agent, with their toggles. */
export function agentMcpServersOptions(agentId: string) {
  return queryOptions({
    queryKey: ["agents", agentId, "mcp-servers"] as const,
    queryFn: () => api.listAgentMcpServers(agentId),
    enabled: agentId !== "",
  });
}

/** Project-manager Bots. The API is owner-only; pass enabled=false otherwise. */
export function workspaceBotsOptions(wsId: string, enabled = true) {
  return queryOptions({
    queryKey: workspaceKeys.bots(wsId),
    queryFn: () => api.listBots(),
    enabled: enabled && wsId !== "",
  });
}
