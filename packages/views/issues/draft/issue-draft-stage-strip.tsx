"use client";

import { Check } from "lucide-react";
import {
  ISSUE_DRAFT_STAGES,
  issueDraftStageIndex,
  type IssueDraftStage,
} from "@multica/core/issue-drafts";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

/**
 * Where this alignment has got to: 对齐中 → 待确认 → 创建中 → 已创建.
 *
 * Shown as one strip rather than as a status word, because the question a
 * person about to create an issue is asking is "what is still missing", and
 * that is a position in a sequence, not a label.
 */
export function IssueDraftStageStrip({
  stage,
  hasTitle,
}: {
  stage: IssueDraftStage;
  hasTitle: boolean;
}) {
  const { t } = useT("issues");
  const current = issueDraftStageIndex(stage);
  const labels: Record<IssueDraftStage, string> = {
    aligning: t(($) => $.alignment.stage_aligning),
    ready: t(($) => $.alignment.stage_ready),
    creating: t(($) => $.alignment.stage_creating),
    created: t(($) => $.alignment.stage_created),
  };
  const hints: Record<IssueDraftStage, string> = {
    aligning: t(($) => $.alignment.stage_hint_aligning),
    ready: hasTitle
      ? t(($) => $.alignment.stage_hint_ready)
      : t(($) => $.alignment.stage_hint_aligning),
    creating: t(($) => $.alignment.stage_hint_creating),
    created: t(($) => $.alignment.stage_hint_created),
  };

  return (
    <div className="border-b px-5 py-3">
      <ol className="flex flex-wrap items-center gap-x-2 gap-y-1">
        {ISSUE_DRAFT_STAGES.map((entry, index) => {
          const done = index < current;
          const active = index === current;
          return (
            <li key={entry} className="flex items-center gap-2">
              {index > 0 ? (
                <span
                  aria-hidden="true"
                  className={cn(
                    "h-px w-5",
                    done || active ? "bg-primary/50" : "bg-border",
                  )}
                />
              ) : null}
              <span
                aria-current={active ? "step" : undefined}
                className={cn(
                  "flex items-center gap-1.5 rounded-full px-2.5 py-1 text-caption",
                  active && "bg-primary/10 font-medium text-primary",
                  !active && done && "text-muted-foreground",
                  !active && !done && "text-faint-foreground",
                )}
              >
                {done ? (
                  <Check className="size-3" aria-hidden="true" />
                ) : (
                  <span
                    aria-hidden="true"
                    className={cn(
                      "size-1.5 rounded-full",
                      active ? "bg-primary" : "bg-muted-foreground/40",
                    )}
                  />
                )}
                {labels[entry]}
              </span>
            </li>
          );
        })}
      </ol>
      <p className="mt-2 text-caption text-muted-foreground" aria-live="polite">
        {hints[stage]}
      </p>
    </div>
  );
}
