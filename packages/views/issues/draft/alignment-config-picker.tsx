"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Brain,
  Check,
  ChevronDown,
  Cpu,
  Loader2,
  Monitor,
  Sparkles,
} from "lucide-react";
import {
  ISSUE_DRAFT_SKILLS,
  type IssueDraftSkillKey,
} from "@multica/core/issue-drafts";
import {
  isRuntimeUsableForUser,
  runtimeDisplayName,
  runtimeModelsOptions,
} from "@multica/core/runtimes";
import type { MemberWithUser, RuntimeDevice } from "@multica/core/types";
import type { TFunction } from "i18next";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import { ProviderLogo } from "../../runtimes/components/provider-logo";

/**
 * The whole configuration of an alignment conversation, in one panel: which
 * machine runs it, which model that machine's CLI is asked for, how hard it is
 * asked to think, and which alignment skills the carrier runs.
 *
 * One panel rather than four adjacent pills (DENE-512). The first three are not
 * independent — the model list belongs to the machine, and the reasoning levels
 * belong to the model — so choosing them in one place is what makes the
 * dependency visible instead of leaving the user to discover it by picking a
 * model that then has no levels. The skills are in the same panel because they
 * are the same decision: "what kind of alignment do I want" and "what runs it"
 * are answered together at the moment the conversation starts.
 *
 * It is the create-time face of the same choices the alignment page exposes
 * afterwards. The machine and the skill set can be changed later (the runtime
 * switch and the skill toggle in the page's ⋯ menu); the model and the
 * reasoning level cannot, because the daemon reads them off the carrier's agent
 * row, which is written once when the session is created.
 *
 * The trigger is a pill, because it sits on the dialog's toolbar beside the
 * project pill — and it summarises all four choices, because a pill that only
 * said "Configuration" would hide the one thing worth knowing at a glance:
 * whether this conversation is about to interview you or draw you a screen.
 */
