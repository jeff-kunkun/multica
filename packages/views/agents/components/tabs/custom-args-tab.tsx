"use client";

import { useEffect, useRef, useState } from "react";
import {
  Check,
  Circle,
  Copy,
  Loader2,
  Pencil,
  Plus,
  Save,
  Terminal,
  Trash2,
} from "lucide-react";
import type { Agent, RuntimeDevice } from "@multica/core/types";
import { createSafeId } from "@multica/core/utils";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { copyText } from "@multica/ui/lib/clipboard";
import { cn } from "@multica/ui/lib/utils";
import { toast } from "sonner";
import { useT } from "../../../i18n";
import {
  SettingsCard,
  SettingsSection,
} from "../../../settings/components/settings-layout";
import {
  type AgyAccountSlot,
  MAX_AGY_ACCOUNT_NUMBER,
  detectAgyAccountSlot,
  formatAgyLoginCommand,
  getGeminiDir,
  isAbsoluteFsPath,
  isGeminiDirToken,
  isIsolatedAccountSlot,
  isNumberedAccountSlot,
  loginDirectory,
  nextAccountNumber,
  normalizeAccountNumbers,
  numberedSlotId,
  parseAccountNumber,
  parseAgySlotsConfig,
  resolveHomeDir,
  resolveSlotDirectory,
  runtimeHomeDir,
  runtimeLoggedInDirs,
  setGeminiDir,
  slotIsSignedIn,
  writeAgySlotsConfig,
} from "./agy-account-slots";

interface ArgEntry {
  id: string;
  value: string;
}

type EditorState =
  | { kind: "add" }
  | { kind: "edit"; entryId: string }
  | null;

function argsToEntries(args: string[]): ArgEntry[] {
  return args.map((value) => ({ id: createSafeId(), value }));
}

function entriesToArgs(entries: ArgEntry[]): string[] {
  return entries.map((entry) => entry.value.trim()).filter(Boolean);
}

function formatArgForPreview(value: string): string {
  return /\s/.test(value) ? JSON.stringify(value) : value;
}

function visibleArgEntries(entries: ArgEntry[]): ArgEntry[] {
  const visible: ArgEntry[] = [];
  for (let index = 0; index < entries.length; index += 1) {
    const entry = entries[index];
    if (!entry) continue;
    if (entry.value === "--gemini_dir") {
      index += 1;
      continue;
    }
    if (isGeminiDirToken(entry.value)) continue;
    visible.push(entry);
  }
  return visible;
}

