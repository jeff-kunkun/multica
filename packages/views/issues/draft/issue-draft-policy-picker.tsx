"use client";

import {
  ISSUE_DRAFT_POLICIES,
  type IssueDraftPolicyKey,
} from "@multica/core/issue-drafts";
import type { IssueDraftPolicy } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

/**
 * Which alignment policy the conversation runs under: the guided one that asks
 * one question at a time, or plain dialogue.
 *
 * Two buttons rather than a switch, because these are two named behaviours and
 * not an on/off setting — "关闭引导" tells the user what they lose, "普通对话"
 * tells them what they get. The version caption next to them is the audit half:
 * it names the prompt the carrier was actually given, which is the answer to
 * "why did the alignment ask that?" a week later.
 *
 * Renders nothing when the server reports no policy at all. An installed
 * desktop client can talk to a backend that predates policies, and offering a
 * switch that cannot land is worse than offering none.
 */
export function IssueDraftPolicyPicker({
  policy,
  switching,
  disabled,
  onChange,
}: {
  policy: IssueDraftPolicy;
  /** A switch is in flight: the picker must show the state the server has, not
   *  the one the user clicked. */
  switching: boolean;
  disabled: boolean;
  onChange: (policy: IssueDraftPolicyKey) => void;
}) {
  const { t } = useT("issues");
  if (!policy.key) return null;

  return (
    <div className="flex items-center gap-1.5">
      <div
        role="group"
        aria-label={t(($) => $.alignment.policy_label)}
        className="flex items-center gap-0.5 rounded-lg border p-0.5"
      >
        {ISSUE_DRAFT_POLICIES.map((key) => {
          const active = policy.key === key;
          return (
            <Button
              key={key}
              type="button"
              variant="ghost"
              size="sm"
              aria-pressed={active}
              // The selected state lives on weight and text colour, and its
              // hover is written out: a plain `hover:bg-muted` would make the
              // active option look exactly like the inactive one under the
              // cursor.
              data-active={active ? "true" : undefined}
              className={cn(
                "text-muted-foreground",
                "data-[active=true]:bg-muted data-[active=true]:font-medium data-[active=true]:text-foreground",
                "data-[active=true]:hover:bg-muted data-[active=true]:hover:text-foreground",
              )}
              disabled={disabled || switching || active}
              onClick={() => onChange(key)}
            >
              {key === "question"
                ? t(($) => $.alignment.policy_guided)
                : t(($) => $.alignment.policy_plain)}
            </Button>
          );
        })}
      </div>
      <span
        className="truncate text-caption text-muted-foreground"
        title={t(($) => $.alignment.policy_version_hint)}
      >
        {t(($) => $.alignment.policy_version, {
          version: `${policy.key}@${policy.version}`,
        })}
      </span>
    </div>
  );
}
