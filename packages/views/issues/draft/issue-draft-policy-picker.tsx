"use client";

import {
  ISSUE_DRAFT_SKILLS,
  readIssueDraftSkills,
  type IssueDraftSkillKey,
} from "@multica/core/issue-drafts";
import type { IssueDraftPolicy } from "@multica/core/types";
import { DropdownMenuSeparator } from "@multica/ui/components/ui/dropdown-menu";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

/**
 * The label and one-line description each skill carries, as message keys.
 *
 * A record rather than a ternary on the key: `ISSUE_DRAFT_SKILLS` is the
 * whitelist this toggle renders, so a key added there without copy here is a
 * compile error — not a checkbox that quietly reads as the requirement
 * interview, and not a menu that has to be edited in three places to grow a
 * fourth.
 */
const SKILL_COPY = {
  grill: { label: "skill_grill", description: "skill_grill_description" },
  wayfinder: {
    label: "skill_wayfinder",
    description: "skill_wayfinder_description",
  },
  frontend: { label: "skill_frontend", description: "skill_frontend_description" },
} as const satisfies Record<IssueDraftSkillKey, { label: string; description: string }>;

/**
 * Which alignment skills the conversation runs: the requirement interview, the
 * decision map, and the look round that settles a screen by building something
 * the user can open. Any combination, including one.
 *
 * Checkboxes rather than the three named radio options this replaced (DENE-512).
 * The three are not alternatives — a request can need its route settled AND its
 * screen seen AND its wording converged, and the carrier's prompt is composed
 * from whichever are on. The server refuses an empty set, so the last checked
 * box cannot be turned off: the control says why instead of accepting a click
 * that 400s.
 *
 * It is menu content, not a control strip: it lives in the alignment header's ⋯
 * menu, so it renders menu-shaped rows and writes out the label it used to
 * inherit from the header it sat in.
 *
 * Renders nothing when the server reports no skill record at all. An installed
 * desktop client can talk to a backend that predates skills, and offering a
 * toggle that cannot land is worse than offering none.
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
  onChange: (skills: IssueDraftSkillKey[]) => void;
}) {
  const { t } = useT("issues");
  if (!policy.key) return null;

  const enabled = readIssueDraftSkills(policy);
  const locked = disabled || switching;
  // A key the client cannot name is still a skill this conversation runs, and
  // hiding it would misreport the set. It is shown as an inert row instead, so
  // what the user reads matches what the carrier was told.
  const unknown = (policy.skills ?? [])
    .map((skill) => skill.key)
    .filter((key) => !(ISSUE_DRAFT_SKILLS as readonly string[]).includes(key));

  const toggle = (key: IssueDraftSkillKey, next: boolean) => {
    const set = new Set(enabled);
    if (next) set.add(key);
    else set.delete(key);
    // The last one cannot be turned off; the row explains why rather than the
    // click being swallowed.
    if (set.size === 0) return;
    onChange(ISSUE_DRAFT_SKILLS.filter((candidate) => set.has(candidate)));
  };

  return (
    <>
      <span className="px-1.5 py-1 text-caption font-medium text-muted-foreground">
        {t(($) => $.alignment.policy_label)}
      </span>
      <div role="group" aria-label={t(($) => $.alignment.policy_label)}>
        {ISSUE_DRAFT_SKILLS.map((key) => {
          const checked = enabled.includes(key);
          const lastOne = checked && enabled.length === 1;
          return (
            <button
              key={key}
              type="button"
              role="menuitemcheckbox"
              aria-checked={checked}
              disabled={locked || lastOne}
              title={
                lastOne
                  ? t(($) => $.alignment.skill_last_one_hint)
                  : undefined
              }
              onClick={() => toggle(key, !checked)}
              className={cn(
                "flex w-full items-start gap-2 rounded-sm px-2 py-1.5 text-left text-caption",
                "transition-colors hover:bg-accent/50 focus-visible:bg-accent/50 focus-visible:outline-none",
                "disabled:cursor-not-allowed disabled:opacity-60",
                checked ? "text-foreground" : "text-muted-foreground",
              )}
            >
              {/* A box, not a tick: what the user is choosing is a SET, and a
                  ticked line reads as "this is the one". */}
              <span
                aria-hidden="true"
                className={cn(
                  "mt-0.5 flex size-3.5 shrink-0 items-center justify-center rounded-[4px] border",
                  checked
                    ? "border-foreground bg-foreground text-background"
                    : "border-muted-foreground/50",
                )}
              >
                {checked ? (
                  <svg viewBox="0 0 12 12" className="size-2.5" fill="none">
                    <path
                      d="M2.5 6.2 5 8.5l4.5-5"
                      stroke="currentColor"
                      strokeWidth="1.8"
                      strokeLinecap="round"
                      strokeLinejoin="round"
                    />
                  </svg>
                ) : null}
              </span>
              <span className="min-w-0 flex-1">
                <span className={cn("block", checked && "font-medium")}>
                  {t(($) => $.alignment[SKILL_COPY[key].label])}
                </span>
                <span className="mt-0.5 block text-caption leading-snug text-muted-foreground">
                  {t(($) => $.alignment[SKILL_COPY[key].description])}
                </span>
              </span>
            </button>
          );
        })}
        {unknown.map((key) => (
          <div
            key={key}
            role="menuitemcheckbox"
            aria-checked="true"
            aria-disabled="true"
            className="flex w-full items-start gap-2 px-2 py-1.5 text-caption text-muted-foreground"
          >
            <span
              aria-hidden="true"
              className="mt-0.5 size-3.5 shrink-0 rounded-[4px] border border-muted-foreground/50"
            />
            <span className="min-w-0 flex-1">
              <span className="mono block">{key}</span>
              <span className="mt-0.5 block text-caption leading-snug">
                {t(($) => $.alignment.skill_unknown)}
              </span>
            </span>
          </div>
        ))}
      </div>
      <DropdownMenuSeparator />
      <span
        className="block max-w-full truncate px-1.5 py-1 text-caption text-muted-foreground"
        title={t(($) => $.alignment.policy_version_hint)}
      >
        {t(($) => $.alignment.policy_version, {
          version: `${policy.key}@${policy.version}`,
        })}
      </span>
    </>
  );
}
