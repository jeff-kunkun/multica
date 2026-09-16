"use client";

// Editing half of the agent "accounts" surface (design C5: summary bar +
// drawer).
//
// This is the leaf module of the pair, so the small pieces both halves need to
// render an account the same way live here: the CLI badge/label, the status
// pill and the login command. `agent-accounts-tab.tsx` owns the state, the
// reads and the single write.
//
// Nothing in this file performs a side effect. Picking a row is local state —
// the design's rule is that a selection made inside the drawer only takes
// effect once "save and switch" (owned by the tab) commits it.

import { useState } from "react";
import { Check, Copy, Loader2, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@multica/ui/components/ui/button";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@multica/ui/components/ui/sheet";
import { copyText } from "@multica/ui/lib/clipboard";
import { useIsMobile } from "@multica/ui/hooks/use-mobile";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../../i18n";
import { ProviderLogo } from "../../../runtimes/components/provider-logo";
import {
  type AgentAccount,
  type AgentAccountCli,
  type AgentAccountGroup,
  accountLeverLabel,
  accountSlotNumber,
  accountStatus,
  parseAccountLever,
} from "./agent-accounts-model";
import { formatAgyLoginCommand } from "./agy-account-slots";

/**
 * CLI display names. These are product nouns — the same word in every locale —
 * so they are constants rather than translation keys, the same stance
 * `providerDisplayName` takes for runtime providers.
 */
const CLI_LABELS: Record<AgentAccountCli, string> = {
  dsh: "DSH",
  agy: "Antigravity",
  codex: "Codex",
  claude: "Claude",
  cursor: "Cursor",
};

/** `ProviderLogo` is keyed by runtime provider, which is not the CLI id (`agy` runs Antigravity). */
const CLI_LOGO_PROVIDER: Record<AgentAccountCli, string> = {
  dsh: "dsh",
  agy: "antigravity",
  codex: "codex",
  claude: "claude",
  cursor: "cursor",
};

/** Command names for the CLI families the daemon reports (scripts/agent-cli-command-names.txt). */
const CLI_BINARY: Record<AgentAccountCli, string> = {
  dsh: "dsh",
  agy: "agy",
  codex: "codex",
  claude: "claude",
  cursor: "cursor-agent",
};

export function agentCliLabel(cli: AgentAccountCli): string {
  return CLI_LABELS[cli];
}

/**
 * Row identity. The daemon's contract makes `(cli, account)` unique, and the
 * parser keeps only the first occurrence of a duplicate, so this key is safe
 * as a React key and as the "which row is selected" value.
 */
export function accountKey(
  account: Pick<AgentAccount, "cli" | "account">,
): string {
  return `${account.cli}\u0000${account.account}`;
}

/** The CLI mark plus its name: the design's "CLI 徽标". */
export function AgentCliBadge({
  cli,
  showLabel = true,
  className,
}: {
  cli: AgentAccountCli;
  showLabel?: boolean;
  className?: string;
}) {
  return (
    <span className={cn("flex min-w-0 items-center gap-1.5", className)}>
      <span
        aria-hidden="true"
        className="flex size-5 shrink-0 items-center justify-center rounded-md border border-border bg-muted text-muted-foreground"
      >
        <ProviderLogo provider={CLI_LOGO_PROVIDER[cli]} className="size-3.5" />
      </span>
      {showLabel ? (
        <span className="shrink-0 text-caption font-medium">
          {agentCliLabel(cli)}
        </span>
      ) : null}
    </span>
  );
}

/**
 * The login / self-check command for one account, meant to be run on the
 * machine that hosts the daemon.
 *
 * agy goes through the shared `formatAgyLoginCommand` (its `--gemini_dir` flag
 * is the account selector). An env-lever CLI is selected by the env var the
 * daemon reported as its lever, so the command is that assignment in front of
 * the CLI binary — the same lever the switch writes, just applied by hand. A
 * CLI with no lever has no way to point itself at another directory, so it
 * gets no command rather than an invented one.
 */
export function accountLoginCommand(account: AgentAccount): string {
  const lever = parseAccountLever(account.lever);
  if (lever.kind === "custom_args" && lever.flag === "--gemini_dir") {
    return formatAgyLoginCommand(account.home);
  }
  if (lever.kind === "env") {
    return `${lever.key}=${account.home} ${CLI_BINARY[account.cli]}`;
  }
  return "";
}

/**
 * Existence-only status pill. A credential value never reaches this component:
 * `AgentAccount` carries a `key_ref` name and a `signed_in` flag, nothing more.
 */
export function AccountStatusPill({
  account,
  nowMs,
  className,
}: {
  account: AgentAccount;
  nowMs: number;
  className?: string;
}) {
  const { t } = useT("agents");
  const status = accountStatus(account, nowMs);
  const tone =
    status.kind === "quota_exhausted"
      ? "border-destructive/30 bg-destructive/5 text-destructive"
      : status.kind === "signed_out"
        ? "border-border bg-muted text-muted-foreground"
        : "border-success/30 bg-success/10 text-success";
  const label =
    status.kind === "signed_out"
      ? t(($) => $.tab_body.accounts.status_signed_out)
      : status.kind === "quota_exhausted"
        ? t(($) => $.tab_body.accounts.status_quota_exhausted, {
            time: new Date(status.reset_at_ms).toLocaleString(undefined, {
              month: "short",
              day: "numeric",
              hour: "2-digit",
              minute: "2-digit",
            }),
          })
        : t(($) => $.tab_body.accounts.status_signed_in);
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center gap-1 rounded-full border px-2 py-0.5 text-micro whitespace-nowrap",
        tone,
        className,
      )}
    >
      <span aria-hidden="true" className="size-1.5 rounded-full bg-current" />
      {label}
    </span>
  );
}

