"use client";

import { useQuery } from "@tanstack/react-query";
import { MessagesSquare } from "lucide-react";
import { chatSessionOptions } from "@multica/core/chat/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { Tooltip, TooltipContent, TooltipTrigger } from "@multica/ui/components/ui/tooltip";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";

/**
 * "From chat" in the issue header (DENE-1672): the chat this task was
 * dispatched from, where its result is posted back. The chat's title shows
 * only to someone who can open it; anyone else still sees where it came from.
 */
export function IssueSourceChatLink({ sessionId }: { sessionId: string }) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const { data: session } = useQuery({ ...chatSessionOptions(wsId, sessionId), retry: false });
  const title = session?.title?.trim();
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <AppLink
            href={paths.chatSession(sessionId)}
            className="flex h-7 min-w-0 shrink-0 items-center gap-1 rounded-md px-2 text-xs text-muted-foreground hover:bg-accent hover:text-foreground"
          >
            <MessagesSquare className="size-3.5 shrink-0" />
            <span className="whitespace-nowrap">{t(($) => $.detail.source_chat)}</span>
          </AppLink>
        }
      />
      <TooltipContent side="bottom">
        {title ? t(($) => $.detail.source_chat_tooltip, { title }) : t(($) => $.detail.source_chat_tooltip_untitled)}
      </TooltipContent>
    </Tooltip>
  );
}
