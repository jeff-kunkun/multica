"use client";

import type { ComponentProps } from "react";
import { useQuery } from "@tanstack/react-query";
import { ActorAvatar as ActorAvatarBase } from "@multica/ui/components/common/actor-avatar";
import { useCurrentWorkspace } from "@multica/core/paths";
import { agentListOptions } from "@multica/core/workspace/queries";
import { runtimeListOptions } from "@multica/core/runtimes";
import { ProviderLogo } from "../../runtimes/components/provider-logo";

/** The CLI an agent runs on, as shown in the agents list. */
export function useAgentCliProvider(agentId: string): string | null {
  const wsId = useCurrentWorkspace()?.id ?? "";
  const enabled = !!wsId && !!agentId;
  const { data: agents } = useQuery({ ...agentListOptions(wsId), enabled });
  const { data: runtimes } = useQuery({ ...runtimeListOptions(wsId), enabled });
  const runtimeId = agents?.find((a) => a.id === agentId)?.runtime_id;
  if (!runtimeId) return null;
  return runtimes?.find((r) => r.id === runtimeId)?.provider ?? null;
}

/** Base avatar for an agent, wearing its CLI's official logo. */
export function AgentCliAvatar({
  agentId,
  ...props
}: { agentId: string } & Omit<ComponentProps<typeof ActorAvatarBase>, "icon">) {
  const provider = useAgentCliProvider(agentId);
  return (
    <ActorAvatarBase
      {...props}
      icon={provider ? <ProviderLogo provider={provider} className="h-full w-full" /> : undefined}
    />
  );
}