/** The design's "使用中" marker for the account the agent is bound to. */
export function AccountActivePill({ className }: { className?: string }) {
  const { t } = useT("agents");
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center gap-1 rounded-full border border-brand/28 bg-brand/7 px-2 py-0.5 text-micro font-medium whitespace-nowrap text-foreground",
        className,
      )}
    >
      <span aria-hidden="true" className="size-1.5 rounded-full bg-brand" />
      {t(($) => $.tab_body.accounts.active_badge)}
    </span>
  );
}

/**
 * Copyable monospace command row. Used for an account's login/self-check
 * command in the drawer and for the empty state's setup commands, so the two
 * entry points cannot drift apart.
 */
export function CommandBlock({
  command,
  className,
}: {
  command: string;
  className?: string;
}) {
  const { t } = useT("agents");
  const [copied, setCopied] = useState(false);

  const handleCopy = () => {
    void copyText(command).then((ok) => {
      if (!ok) {
        toast.error(t(($) => $.tab_body.accounts.login_copy_failed_toast));
        return;
      }
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  };

  return (
    <div
      className={cn(
        "flex min-w-0 items-start gap-2 rounded-md bg-foreground/95 px-2.5 py-2",
        className,
      )}
    >
      <code
        className="min-w-0 flex-1 break-all font-mono text-micro leading-5 text-background"
        translate="no"
      >
        {command}
      </code>
      <Button
        type="button"
        variant="ghost"
        size="icon-xs"
        className="text-background/70 hover:bg-background/10 hover:text-background"
        onClick={handleCopy}
        aria-label={
          copied
            ? t(($) => $.tab_body.accounts.login_copied_aria)
            : t(($) => $.tab_body.accounts.login_copy_aria)
        }
      >
        {copied ? (
          <Check className="size-3" aria-hidden="true" />
        ) : (
          <Copy className="size-3" aria-hidden="true" />
        )}
      </Button>
    </div>
  );
}

export interface AgentAccountDrawerProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Accounts grouped by CLI in the contract's fixed order. */
  groups: readonly AgentAccountGroup[];
  /** Account currently in effect, or null while it cannot be determined. */
  current: AgentAccount | null;
  /** Key of the row selected in the drawer (`cli\u0000account`), or null. */
  selectedKey: string | null;
  onSelect: (account: AgentAccount) => void;
  /** True when the selection differs from the account in effect. */
  dirty: boolean;
  saving: boolean;
  onSave: () => void;
  nowMs: number;
  /**
   * Editing of the agent's numbered AGY slot pool (`runtime_config.agy_slots`),
   * rendered inside the agy group. The pool is agent config rather than a
   * daemon report, so the tab owns the draft and this drawer only renders and
   * reports the intent — nothing here writes before "save and switch".
   *
   * Absent when the report has no agy group: no other CLI has a numbered pool.
   */
  slotPool?: {
    /** Pending pool the caller writes on save. */
    numbers: readonly number[];
    /** False once the pool reached the server's own cap. */
    canAdd: boolean;
    onAdd: () => void;
    onRemove: (number: number) => void;
  };
}

/**
 * Right-side drawer on desktop, bottom sheet on phones (same content, full
 * width, footer buttons side by side).
 */
export function AgentAccountDrawer({
  open,
  onOpenChange,
  groups,
  current,
  selectedKey,
  onSelect,
  dirty,
  saving,
  onSave,
  nowMs,
  slotPool,
}: AgentAccountDrawerProps) {
  const { t } = useT("agents");
  const isMobile = useIsMobile();
  const total = groups.reduce((sum, group) => sum + group.accounts.length, 0);
  const selected =
    groups
      .flatMap((group) => group.accounts)
      .find((account) => accountKey(account) === selectedKey) ?? null;
  const [adding, setAdding] = useState(false);

  const command = selected ? accountLoginCommand(selected) : "";

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side={isMobile ? "bottom" : "right"}
        className={cn(
          // The design's 520px drawer; phones get the full width sheet.
          "gap-0 p-0 max-md:max-h-[85vh]",
          "data-[side=right]:w-full data-[side=right]:sm:w-[520px] data-[side=right]:sm:max-w-[520px]",
        )}
      >
        <SheetHeader className="gap-1 border-b border-border p-4">
          <SheetTitle className="text-title-sm">
            {t(($) => $.tab_body.accounts.drawer_title)}
          </SheetTitle>
          <SheetDescription className="text-caption">
            {t(($) => $.tab_body.accounts.drawer_counts, {
              clis: groups.length,
              accounts: total,
            })}
          </SheetDescription>
        </SheetHeader>

        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
          {groups.map((group) => {
            // The numbered pool is an agy concept: its rows are the only ones
            // keyed by a numbered directory, and its lever is the only
            // `custom_args` lever this surface writes.
            const pool = group.cli === "agy" ? slotPool : undefined;
            return (
              <section key={group.cli} className="space-y-2">
                <div className="flex min-w-0 items-baseline gap-1.5 px-0.5 text-micro tracking-wide text-faint-foreground uppercase">
                  <span className="shrink-0 font-medium">
                    {agentCliLabel(group.cli)}
                  </span>
                  {accountLeverLabel(group.lever) ? (
                    <>
                      <span aria-hidden="true">·</span>
                      <span className="truncate font-mono normal-case" translate="no">
                        {accountLeverLabel(group.lever)}
                      </span>
                    </>
                  ) : null}
                </div>

                {group.switchable ? null : (
                  <p className="px-0.5 text-caption text-muted-foreground">
                    {t(($) => $.tab_body.accounts.group_readonly_hint)}
                  </p>
                )}

                <div
                  className="overflow-hidden rounded-lg border border-border bg-background"
                  {...(group.switchable
                    ? {
                        role: "radiogroup" as const,
                        "aria-label": t(
                          ($) => $.tab_body.accounts.drawer_groups_aria,
                          { cli: agentCliLabel(group.cli) },
                        ),
                      }
                    : {})}
                >
                  {group.accounts.map((account, index) => {
                    const key = accountKey(account);
                    const isCurrent =
                      current != null && accountKey(current) === key;
                    const isSelected = key === selectedKey;
                    const slotNumber = pool ? accountSlotNumber(account) : null;
                    const pooled =
                      slotNumber !== null &&
                      pool !== undefined &&
                      pool.numbers.includes(slotNumber);
                    // Account 1 is the CLI's own directory: always in the pool,
                    // never removable.
                    const removable = pooled && slotNumber !== null && slotNumber > 1;
                    // A reported account the pending pool no longer covers:
                    // still listed and switchable, but out of the rotation.
                    const dropped =
                      slotNumber !== null && !pooled && slotNumber > 1;
                    const rowClassName = cn(
                      "flex w-full min-w-0 items-center gap-2.5 px-3 py-2.5 text-left",
                      // A selected row keeps its selected background on hover —
                      // the design's rule is that the state must stay readable
                      // while the pointer is on it.
                      isSelected && "bg-surface-selected hover:bg-surface-selected",
                      !isSelected && group.switchable && "hover:bg-surface-hover",
                      !isSelected && !group.switchable && "opacity-70",
                    );
                    const body = (
                      <>
                        <AgentCliBadge cli={account.cli} showLabel={false} />
                        <span className="min-w-0 flex-1">
                          <span
                            className={cn(
                              "block truncate text-caption",
                              isSelected ? "font-semibold" : "font-medium",
                            )}
                          >
                            {account.account}
                          </span>
                          <span
                            className="block truncate font-mono text-micro text-muted-foreground"
                            translate="no"
                          >
                            {account.home}
                          </span>
                          {dropped ? (
                            <span className="block truncate text-micro text-faint-foreground">
                              {t(($) => $.tab_body.accounts.slot_dropped_hint)}
                            </span>
                          ) : null}
                        </span>
                        {isCurrent ? (
                          <AccountActivePill />
                        ) : (
                          <AccountStatusPill account={account} nowMs={nowMs} />
                        )}
                        {group.switchable ? (
                          <span
                            aria-hidden="true"
                            className={cn(
                              "shrink-0 rounded-md border px-1.5 py-0.5 text-micro",
                              isSelected
                                ? "border-foreground/30 text-foreground"
                                : "border-border text-muted-foreground",
                            )}
                          >
                            {t(($) => $.tab_body.accounts.row_edit_action)}
                          </span>
                        ) : null}
                      </>
                    );

                    const row = group.switchable ? (
                      <button
                        type="button"
                        role="radio"
                        aria-checked={isSelected}
                        onClick={() => onSelect(account)}
                        className={cn(rowClassName, "flex-1")}
                      >
                        {body}
                      </button>
                    ) : (
                      <div className={cn(rowClassName, "flex-1")}>{body}</div>
                    );

                    // A remove control cannot live inside the row: the row is
                    // itself a button, so the trash sits beside it, under the
                    // same divider.
                    return (
                      <div
                        key={key}
                        className={cn(
                          "flex w-full min-w-0 items-stretch",
                          index > 0 && "border-t border-border",
                        )}
                      >
                        {row}
                        {removable && slotNumber !== null ? (
                          <Button
                            type="button"
                            variant="ghost"
                            size="icon-xs"
                            className="me-1.5 shrink-0 self-center text-muted-foreground hover:text-destructive"
                            onClick={() => pool?.onRemove(slotNumber)}
                            aria-label={t(
                              ($) => $.tab_body.accounts.slot_remove_aria,
                              { n: slotNumber },
                            )}
                          >
                            <Trash2 className="size-3.5" aria-hidden="true" />
                          </Button>
                        ) : null}
                      </div>
                    );
                  })}
                </div>

                {pool ? (
                  <div className="space-y-1.5">
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      className="w-full justify-center"
                      onClick={pool.onAdd}
                      disabled={!pool.canAdd}
                      aria-label={t(($) => $.tab_body.accounts.slot_add_aria)}
                    >
                      <Plus className="size-3.5" aria-hidden="true" />
                      {t(($) => $.tab_body.accounts.slot_add_action)}
                    </Button>
                    <p className="px-0.5 text-caption leading-5 text-muted-foreground">
                      {t(($) => $.tab_body.accounts.slot_pool_hint)}
                    </p>
                  </div>
                ) : null}
              </section>
            );
          })}

          {selected ? (
            <section className="space-y-2 rounded-lg border border-border bg-muted/40 p-3">
              <p className="text-caption font-medium">
                {t(($) => $.tab_body.accounts.login_title)}
              </p>
              {command ? (
                <CommandBlock command={command} />
              ) : (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.tab_body.accounts.group_readonly_hint)}
                </p>
              )}
              {selected.key_ref ? (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.tab_body.accounts.credential_label, {
                    key: selected.key_ref,
                    state: selected.signed_in
                      ? t(($) => $.tab_body.accounts.credential_signed_in)
                      : t(($) => $.tab_body.accounts.credential_signed_out),
                  })}
                </p>
              ) : null}
              {selected.cli === "agy" ? (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.tab_body.accounts.login_keychain_hint)}
                </p>
              ) : null}
              <p className="text-caption text-muted-foreground">
                {t(($) => $.tab_body.accounts.login_directory_notice)}
              </p>
            </section>
          ) : null}

          {adding ? (
            <p className="rounded-lg border border-dashed border-border p-3 text-caption text-muted-foreground">
              {t(($) => $.tab_body.accounts.add_hint)}
            </p>
          ) : null}
        </div>

        <SheetFooter className="flex-row items-center justify-between gap-2 border-t border-border p-4">
          {/* A pool group owns its own add control, so the generic "how do I
              create an account" explainer would be a second, inert button with
              the same label. */}
          {slotPool ? null : (
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="flex-1 sm:flex-none"
              aria-expanded={adding}
              onClick={() => setAdding((value) => !value)}
            >
              <Plus className="size-3.5" aria-hidden="true" />
              {t(($) => $.tab_body.accounts.add_action)}
            </Button>
          )}
          <Button
            type="button"
            size="sm"
            className={cn("flex-1 sm:flex-none", slotPool && "ms-auto")}
            disabled={!dirty || saving}
            onClick={onSave}
          >
            {saving ? (
              <Loader2
                className="size-3.5 animate-spin motion-reduce:animate-none"
                aria-hidden="true"
              />
            ) : null}
            {t(($) => $.tab_body.accounts.save_action)}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}