export function CustomArgsTab({
  agent,
  runtimeDevice,
  onSave,
  onDirtyChange,
}: {
  agent: Agent;
  runtimeDevice?: RuntimeDevice;
  onSave: (updates: Partial<Agent>) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useT("agents");
  const [entries, setEntries] = useState<ArgEntry[]>(
    argsToEntries(agent.custom_args ?? []),
  );
  const isAntigravity = runtimeDevice?.provider?.toLowerCase() === "antigravity";
  const [slot, setSlot] = useState<AgyAccountSlot>(() =>
    detectAgyAccountSlot(getGeminiDir(agent.custom_args ?? [])),
  );
  const [accounts, setAccounts] = useState<number[]>(() =>
    parseAgySlotsConfig(agent.runtime_config, getGeminiDir(agent.custom_args ?? [])),
  );
  const [editor, setEditor] = useState<EditorState>(null);
  const [editorValue, setEditorValue] = useState("");
  const [saving, setSaving] = useState(false);
  const [copied, setCopied] = useState(false);
  const editorInputRef = useRef<HTMLInputElement>(null);

  const currentArgs = entriesToArgs(entries);
  const geminiDir = getGeminiDir(currentArgs);
  const homeDir = resolveHomeDir(geminiDir, runtimeHomeDir(runtimeDevice));
  const originalArgs = agent.custom_args ?? [];
  const originalAccounts = parseAgySlotsConfig(
    agent.runtime_config,
    getGeminiDir(originalArgs),
  );
  const argsDirty = JSON.stringify(currentArgs) !== JSON.stringify(originalArgs);
  const slotsDirty = JSON.stringify(accounts) !== JSON.stringify(originalAccounts);
  const dirty = argsDirty || slotsDirty;
  const visibleEntries = visibleArgEntries(entries);
  const loginPath = loginDirectory(slot, geminiDir, homeDir);
  const loginCommand = formatAgyLoginCommand(loginPath);
  const loggedInDirs = runtimeLoggedInDirs(runtimeDevice);
  const nextAccount = nextAccountNumber(accounts);
  const canAddAccount = nextAccount <= MAX_AGY_ACCOUNT_NUMBER;

  const numberedSlotLabel = (account: number) =>
    account === 1
      ? t(($) => $.tab_body.custom_args.slot_account1_label)
      : t(($) => $.tab_body.custom_args.slot_account_label, { n: account });
  const numberedSlotHint = (account: number) =>
    account === 1
      ? t(($) => $.tab_body.custom_args.slot_account1_hint)
      : t(($) => $.tab_body.custom_args.slot_account_hint, { n: account });
  const slotLabel = isNumberedAccountSlot(slot)
    ? numberedSlotLabel(parseAccountNumber(slot) ?? 1)
    : t(($) => $.tab_body.custom_args.slot_custom_label);

  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);

  useEffect(() => {
    if (editor) editorInputRef.current?.focus();
  }, [editor]);

  const applyGeminiDir = (profile: string) => {
    setEntries(argsToEntries(setGeminiDir(currentArgs, profile)));
  };

  const selectSlot = (next: AgyAccountSlot) => {
    if (isIsolatedAccountSlot(next) && !homeDir) {
      toast.error(t(($) => $.tab_body.custom_args.home_unresolved_toast));
      return;
    }
    setSlot(next);
    if (next === "custom") return;
    applyGeminiDir(resolveSlotDirectory(next, geminiDir, homeDir));
  };

  const addAccount = () => {
    if (!homeDir) {
      toast.error(t(($) => $.tab_body.custom_args.home_unresolved_toast));
      return;
    }
    if (!canAddAccount) return;
    const next = normalizeAccountNumbers([...accounts, nextAccount]);
    setAccounts(next);
    selectSlot(numberedSlotId(nextAccount));
  };

  const removeAccount = (account: number) => {
    if (account <= 1) return;
    const next = normalizeAccountNumbers(accounts.filter((n) => n !== account));
    setAccounts(next);
    if (parseAccountNumber(slot) === account) selectSlot("account1");
  };

  const startAdding = () => {
    setEditor({ kind: "add" });
    setEditorValue("");
  };

  const startEditing = (entry: ArgEntry) => {
    setEditor({ kind: "edit", entryId: entry.id });
    setEditorValue(entry.value);
  };

  const closeEditor = () => {
    setEditor(null);
    setEditorValue("");
  };

  const commitEditor = () => {
    const value = editorValue.trim();
    if (!editor || !value) return;

    if (editor.kind === "add") {
      setEntries((current) => [...current, { id: createSafeId(), value }]);
    } else {
      setEntries((current) =>
        current.map((entry) =>
          entry.id === editor.entryId ? { ...entry, value } : entry,
        ),
      );
    }
    closeEditor();
  };

  const removeEntry = (entryId: string) => {
    setEntries((current) => current.filter((entry) => entry.id !== entryId));
    if (editor?.kind === "edit" && editor.entryId === entryId) closeEditor();
  };

  const handleCopyLogin = () => {
    void copyText(loginCommand).then((ok) => {
      if (!ok) {
        toast.error(t(($) => $.tab_body.custom_args.login_copy_failed_toast));
        return;
      }
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  };

  const handleSave = async () => {
    if (isAntigravity && geminiDir && !isAbsoluteFsPath(geminiDir)) {
      toast.error(t(($) => $.tab_body.custom_args.absolute_path_required_toast));
      return;
    }
    setSaving(true);
    try {
      await onSave(
        isAntigravity
          ? {
              custom_args: currentArgs,
              runtime_config: writeAgySlotsConfig(agent.runtime_config, accounts),
            }
          : { custom_args: currentArgs },
      );
      toast.success(t(($) => $.tab_body.custom_args.saved_toast));
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.tab_body.custom_args.save_failed_toast),
      );
    } finally {
      setSaving(false);
    }
  };

  const renderEditor = (index?: number) => (
    <form
      className="rounded-lg border border-input bg-background p-2.5 shadow-xs"
      onSubmit={(event) => {
        event.preventDefault();
        commitEditor();
      }}
      onKeyDown={(event) => {
        if (event.key === "Escape") closeEditor();
      }}
    >
      <Input
        ref={editorInputRef}
        name={editor?.kind === "add" ? "agent-custom-arg-new" : `agent-custom-arg-${index}`}
        autoComplete="off"
        spellCheck={false}
        value={editorValue}
        onChange={(event) => setEditorValue(event.target.value)}
        placeholder={t(($) => $.tab_body.custom_args.input_placeholder)}
        aria-label={
          editor?.kind === "add"
            ? t(($) => $.tab_body.custom_args.new_argument_aria)
            : t(($) => $.tab_body.custom_args.input_aria, { index })
        }
        className="font-mono text-caption"
      />
      <div className="mt-2 flex justify-end gap-2">
        <Button type="button" variant="ghost" size="sm" onClick={closeEditor}>
          {t(($) => $.tab_body.custom_args.cancel_action)}
        </Button>
        <Button type="submit" size="sm" disabled={!editorValue.trim()}>
          {editor?.kind === "add"
            ? t(($) => $.tab_body.custom_args.add_action)
            : t(($) => $.tab_body.custom_args.update_action)}
        </Button>
      </div>
    </form>
  );

  const launchHeader = runtimeDevice?.launch_header;
  const launchCommand = launchHeader
    ? [launchHeader, ...currentArgs.map(formatArgForPreview)].join(" ")
    : null;

  const slots: Array<{
    id: AgyAccountSlot;
    account: number | null;
    label: string;
    hint: string;
    directory: string;
  }> = [
    ...accounts.map((account) => {
      const id = numberedSlotId(account);
      const directory = loginDirectory(id, account === 1 ? "" : resolveSlotDirectory(id, "", homeDir), homeDir);
      return {
        id,
        account,
        label: numberedSlotLabel(account),
        hint: numberedSlotHint(account),
        directory,
      };
    }),
    {
      id: "custom" as const,
      account: null,
      label: t(($) => $.tab_body.custom_args.slot_custom_label),
      hint: t(($) => $.tab_body.custom_args.slot_custom_hint),
      directory: loginDirectory("custom", geminiDir, homeDir),
    },
  ];

  return (
    <div className="space-y-6">
      <p className="max-w-2xl text-pretty text-body leading-6 text-muted-foreground">
        {t(($) => $.tab_body.custom_args.intro)}
      </p>

      {isAntigravity ? (
        <>
          <SettingsSection
            title={t(($) => $.tab_body.custom_args.antigravity_profile_label)}
            description={t(($) => $.tab_body.custom_args.antigravity_profile_description)}
          >
            <SettingsCard>
              <div className="space-y-3 p-3">
                <div
                  role="radiogroup"
                  aria-label={t(($) => $.tab_body.custom_args.slot_group_aria)}
                  className="grid gap-2"
                >
                  {slots.map((item) => {
                    const selected = slot === item.id;
                    const signedIn = slotIsSignedIn(item.directory, loggedInDirs);
                    return (
                      <div key={item.id} className="flex items-stretch gap-1">
                        <button
                          type="button"
                          role="radio"
                          aria-checked={selected}
                          onClick={() => selectSlot(item.id)}
                          className={cn(
                            "flex min-w-0 flex-1 items-start gap-3 rounded-lg border px-3 py-2.5 text-left transition-colors",
                            selected
                              ? "border-foreground font-medium shadow-[inset_0_0_0_1px_var(--color-foreground)]"
                              : "border-border hover:border-foreground/20 hover:bg-accent/30",
                          )}
                        >
                          <span
                            aria-hidden
                            className={cn(
                              "relative mt-0.5 inline-block size-4 shrink-0 rounded-full border-[1.5px]",
                              selected ? "border-foreground" : "border-border",
                            )}
                          >
                            {selected ? (
                              <span className="absolute inset-[3px] rounded-full bg-foreground" />
                            ) : null}
                          </span>
                          <span className="min-w-0 flex-1">
                            <span className="block text-body leading-5">{item.label}</span>
                            <span className="mt-0.5 block font-normal text-caption leading-5 text-muted-foreground">
                              {item.hint}
                            </span>
                          </span>
                          <span
                            role="img"
                            aria-label={
                              signedIn
                                ? t(($) => $.tab_body.custom_args.slot_signed_in_aria)
                                : t(($) => $.tab_body.custom_args.slot_signed_out_aria)
                            }
                            className="mt-0.5 shrink-0"
                          >
                            {signedIn ? (
                              <Check className="size-3.5 text-success" aria-hidden="true" />
                            ) : (
                              <Circle className="size-3.5 text-muted-foreground/70" aria-hidden="true" />
                            )}
                          </span>
                        </button>
                        {item.account && item.account > 1 ? (
                          <Button
                            type="button"
                            variant="ghost"
                            size="icon-sm"
                            className="mt-1 shrink-0 text-muted-foreground hover:text-destructive"
                            onClick={() => removeAccount(item.account ?? 0)}
                            aria-label={t(($) => $.tab_body.custom_args.slot_remove_aria, {
                              n: item.account,
                            })}
                          >
                            <Trash2 className="size-3.5" aria-hidden="true" />
                          </Button>
                        ) : null}
                      </div>
                    );
                  })}
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="w-full justify-center"
                    onClick={addAccount}
                    disabled={!canAddAccount}
                    aria-label={t(($) => $.tab_body.custom_args.slot_add_aria)}
                  >
                    <Plus className="size-3.5" aria-hidden="true" />
                    {t(($) => $.tab_body.custom_args.slot_add_action)}
                  </Button>
                </div>

                {slot === "custom" ? (
                  <div className="space-y-2">
                    <label
                      className="text-caption font-medium"
                      htmlFor="agy-profile-directory"
                    >
                      {t(($) => $.tab_body.custom_args.antigravity_profile_input_label)}
                    </label>
                    <Input
                      id="agy-profile-directory"
                      value={geminiDir}
                      onChange={(event) => applyGeminiDir(event.target.value.trim())}
                      placeholder={t(
                        ($) => $.tab_body.custom_args.antigravity_profile_placeholder,
                      )}
                      spellCheck={false}
                      autoComplete="off"
                      className="font-mono text-caption"
                    />
                  </div>
                ) : loginPath ? (
                  <p className="text-caption leading-5 text-muted-foreground">
                    <span className="font-medium text-foreground">
                      {t(($) => $.tab_body.custom_args.resolved_path_label)}
                    </span>{" "}
                    <code className="font-mono" translate="no">
                      {loginPath}
                    </code>
                  </p>
                ) : null}
              </div>
            </SettingsCard>
          </SettingsSection>

          <SettingsSection title={t(($) => $.tab_body.custom_args.login_title)}>
            <SettingsCard>
              <div className="space-y-3 p-4">
                <p className="text-body leading-6">
                  {t(($) => $.tab_body.custom_args.login_bound, {
                    slot: slotLabel,
                    path: loginPath,
                  })}
                </p>
                {loginPath ? (
                  <div className="flex items-start gap-2 rounded-lg bg-muted px-3 py-2.5">
                    <code
                      className="min-w-0 flex-1 break-all font-mono text-caption leading-5"
                      translate="no"
                    >
                      {loginCommand}
                    </code>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-sm"
                      onClick={handleCopyLogin}
                      aria-label={
                        copied
                          ? t(($) => $.tab_body.custom_args.login_copied_aria)
                          : t(($) => $.tab_body.custom_args.login_copy_aria)
                      }
                    >
                      {copied ? (
                        <Check className="size-3.5" aria-hidden="true" />
                      ) : (
                        <Copy className="size-3.5" aria-hidden="true" />
                      )}
                    </Button>
                  </div>
                ) : null}
                <p className="text-caption leading-5 text-muted-foreground">
                  {t(($) => $.tab_body.custom_args.login_keychain_hint)}
                </p>
              </div>
            </SettingsCard>
          </SettingsSection>
        </>
      ) : null}

      <SettingsSection
        title={t(($) => $.tab_body.custom_args.arguments_label)}
        description={t(($) => $.tab_body.custom_args.arguments_description)}
        action={
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={startAdding}
            disabled={editor !== null}
          >
            <Plus className="size-3.5" aria-hidden="true" />
            {t(($) => $.tab_body.custom_args.add_argument_action)}
          </Button>
        }
      >
        <SettingsCard>
          <div className="space-y-2 p-3">
            {visibleEntries.length === 0 && editor?.kind !== "add" ? (
              <div className="flex min-h-28 flex-col items-center justify-center px-4 py-6 text-center">
                <span className="flex size-9 items-center justify-center rounded-lg bg-muted text-muted-foreground">
                  <Terminal className="size-4" aria-hidden="true" />
                </span>
                <p className="mt-3 text-body font-medium">
                  {t(($) => $.tab_body.custom_args.empty_title)}
                </p>
                <p className="mt-1 max-w-sm text-caption leading-5 text-muted-foreground">
                  {t(($) => $.tab_body.custom_args.empty_hint)}
                </p>
              </div>
            ) : null}

            <div role="list" className="space-y-2">
              {visibleEntries.map((entry, index) => (
                <div key={entry.id} role="listitem">
                  {editor?.kind === "edit" && editor.entryId === entry.id ? (
                    renderEditor(index + 1)
                  ) : (
                    <div className="group flex min-w-0 items-center gap-3 rounded-lg bg-muted/45 px-3 py-2.5 transition-colors hover:bg-muted/70">
                      <span className="w-5 shrink-0 text-center text-micro font-medium tabular-nums text-muted-foreground">
                        {index + 1}
                      </span>
                      <code
                        className="min-w-0 flex-1 break-all font-mono text-caption leading-5"
                        translate="no"
                      >
                        {entry.value}
                      </code>
                      <div className="flex shrink-0 items-center gap-0.5">
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-sm"
                          onClick={() => startEditing(entry)}
                          disabled={editor !== null}
                          aria-label={t(($) => $.tab_body.custom_args.edit_aria, {
                            index: index + 1,
                          })}
                        >
                          <Pencil className="size-3.5" aria-hidden="true" />
                        </Button>
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-sm"
                          onClick={() => removeEntry(entry.id)}
                          disabled={editor !== null}
                          className="text-muted-foreground hover:text-destructive"
                          aria-label={t(($) => $.tab_body.custom_args.remove_aria, {
                            index: index + 1,
                          })}
                        >
                          <Trash2 className="size-3.5" aria-hidden="true" />
                        </Button>
                      </div>
                    </div>
                  )}
                </div>
              ))}
            </div>

            {editor?.kind === "add" ? renderEditor() : null}
          </div>
        </SettingsCard>
      </SettingsSection>

      {launchCommand ? (
        <SettingsSection title={t(($) => $.tab_body.custom_args.command_preview_label)}>
          <SettingsCard>
            <div className="flex min-w-0 items-start gap-3 p-4">
              <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">
                <Terminal className="size-3.5" aria-hidden="true" />
              </span>
              <code
                className="min-w-0 break-all pt-1.5 font-mono text-caption leading-5"
                translate="no"
              >
                {launchCommand}
              </code>
            </div>
          </SettingsCard>
        </SettingsSection>
      ) : null}

      <div className="flex items-center justify-end gap-3 pt-1">
        {dirty ? (
          <span role="status" className="text-caption text-muted-foreground">
            {t(($) => $.tab_body.common.unsaved_changes)}
          </span>
        ) : null}
        <Button
          onClick={handleSave}
          disabled={!dirty || saving || editor !== null}
          size="sm"
        >
          {saving ? (
            <Loader2
              className="size-3.5 animate-spin motion-reduce:animate-none"
              aria-hidden="true"
            />
          ) : (
            <Save className="size-3.5" aria-hidden="true" />
          )}
          {t(($) => $.tab_body.common.save)}
        </Button>
      </div>
    </div>
  );
}