export function AlignmentConfigPicker({
  runtimes,
  runtimesLoading,
  members,
  currentUserId,
  runtimeId,
  onRuntimeChange,
  model,
  onModelChange,
  thinkingLevel,
  onThinkingLevelChange,
  skills,
  onSkillsChange,
  disabled,
}: {
  runtimes: RuntimeDevice[];
  runtimesLoading?: boolean;
  members: MemberWithUser[];
  currentUserId: string | null;
  runtimeId: string;
  onRuntimeChange: (runtimeId: string) => void;
  /** Empty means "the CLI's own default", which is a legitimate choice. */
  model: string;
  onModelChange: (model: string) => void;
  /** Empty means "follow the local CLI config". */
  thinkingLevel: string;
  onThinkingLevelChange: (level: string) => void;
  /** The alignment skills to switch on, in registry order. Read-only because
   *  the default set is a frozen constant; every change goes through
   *  `onSkillsChange` with a fresh array. */
  skills: readonly IssueDraftSkillKey[];
  onSkillsChange: (skills: readonly IssueDraftSkillKey[]) => void;
  disabled?: boolean;
}) {
  const { t } = useT("issues");
  const { t: tAgents } = useT("agents");
  const [open, setOpen] = useState(false);

  const usableRuntimes = useMemo(
    () => runtimes.filter((runtime) => isRuntimeUsableForUser(runtime, currentUserId)),
    [runtimes, currentUserId],
  );
  const selectedRuntime =
    usableRuntimes.find((runtime) => runtime.id === runtimeId) ?? null;
  const runtimeOnline = selectedRuntime?.status === "online";

  const modelsQuery = useQuery(
    runtimeModelsOptions(runtimeOnline ? runtimeId : null),
  );
  const models = useMemo(() => modelsQuery.data?.models ?? [], [modelsQuery.data]);
  const selectedModel = models.find((candidate) => candidate.id === model) ?? null;
  // The catalog is per model, and an unsupported model is a level the daemon
  // would drop. So the levels come from the CHOSEN model, not from the runtime —
  // which is exactly why the three controls share a panel.
  const levels = selectedModel?.thinking?.supported_levels ?? [];
  const selectedLevel = levels.find((level) => level.value === thinkingLevel) ?? null;

  const label = alignmentConfigSummary({
    runtime: selectedRuntime,
    model,
    thinkingLevel,
    thinkingLabel: selectedLevel?.label ?? "",
    skills,
    t,
    tAgents,
  });

  const toggleSkill = (key: IssueDraftSkillKey, next: boolean) => {
    const set = new Set<IssueDraftSkillKey>(skills);
    if (next) set.add(key);
    else set.delete(key);
    // The server refuses an empty set, so the control does not offer one: the
    // row explains the last checkbox rather than letting a click 400.
    if (set.size === 0) return;
    onSkillsChange(ISSUE_DRAFT_SKILLS.filter((candidate) => set.has(candidate)));
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        disabled={disabled}
        className={cn(
          "inline-flex h-7 max-w-[16rem] min-w-0 items-center gap-1.5 rounded-full border border-border bg-background px-2.5 text-caption",
          "hover:bg-accent/50 disabled:cursor-not-allowed disabled:opacity-50",
          open && "border-ring bg-accent/50",
        )}
        aria-label={t(($) => $.alignment.config_aria)}
        title={label}
      >
        <Monitor className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="truncate">{label}</span>
        <ChevronDown
          className={cn("size-3 shrink-0 text-muted-foreground transition-transform", open && "rotate-180")}
          aria-hidden="true"
        />
      </PopoverTrigger>
      {/* `w-80` and `align="start"`: the panel is anchored to a pill at the left
          end of a 576px dialog's toolbar, so it opens rightward and stays inside
          the dialog at both breakpoints. */}
      <PopoverContent align="start" side="top" className="w-80 p-0">
        {/* Runtime */}
        <Section
          icon={<Monitor className="size-3.5" aria-hidden="true" />}
          title={t(($) => $.alignment.config_runtime)}
          hint={
            runtimesLoading
              ? t(($) => $.alignment.config_runtime_loading)
              : t(($) => $.alignment.config_runtime_hint, {
                  count: usableRuntimes.filter((r) => r.status === "online").length,
                })
          }
        >
          {usableRuntimes.length === 0 && !runtimesLoading ? (
            <p className="px-1 py-2 text-caption text-muted-foreground">
              {t(($) => $.alignment.entry_no_runtime)}
            </p>
          ) : (
            usableRuntimes.map((runtime) => (
              <Row
                key={runtime.id}
                selected={runtime.id === runtimeId}
                onClick={() => {
                  if (runtime.id === runtimeId) return;
                  // Model ids are per runtime and levels are per model, so both
                  // reset. Leaving them would show a model this machine may not
                  // serve, which reads as a choice that was made when it was
                  // only inherited (same rule as SwitchAgentBuilderRuntime).
                  onRuntimeChange(runtime.id);
                  onModelChange("");
                  onThinkingLevelChange("");
                }}
              >
                <span
                  className={cn(
                    "mt-1.5 size-1.5 shrink-0 rounded-full",
                    runtime.status === "online" ? "bg-success" : "bg-muted-foreground/40",
                  )}
                  aria-hidden="true"
                />
                <span className="min-w-0 flex-1">
                  <span className="flex items-center gap-1.5">
                    <ProviderLogo provider={runtime.provider} className="size-3.5 shrink-0" />
                    <span className="truncate">{runtimeDisplayName(runtime)}</span>
                  </span>
                  <span className="mt-0.5 block truncate text-caption text-muted-foreground">
                    {runtime.status === "online"
                      ? runtimeOwnerLabel(runtime, members, t)
                      : t(($) => $.alignment.config_runtime_offline)}
                  </span>
                </span>
                {runtime.id === runtimeId && <Check className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />}
              </Row>
            ))
          )}
        </Section>

        {/* Model */}
        <Section
          icon={<Cpu className="size-3.5" aria-hidden="true" />}
          title={tAgents(($) => $.model_dropdown.label)}
          hint={
            modelsQuery.isLoading
              ? tAgents(($) => $.pickers.model_discovering)
              : modelsQuery.data?.supported === false
                ? t(($) => $.alignment.config_model_managed)
                : t(($) => $.alignment.config_model_hint)
          }
        >
          {modelsQuery.isLoading ? (
            <p className="flex items-center gap-2 px-1 py-2 text-caption text-muted-foreground">
              <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
              {tAgents(($) => $.pickers.model_discovering)}
            </p>
          ) : modelsQuery.data?.supported === false ? (
            <p className="px-1 py-2 text-caption text-muted-foreground">
              {tAgents(($) => $.model_dropdown.managed_by_runtime_hint)}
            </p>
          ) : (
            <>
              <Row
                selected={model === ""}
                onClick={() => {
                  onModelChange("");
                  onThinkingLevelChange("");
                }}
              >
                <span className="min-w-0 flex-1 truncate">
                  {t(($) => $.alignment.config_model_default)}
                </span>
                {model === "" && <Check className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />}
              </Row>
              {models.map((candidate) => (
                <Row
                  key={candidate.id}
                  selected={candidate.id === model}
                  onClick={() => {
                    if (candidate.id === model) return;
                    onModelChange(candidate.id);
                    onThinkingLevelChange("");
                  }}
                >
                  <span className="min-w-0 flex-1">
                    <span className="block truncate">{candidate.label}</span>
                    {candidate.label !== candidate.id && (
                      <span className="mono mt-0.5 block truncate text-muted-foreground">
                        {candidate.id}
                      </span>
                    )}
                  </span>
                  {candidate.id === model && <Check className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />}
                </Row>
              ))}
            </>
          )}
        </Section>

        {/* Think level */}
        <Section
          icon={<Brain className="size-3.5" aria-hidden="true" />}
          title={tAgents(($) => $.inspector.prop_thinking)}
          hint={
            levels.length > 0
              ? t(($) => $.alignment.config_thinking_hint)
              : t(($) => $.alignment.config_thinking_unavailable)
          }
        >
          {levels.length === 0 ? (
            // Stated, not hidden: the panel is where the user looks for it, and
            // an absent control reads as a missing feature rather than as a
            // model that has no reasoning dial.
            <p className="px-1 py-2 text-caption text-muted-foreground">
              {t(($) => $.alignment.config_thinking_none)}
            </p>
          ) : (
            <div className="flex flex-wrap gap-1.5 px-1 py-1">
              <LevelChip
                selected={thinkingLevel === ""}
                label={tAgents(($) => $.pickers.thinking_default)}
                onClick={() => onThinkingLevelChange("")}
              />
              {levels.map((level) => (
                <LevelChip
                  key={level.value}
                  selected={level.value === thinkingLevel}
                  label={level.label}
                  title={level.description}
                  onClick={() => onThinkingLevelChange(level.value)}
                />
              ))}
            </div>
          )}
        </Section>

        {/* Skills */}
        <Section
          icon={<Sparkles className="size-3.5" aria-hidden="true" />}
          title={t(($) => $.alignment.policy_label)}
          hint={t(($) => $.alignment.config_skills_hint, { count: skills.length })}
        >
          {ISSUE_DRAFT_SKILLS.map((key) => {
            const checked = skills.includes(key);
            const lastOne = checked && skills.length === 1;
            return (
              <label
                key={key}
                className={cn(
                  "flex cursor-pointer items-start gap-2 rounded-md px-1.5 py-1.5 transition-colors hover:bg-accent/50",
                  lastOne && "cursor-not-allowed",
                )}
                title={lastOne ? t(($) => $.alignment.skill_last_one_hint) : undefined}
              >
                <input
                  type="checkbox"
                  className="mt-0.5 size-3.5 shrink-0 accent-foreground"
                  checked={checked}
                  disabled={disabled || lastOne}
                  onChange={(event) => toggleSkill(key, event.target.checked)}
                />
                <span className="min-w-0 flex-1">
                  <span className={cn("block", checked && "font-medium")}>
                    {t(($) => $.alignment[SKILL_COPY[key]])}
                  </span>
                  <span className="mt-0.5 block text-caption leading-snug text-muted-foreground">
                    {t(($) => $.alignment[SKILL_DESCRIPTION[key]])}
                  </span>
                </span>
              </label>
            );
          })}
        </Section>
      </PopoverContent>
    </Popover>
  );
}

