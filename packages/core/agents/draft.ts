import { isRuntimeUsableForUser } from "../runtimes/access";
import type {
  Agent,
  AgentInvocationTargetInput,
  AgentPermissionScope,
  AgentConversationStarter,
  CreateAgentRequest,
  RuntimeDevice,
} from "../types";
import {
  AGENT_DESCRIPTION_MAX_LENGTH,
  AGENT_MAX_CONCURRENT_TASKS_MAX,
  AGENT_MAX_CONCURRENT_TASKS_MIN,
} from "./constants";

// Declared with the other agent wire types so `StoredAgentDraft` can name it
// without types/ having to import from agents/.
export type { AgentPermissionScope };

/**
 * The in-progress configuration of an agent being created. Shared by every
 * creation surface (manual, duplicate) so the same field set is
 * validated, seeded and submitted the same way regardless of entry point.
 */
export interface AgentDraft {
  name: string;
  description: string;
  instructions: string;
  conversationStarters: AgentConversationStarter[];
  avatarUrl: string | null;
  runtimeId: string;
  permissionScope: AgentPermissionScope;
  memberIds: Set<string>;
  /** Team grants are not editable in this form yet, but duplicates must preserve them. */
  teamIds: Set<string>;
}

export const EMPTY_AGENT_DRAFT: AgentDraft = {
  name: "",
  description: "",
  instructions: "",
  conversationStarters: [],
  avatarUrl: null,
  runtimeId: "",
  permissionScope: "private",
  memberIds: new Set(),
  teamIds: new Set(),
};

/** Runtime change: the only execution choice the platform owns. */
export function applyDraftRuntimeChange(
  draft: AgentDraft,
  runtimeId: string,
): AgentDraft {
  return { ...draft, runtimeId };
}

/**
 * Mirrors the server's `utf8.RuneCountInString` check (agent.go:1013). The
 * textarea's `maxLength` covers typing and pasting, but a duplicated agent can seed a longer value programmatically — without this
 * the create button would submit into a guaranteed 400.
 */
export function isDraftDescriptionWithinLimit(description: string): boolean {
  return [...description].length <= AGENT_DESCRIPTION_MAX_LENGTH;
}

export function buildInvocationTargets(
  draft: AgentDraft,
): AgentInvocationTargetInput[] {
  if (draft.permissionScope === "private") return [];
  if (draft.permissionScope === "workspace") {
    return [{ target_type: "workspace" }];
  }
  return [
    ...[...draft.memberIds].map((targetId) => ({
      target_type: "member" as const,
      target_id: targetId,
    })),
    ...[...draft.teamIds].map((targetId) => ({
      target_type: "team" as const,
      target_id: targetId,
    })),
  ];
}

export function deriveDuplicateAccess(
  agent: Pick<Agent, "permission_mode" | "invocation_targets">,
): Pick<AgentDraft, "permissionScope" | "memberIds" | "teamIds"> {
  if (agent.permission_mode !== "public_to") {
    return {
      permissionScope: "private",
      memberIds: new Set(),
      teamIds: new Set(),
    };
  }

  const targets = agent.invocation_targets ?? [];
  if (targets.some((target) => target.target_type === "workspace")) {
    return {
      permissionScope: "workspace",
      memberIds: new Set(),
      teamIds: new Set(),
    };
  }

  const memberIds = targets
    .filter((target) => target.target_type === "member" && target.target_id)
    .map((target) => target.target_id as string);
  const teamIds = targets
    .filter((target) => target.target_type === "team" && target.target_id)
    .map((target) => target.target_id as string);
  if (memberIds.length === 0 && teamIds.length === 0) {
    return {
      permissionScope: "private",
      memberIds: new Set(),
      teamIds: new Set(),
    };
  }
  return {
    permissionScope: "members",
    memberIds: new Set(memberIds),
    teamIds: new Set(teamIds),
  };
}

/**
 * Seeds the create draft from the agent being duplicated.
 *
 * The copy keeps the source runtime only when this user can still use it;
 * otherwise it falls back to `fallbackRuntimeId`.
 */
export function buildDuplicateDraft(
  source: Agent,
  options: {
    runtimes: RuntimeDevice[];
    currentUserId: string | null;
    fallbackRuntimeId: string;
    nameSuffix: string;
  },
): AgentDraft {
  const keepsRuntime =
    !!source.runtime_id &&
    options.runtimes.some(
      (runtime) =>
        runtime.id === source.runtime_id &&
        isRuntimeUsableForUser(runtime, options.currentUserId),
    );
  return {
    ...EMPTY_AGENT_DRAFT,
    name: `${source.name}${options.nameSuffix}`,
    description: source.description ?? "",
    instructions: source.instructions ?? "",
    conversationStarters: (source.conversation_starters ?? []).map((item) => ({ ...item })),
    avatarUrl: source.avatar_url ?? null,
    runtimeId: keepsRuntime
      ? (source.runtime_id as string)
      : options.fallbackRuntimeId,
    ...deriveDuplicateAccess(source),
  };
}

/**
 * Assembles the `POST /api/agents` body. The duplicate-only fields are the runtime-independent ones (`custom_args`,
 * concurrency) — mirroring `multica agent copy`.
 */
export function buildCreateAgentRequest(options: {
  draft: AgentDraft;
  runtimeId: string;
  /** Creation-source attribution for the `agent_created` analytics event. */
  template?: string;
  duplicateSource?: Agent | null;
}): CreateAgentRequest {
  const { draft, runtimeId, template, duplicateSource } = options;
  const request: CreateAgentRequest = {
    name: draft.name.trim(),
    description: draft.description.trim(),
    instructions: draft.instructions.trim() || undefined,
    ...(draft.conversationStarters.length > 0
      ? {
          conversation_starters: draft.conversationStarters.map((item) => ({
            label: item.label.trim(),
            prompt: item.prompt.trim(),
          })),
        }
      : {}),
    avatar_url: draft.avatarUrl ?? undefined,
    runtime_id: runtimeId,
    permission_mode:
      draft.permissionScope === "private" ? "private" : "public_to",
    invocation_targets: buildInvocationTargets(draft),
    template,
  };
  if (duplicateSource) {
    if (duplicateSource.custom_args.length > 0) {
      request.custom_args = duplicateSource.custom_args;
    }
    const sourceConcurrency = duplicateSource.max_concurrent_tasks;
    if (
      Number.isInteger(sourceConcurrency) &&
      sourceConcurrency >= AGENT_MAX_CONCURRENT_TASKS_MIN &&
      sourceConcurrency <= AGENT_MAX_CONCURRENT_TASKS_MAX
    ) {
      request.max_concurrent_tasks = sourceConcurrency;
    }
  }
  return request;
}
