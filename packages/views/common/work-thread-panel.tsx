"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "@multica/ui/components/ui/button";
import { Loader2, Pause, Play, ListPlus, ArrowUp } from "lucide-react";
import { api } from "@multica/core/api";
import type { WorkThreadSnapshot } from "@multica/core/types/work_thread";
import { useState } from "react";

interface Props {
  kind: "issue" | "chat";
  id: string;
}

export function WorkThreadPanel({ kind, id }: Props) {
  const queryClient = useQueryClient();
  const [busy, setBusy] = useState(false);
  const queryKey = ["work-thread", kind, id];
  const { data: snapshot } = useQuery<WorkThreadSnapshot | null>({
    queryKey,
    queryFn: () => kind === "issue" ? api.getIssueWorkThread(id) : api.getChatWorkThread(id),
    enabled: !!id,
    refetchInterval: 5_000,
  });
  if (!snapshot) return null;

  const action = async (name: "continue" | "interrupt" | "queue" | "prioritize", taskId?: string) => {
    setBusy(true);
    try {
      if (kind === "issue") await api.issueWorkThreadAction(id, name, name === "queue" ? "Queued from Work Thread controls" : undefined, taskId);
      else if (name !== "prioritize") await api.chatWorkThreadAction(id, name, name === "queue" ? "Queued from Work Thread controls" : undefined);
      await queryClient.invalidateQueries({ queryKey });
    } finally {
      setBusy(false);
    }
  };
  const stateLabel = snapshot.state.replaceAll("_", " ");
  return (
    <div className="flex items-center gap-2 rounded-md border bg-muted/30 px-2 py-1" data-testid={`work-thread-${kind}`}>
      <span className="text-caption text-muted-foreground" title={snapshot.thread_id}>
        Thread · {stateLabel}{snapshot.queued_inputs.length ? ` · ${snapshot.queued_inputs.length} queued` : ""}
      </span>
      {snapshot.can_resume && <Button size="sm" variant="ghost" disabled={busy} onClick={() => action("continue")} aria-label="Continue work thread"><Play className="mr-1 h-3.5 w-3.5" />Continue</Button>}
      {snapshot.current_turn && <Button size="sm" variant="ghost" disabled={busy} onClick={() => action("interrupt")} aria-label="Interrupt work thread"><Pause className="mr-1 h-3.5 w-3.5" />Interrupt</Button>}
      <Button size="sm" variant="ghost" disabled={busy} onClick={() => action("queue")} aria-label="Queue work thread input"><ListPlus className="mr-1 h-3.5 w-3.5" />Queue</Button>
      {kind === "issue" && snapshot.queued_inputs.map((input) => (
        <Button key={input.id} size="sm" variant="ghost" disabled={busy} onClick={() => action("prioritize", input.id)} aria-label={`Prioritize ${input.id}`} title={input.summary}>
          <ArrowUp className="mr-1 h-3.5 w-3.5" />Insert
        </Button>
      ))}
      {busy && <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />}
    </div>
  );
}
