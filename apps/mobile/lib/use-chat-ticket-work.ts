/**
 * Who is on a chat ticket right now — mirrors `useChatTicketWorkLabel` in
 * packages/views/chat/components/chat-ticket-card.tsx: the agents running it,
 * else the ones queued for it; an 「进行中」 ticket with neither says nobody is
 * on it, so the status alone can't claim work nobody is doing.
 */
import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import type { AgentTask, ChatTicket } from "@multica/core/types";
import { agentTaskSnapshotOptions } from "@/data/queries/agent-task-snapshot";
import { agentListOptions } from "@/data/queries/agents";
import { useT } from "@/lib/i18n";

const QUEUED = new Set<AgentTask["status"]>(["queued", "dispatched", "waiting_local_directory", "deferred"]);

export function useChatTicketWorkLabel(wsId: string | null) {
  const { t } = useT("chat");
  const { data: snapshot } = useQuery(agentTaskSnapshotOptions(wsId));
  const { data: agents } = useQuery(agentListOptions(wsId));
  const names = useMemo(() => new Map((agents ?? []).map((a) => [a.id, a.name])), [agents]);
  return (ticket: Pick<ChatTicket, "id" | "status">) => {
    const tasks = (snapshot ?? []).filter((task) => task.issue_id === ticket.id);
    const who = (list: AgentTask[]) =>
      [...new Set(list.map((task) => names.get(task.agent_id)).filter(Boolean))].join("、");
    const running = tasks.filter((task) => task.status === "running");
    if (running.length) {
      const name = who(running);
      return name ? t("tickets.working", { name }) : t("tickets.working_unnamed");
    }
    const queued = tasks.filter((task) => QUEUED.has(task.status));
    if (queued.length) {
      const name = who(queued);
      return name ? t("tickets.queued", { name }) : t("tickets.queued_unnamed");
    }
    return snapshot && ticket.status === "in_progress" ? t("tickets.idle") : "";
  };
}
