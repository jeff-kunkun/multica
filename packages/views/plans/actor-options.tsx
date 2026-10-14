"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { agentListOptions, memberListOptions } from "@multica/core/workspace/queries";
import { useAuthStore } from "@multica/core/auth";
import { cn } from "@multica/ui/lib/utils";
import { ActorAvatar } from "../common/actor-avatar";

export interface ActorRef {
  type: "member" | "agent";
  id: string;
  name: string;
}

/** Active agents first (they do the work), then human colleagues. */
export function useActorOptions(): ActorRef[] {
  const wsId = useWorkspaceId();
  const me = useAuthStore((s) => s.user?.id);
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  return useMemo(
    () => [
      ...agents
        .filter((a) => !a.archived_at)
        .map((a): ActorRef => ({ type: "agent", id: a.id, name: a.name })),
      ...members
        .filter((m) => m.user_id !== me)
        .map((m): ActorRef => ({ type: "member", id: m.user_id, name: m.name })),
    ],
    [agents, members, me],
  );
}

function actorKey(actor: Pick<ActorRef, "type" | "id">) {
  return `${actor.type}:${actor.id}`;
}

export function mentionMarkdown(actor: ActorRef) {
  return `[@${actor.name}](mention://${actor.type}/${actor.id})`;
}

/** Tap-friendly rows for picking people; each row is one actor. */
export function ActorOptionList({
  options,
  selected,
  onToggle,
}: {
  options: ActorRef[];
  selected: ActorRef[];
  onToggle: (actor: ActorRef) => void;
}) {
  const picked = new Set(selected.map(actorKey));
  return (
    <div className="flex max-h-64 flex-col gap-0.5 overflow-y-auto">
      {options.map((actor) => {
        const isPicked = picked.has(actorKey(actor));
        return (
          <button
            key={actorKey(actor)}
            type="button"
            aria-pressed={isPicked}
            onClick={() => onToggle(actor)}
            className={cn(
              "flex min-h-10 items-center gap-2 rounded-md px-2 text-left text-body outline-none transition-colors hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring",
              isPicked && "bg-accent",
            )}
          >
            <ActorAvatar actorType={actor.type} actorId={actor.id} size="sm" profileLink={false} />
            <span className="min-w-0 flex-1 truncate">{actor.name}</span>
            {isPicked && <Check className="size-4 text-muted-foreground" aria-hidden="true" />}
          </button>
        );
      })}
    </div>
  );
}