/**
 * The message key each skill's name and description resolve to.
 *
 * Records rather than ternaries on the key: `ISSUE_DRAFT_SKILLS` is the
 * whitelist this panel renders, so a skill added there without copy here is a
 * compile error rather than a checkbox with a raw registry key for a label.
 */
const SKILL_COPY = {
  grill: "skill_grill",
  wayfinder: "skill_wayfinder",
  frontend: "skill_frontend",
} as const satisfies Record<IssueDraftSkillKey, string>;

const SKILL_DESCRIPTION = {
  grill: "skill_grill_description",
  wayfinder: "skill_wayfinder_description",
  frontend: "skill_frontend_description",
} as const satisfies Record<IssueDraftSkillKey, string>;

/** The pill's one line: machine, model, effort, and how many skills are on. */
function alignmentConfigSummary({
  runtime,
  model,
  thinkingLevel,
  thinkingLabel,
  skills,
  t,
  tAgents,
}: {
  runtime: RuntimeDevice | null;
  model: string;
  thinkingLevel: string;
  thinkingLabel: string;
  skills: readonly IssueDraftSkillKey[];
  t: TFunction<"issues">;
  tAgents: TFunction<"agents">;
}): string {
  if (!runtime) return t(($) => $.alignment.config_pick_runtime);
  const parts = [runtimeDisplayName(runtime)];
  parts.push(model || t(($) => $.alignment.config_model_short));
  // The stored value, not the label: a level the catalog no longer lists must
  // still show as the thing that will be sent, not as "follow the CLI".
  if (thinkingLevel) parts.push(thinkingLabel || thinkingLevel);
  else parts.push(tAgents(($) => $.pickers.thinking_default));
  parts.push(t(($) => $.alignment.config_skills_count, { count: skills.length }));
  return parts.join(" · ");
}

