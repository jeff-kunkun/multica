"use client";

import { useWorkspacePaths } from "@multica/core/paths";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";

export function ConvertedChatNotice({ issueId }: { issueId: string }) {
  const { t } = useT("chat");
  const paths = useWorkspacePaths();
  return (
    <div className="flex shrink-0 flex-wrap items-center justify-between gap-x-3 gap-y-1 px-4 py-3 text-caption">
      <span className="text-muted-foreground">{t(($) => $.input.converted_to_issue)}</span>
      <AppLink href={paths.issueDetail(issueId)} className="inline-flex min-h-11 items-center text-primary underline underline-offset-4">
        {t(($) => $.input.open_converted_issue)}
      </AppLink>
    </div>
  );
}