function runtimeOwnerLabel(
  runtime: RuntimeDevice,
  members: MemberWithUser[],
  t: TFunction<"issues">,
): string {
  if (runtime.runtime_mode === "cloud") {
    return t(($) => $.alignment.config_runtime_cloud);
  }
  const owner = members.find((member) => member.user_id === runtime.owner_id);
  return owner?.name ?? runtime.owner_id ?? t(($) => $.alignment.config_runtime_workspace);
}

function Section({
  icon,
  title,
  hint,
  children,
}: {
  icon: React.ReactNode;
  title: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="border-b border-border px-2 py-2 last:border-b-0">
      <div className="flex items-baseline justify-between gap-2 px-1 pb-1">
        <span className="flex items-center gap-1.5 text-caption font-medium">
          <span className="text-muted-foreground">{icon}</span>
          {title}
        </span>
        {hint ? (
          <span className="truncate text-caption text-muted-foreground">{hint}</span>
        ) : null}
      </div>
      <div className="max-h-44 overflow-y-auto">{children}</div>
    </div>
  );
}

function Row({
  selected,
  onClick,
  children,
}: {
  selected: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex w-full items-start gap-2 rounded-md px-1.5 py-1.5 text-left text-caption transition-colors hover:bg-accent/50",
        // Selection on weight + colour, so hovering the selected row cannot
        // make it look like a plain hover (UI rule).
        selected ? "font-medium text-foreground" : "text-muted-foreground",
      )}
    >
      {children}
    </button>
  );
}

function LevelChip({
  selected,
  label,
  title,
  onClick,
}: {
  selected: boolean;
  label: string;
  title?: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={title}
      aria-pressed={selected}
      className={cn(
        "rounded-full border px-2 py-0.5 text-caption transition-colors",
        selected
          ? "border-foreground bg-foreground font-medium text-background"
          : "border-border text-muted-foreground hover:bg-accent/50",
      )}
    >
      {label}
    </button>
  );
}

